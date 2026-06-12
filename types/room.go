package types

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/ohhcgan/lattice/utils"
)

type RoomStatus uint8

const (
	DefaultTickRate            = 50 * time.Millisecond
	DefaultMaxClients      int = -1
	BroadcastChanBuffering int = 256
)

const (
	UN_INITIALIZED RoomStatus = iota
	STARTED
	DESTROYING
	DESTROYED
)

type BroadcastMessage struct {
	topic          string
	excludeClients []string
	data           any
}

type Room struct {
	//
	// Private properties
	//
	id               string
	name             string
	tickRate         time.Duration
	autoDestroy      bool
	metadata         map[string]any
	status           RoomStatus
	broadcastOnJoin  bool
	broadcastOnLeave bool
	maxClients       int
	locked           bool
	createdAt        time.Time

	//
	// Channels
	//
	broadcast    chan BroadcastMessage
	updateTicked chan time.Duration
	SendMessage  chan Message

	//
	// Concurrency
	//
	wg          sync.WaitGroup
	ctx         context.Context
	mu          sync.RWMutex
	cancel      context.CancelFunc
	destroyOnce sync.Once

	//
	// Tick tracking
	//
	lastTickTime time.Time

	//
	// Internal server hook — called when room is fully destroyed so the server
	// can remove it from its room map (used by autoDestroy).
	//
	serverRemoveFunc func(roomID string)

	//
	// Public properties
	//
	State   RoomState
	clients map[string]*Client

	//
	// Redis
	//
	RedisConnector utils.RClient

	//
	// onMessage and subscribers
	//
	onMessageHandlers map[string]func(*Client, Message)
	// onPublishHandlers map[string]func(string) // TODO

	//
	// Lifecycle methods
	//
	OnCreate      func(*Room) error
	OnAuth        func(*Client) (any, error)
	OnJoin        func(*Client, any)
	OnLeave       func(*Client)
	OnTick        func(delta time.Duration)
	OnStateChange func(*Room, any) error
	OnDestroy     func()
}

func New() *Room {
	ctx, cancel := context.WithCancel(context.Background())

	return &Room{
		tickRate:          DefaultTickRate,
		autoDestroy:       false,
		metadata:          make(map[string]any),
		ctx:               ctx,
		cancel:            cancel,
		maxClients:        DefaultMaxClients,
		locked:            false,
		clients:           make(map[string]*Client),
		State:             nil,
		onMessageHandlers: make(map[string]func(*Client, Message)),
		createdAt:         time.Now(),
		lastTickTime:      time.Now(),
		updateTicked:      make(chan time.Duration, 1),
		broadcast:         make(chan BroadcastMessage, BroadcastChanBuffering),
		SendMessage:       make(chan Message, 256),
		status:            UN_INITIALIZED,
	}
}

func (r *Room) Run() error {
	ticker := time.NewTicker(r.GetTickRate())
	defer ticker.Stop()

	if err := r.setRoomStatus(STARTED); err != nil {
		return err
	}

	for {
		select {
		case msg, ok := <-r.broadcast:
			if !ok {
				return nil
			}
			r.broadcastHandler(msg)

		case <-ticker.C:
			r.onTick()

		case updatedTickRate, ok := <-r.updateTicked:
			if !ok {
				return nil
			}
			ticker.Stop()
			select {
			case <-ticker.C:
			default:
			}
			ticker = time.NewTicker(updatedTickRate)

		case msg, ok := <-r.SendMessage:
			if !ok {
				return nil
			}

			r.mu.RLock()
			client, clientExists := r.clients[msg.SentBy]
			cb, cbExist := r.onMessageHandlers[msg.Type]
			cbUnhandled, cbUnhandledExists := r.onMessageHandlers["*"]
			r.mu.RUnlock()

			if !clientExists {
				continue
			}
			if cbExist {
				cb(client, msg)
			} else if !cbExist && cbUnhandledExists {
				cbUnhandled(client, msg)
			} else if !cbExist && !cbUnhandledExists {
				fmt.Printf("Unhandled event %v\n", msg.Type)
			}

		case <-r.ctx.Done():
			return nil
		}
	}
}

func (r *Room) LockRoom() bool {
	r.mu.Lock()
	prev := r.locked
	r.locked = true
	r.mu.Unlock()

	return prev
}

func (r *Room) SetMaxClients(max int) error {
	if max == 0 || max < DefaultMaxClients {
		return fmt.Errorf("invalid maxClients: use -1 for unlimited or a positive value")
	}
	if count := r.GetClientCount(); count > max {
		return fmt.Errorf("room already contains more clients than provided value")
	}
	r.mu.Lock()
	r.maxClients = max
	r.mu.Unlock()

	return nil
}

func (r *Room) SetMetadata(key string, val any) {
	r.mu.Lock()
	r.metadata[key] = val
	r.mu.Unlock()
}

func (r *Room) GetMetadata() map[string]any {
	// returning a copy, because map is returned as
	// a reference to original data
	r.mu.RLock()
	copy := make(map[string]any, len(r.metadata))
	maps.Copy(copy, r.metadata)
	r.mu.RUnlock()

	return copy
}

func (r *Room) DisableBroadcastJoin() {
	r.mu.Lock()
	r.broadcastOnJoin = false
	r.mu.Unlock()
}

func (r *Room) EnableBroadcastJoin() {
	r.mu.Lock()
	r.broadcastOnJoin = true
	r.mu.Unlock()
}

func (r *Room) DisableBroadcastLeave() {
	r.mu.Lock()
	r.broadcastOnLeave = false
	r.mu.Unlock()
}

func (r *Room) EnableBroadcastLeave() {
	r.mu.Lock()
	r.broadcastOnLeave = true
	r.mu.Unlock()
}

func (r *Room) GetMaxClients() int {
	r.mu.RLock()
	maxClients := r.maxClients
	r.mu.RUnlock()

	return maxClients
}

func (r *Room) UnlockRoom() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == DESTROYING || r.status == DESTROYED {
		return fmt.Errorf("cannot unlock room")
	}
	r.locked = false

	return nil
}

func (r *Room) GetRoomLockStatus() bool {
	r.mu.RLock()
	locked := r.locked
	r.mu.RUnlock()

	return locked
}

// TODO
// Cannot set id to already created room
func (r *Room) SetId(id string) error {
	if id == "" {
		return fmt.Errorf("id cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == DESTROYING || r.status == DESTROYED {
		return fmt.Errorf("cannot set id, room is in DESTROYING or DESTROYED state")
	}
	r.id = id

	return nil
}

func (r *Room) GetId() string {
	r.mu.RLock()
	id := r.id
	r.mu.RUnlock()

	return id
}

func (r *Room) SetName(name string) error {
	if name == "" {
		return fmt.Errorf("name can't be empty")
	}
	r.mu.Lock()
	r.name = name
	r.mu.Unlock()

	return nil
}

func (r *Room) GetName() string {
	r.mu.RLock()
	name := r.name
	r.mu.RUnlock()

	return name
}

func (r *Room) SetTickRate(tickRate time.Duration) error {
	if tickRate <= 0 {
		return fmt.Errorf("invalid tickRate")
	}
	r.mu.Lock()
	r.tickRate = tickRate
	r.mu.Unlock()

	select {
	case r.updateTicked <- tickRate:
		return nil
	case <-r.ctx.Done():
		return fmt.Errorf("room is shutting down")
	}
}

func (r *Room) GetTickRate() time.Duration {
	r.mu.RLock()
	tickRate := r.tickRate
	r.mu.RUnlock()

	return tickRate
}

func (r *Room) SetAutoDestroy(autoDestroy bool) {
	r.mu.Lock()
	r.autoDestroy = autoDestroy
	r.mu.Unlock()
}

func (r *Room) GetAutoDestroy() bool {
	r.mu.RLock()
	autoDestroy := r.autoDestroy
	r.mu.RUnlock()

	return autoDestroy
}

func (r *Room) GetClientCount() int {
	r.mu.RLock()
	count := len(r.clients)
	r.mu.RUnlock()

	return count
}

func (r *Room) AddClient(client *Client) error {
	if client.SessionID == "" {
		return fmt.Errorf("unauthorized")
	}

	var authResp any
	if r.OnAuth != nil {
		var err error
		if authResp, err = r.OnAuth(client); authResp == nil || err != nil {
			return fmt.Errorf("unauthorized %v", err)
		}
	}

	r.mu.Lock()
	if r.status != STARTED {
		r.mu.Unlock()
		return fmt.Errorf("room is not STARTED")
	}

	if r.locked {
		r.mu.Unlock()
		return fmt.Errorf("room is locked")
	}

	if r.maxClients != DefaultMaxClients && len(r.clients) >= r.maxClients {
		r.mu.Unlock()
		return fmt.Errorf("room is full")
	}

	r.clients[client.SessionID] = client
	client.Room = r

	broadcastOnJoin := r.broadcastOnJoin
	state := r.State

	r.mu.Unlock()

	if broadcastOnJoin {
		r.Broadcast(
			PlayerJoined, []string{client.SessionID},
			fmt.Sprintf("Client joined %v", client.SessionID),
		)
	}

	client.Send(RoomJoined, map[string]any{
		"roomId": r.GetId(),
		"name":   r.GetName(),
	})

	if r.OnJoin != nil {
		r.OnJoin(client, authResp)
	}

	if state != nil {
		client.Send(RoomStateChange, state.GetState())
	}

	return nil
}

func (r *Room) GetRoomStatus() RoomStatus {
	r.mu.RLock()
	status := r.status
	r.mu.RUnlock()

	return status
}

func (r *Room) setRoomStatus(newStatus RoomStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status != UN_INITIALIZED && newStatus == STARTED {
		return fmt.Errorf("cannot move from %v to %v", r.status, newStatus)
	}
	if r.status != STARTED && newStatus == DESTROYING {
		return fmt.Errorf("cannot move from %v to %v", r.status, newStatus)
	}
	if r.status != DESTROYING && newStatus == DESTROYED {
		return fmt.Errorf("cannot move from %v to %v", r.status, newStatus)
	}

	switch newStatus {
	case UN_INITIALIZED:
	case STARTED, DESTROYING, DESTROYED:
		r.status = newStatus
	default:
		return fmt.Errorf("illegal status")
	}

	return nil
}

func (r *Room) Destroy() error {
	if err := r.setRoomStatus(DESTROYING); err != nil {
		return fmt.Errorf("room already shutting down or not started: %w", err)
	}

	r.destroyOnce.Do(func() {
		r.cancel()
		r.LockRoom()
		close(r.broadcast)
		close(r.updateTicked)
	})

	r.mu.Lock()
	for _, client := range r.clients {
		client.Close()
	}
	r.clients = make(map[string]*Client)
	r.mu.Unlock()

	if r.OnDestroy != nil {
		r.OnDestroy()
	}

	r.setRoomStatus(DESTROYED)

	if r.serverRemoveFunc != nil {
		r.serverRemoveFunc(r.GetId())
	}

	return nil
}

func (r *Room) RemoveClient(client *Client) error {
	r.mu.Lock()

	if _, has := r.clients[client.SessionID]; !has {
		r.mu.Unlock()
		return fmt.Errorf("client not found")
	}

	delete(r.clients, client.SessionID)
	clientCount := len(r.clients)
	broadcastOnLeave := r.broadcastOnLeave
	r.mu.Unlock()

	if broadcastOnLeave {
		r.Broadcast(
			PlayerLeft, []string{client.SessionID},
			fmt.Sprintf("Client left %v", client.SessionID),
		)
	}

	if r.OnLeave != nil {
		r.OnLeave(client)
	}

	if r.autoDestroy && clientCount == 0 {
		r.Destroy()
	}
	return nil
}

func (r *Room) OnMessage(topic string, handler func(*Client, Message)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == DESTROYED || r.status == DESTROYING {
		return fmt.Errorf("room is in DESTROYED or DESTROYING state")
	}

	if handler == nil {
		return fmt.Errorf("onMessage handler cannot be nil")
	}

	_, ok := r.onMessageHandlers[topic]
	if ok {
		return fmt.Errorf("onMessage handler already added for %v", topic)
	}

	r.onMessageHandlers[topic] = handler
	return nil
}

func (r *Room) BroadcastAll(topic string, data any) error {
	return r.Broadcast(topic, nil, data)
}

func (r *Room) Broadcast(topic string, exclude []string, data any) error {
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}

	if r.GetRoomStatus() != STARTED {
		return fmt.Errorf("room is not running")
	}

	select {
	case r.broadcast <- BroadcastMessage{
		topic:          topic,
		excludeClients: exclude,
		data:           data,
	}:
		return nil
	case <-r.ctx.Done():
		return fmt.Errorf("room is shutting down")
	}
}

func (r *Room) broadcastHandler(msg BroadcastMessage) {
	fmt.Printf("topic=%v, data=%v, sender=%v\n", msg.topic, msg.data, msg.excludeClients)

	r.mu.RLock()
	copy := make([]*Client, 0, len(r.clients))
	for _, client := range r.clients {
		copy = append(copy, client)
	}
	r.mu.RUnlock()

	for _, client := range copy {
		if !slices.Contains(msg.excludeClients, client.SessionID) {
			client.Send(msg.topic, msg.data)
		}
	}

}

func (r *Room) GetClients() []*Client {
	r.mu.RLock()
	out := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, c)
	}
	r.mu.RUnlock()
	return out
}

func (r *Room) onTick() {
	now := time.Now()
	delta := now.Sub(r.lastTickTime)
	r.lastTickTime = now

	if r.OnTick != nil {
		r.OnTick(delta)
	}
}

//
// TODO
// func (r *Room) OnPublishToTopic(topic string, cb func(string)) {
// 	r.mu.Lock()
//
// 	_, ok := r.onPublishHandlers[topic]
// 	if ok {
// 		fmt.Printf("topic %v is already subscribed\n", topic)
// 		return
// 	}
// 	r.onPublishHandlers[topic] = cb
// 	r.mu.Unlock()
//
// 	r.topicSubHandler(topic, cb)
// }
//
// func (r *Room) topicSubHandler(topic string, cb func(string)) {
// 	r.RedisConnector.Subscribe(topic, cb)
// }
//

// TODO: leave code to OnLeave
// TODO: send client option to OnJoin
// TODO: add option field to client

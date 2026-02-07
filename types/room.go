package types

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/chicasso/lattice/utils"
)

type RoomStatus uint8

const (
	DefaultTickRate       = 50 * time.Millisecond
	DefaultMaxClients int = -1
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
	id              string
	name            string
	tickRate        time.Duration
	autoDestroy     bool
	metadata        map[string]any
	status          RoomStatus
	broadcastOnJoin bool
	maxClients      int
	locked          bool
	createdAt       time.Time

	//
	// Channels
	//
	broadcast    chan BroadcastMessage
	updateTicked chan time.Duration

	//
	// Concurrency
	//
	wg     sync.WaitGroup
	ctx    context.Context
	mu     sync.RWMutex
	cancel context.CancelFunc // TODO

	//
	// Public properties
	//
	State   RoomState
	Clients map[string]*Client

	//
	// Redis
	//
	RedisConnector utils.RClient

	//
	// onMessage and subscribers
	//
	onMessageHandlers map[string]func(*Client, any)
	// onPublishHandlers map[string]func(string)

	//
	// Lifecycle methods
	//
	OnCreate      func(*Room) error
	OnAuth        func(*Client) (any, error)
	OnJoin        func(*Client, any)
	OnLeave       func(*Client)
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
		Clients:           make(map[string]*Client),
		State:             nil,
		onMessageHandlers: make(map[string]func(*Client, any)),
		createdAt:         time.Now(),
		updateTicked:      make(chan time.Duration),
		broadcast:         make(chan BroadcastMessage),
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
	if max <= 0 {
		return fmt.Errorf("invalid maxClients provided")
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
	count := len(r.Clients)
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

	if r.maxClients != DefaultMaxClients && len(r.Clients) >= r.maxClients {
		r.mu.Unlock()
		return fmt.Errorf("room is full")
	}

	r.Clients[client.SessionID] = client
	client.Room = r
	r.mu.Unlock()

	if r.broadcastOnJoin {
		r.Broadcast(
			"player_joined", []string{client.SessionID},
			fmt.Sprintf("Client joined %v", client.SessionID),
		)
	}

	if r.OnJoin != nil {
		r.OnJoin(client, authResp)
	}

	if r.State != nil {
		client.SendMessage(RoomStateChange, r.State.GetState())
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
	status := r.GetRoomStatus()
	if status == DESTROYED || status == DESTROYING {
		return fmt.Errorf("room already shutting down")
	}

	if err := r.setRoomStatus(DESTROYING); err != nil {
		return err
	}

	r.cancel()
	r.LockRoom()

	close(r.broadcast)
	close(r.updateTicked)

	r.mu.Lock()
	for _, client := range r.Clients {
		client.Close()
	}
	r.Clients = make(map[string]*Client)
	r.mu.Unlock()

	if r.OnDestroy != nil {
		r.OnDestroy()
	}

	r.setRoomStatus(DESTROYED)

	return nil
}

func (r *Room) RemoveClient(client *Client) error {
	r.mu.Lock()

	if _, has := r.Clients[client.SessionID]; !has {
		r.mu.Unlock()
		return fmt.Errorf("client not found")
	}

	delete(r.Clients, client.SessionID)
	clientCount := len(r.Clients)

	r.mu.Unlock()

	if r.OnLeave != nil {
		r.OnLeave(client)
	}

	if r.autoDestroy && clientCount == 0 {
		r.Destroy()
	}
	return nil
}

func (r *Room) OnMessage(topic string, handler func(*Client, any)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == DESTROYED || r.status == DESTROYING {
		return fmt.Errorf("room is in DESTROYED or DESTROYING state")
	}

	_, ok := r.onMessageHandlers[topic]
	if ok {
		return fmt.Errorf("onMessage handler already added for %v", topic)
	}

	r.onMessageHandlers[topic] = handler
	return nil
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
	copy := make([]*Client, 0, len(r.Clients))
	for _, client := range r.Clients {
		copy = append(copy, client)
	}
	r.mu.RUnlock()

	for _, client := range copy {
		if !slices.Contains(msg.excludeClients, client.SessionID) {
			client.SendMessage(msg.topic, msg.data)
		}
	}

}

// TODO
func (r *Room) onTick() {}

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

package types

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Room struct {
	//
	// Private properties
	//
	id          string
	name        string
	mu          sync.RWMutex
	tickRate    time.Duration
	autoDestroy bool

	//
	// Public properties
	//
	MaxClients int
	Locked     bool
	Clients    map[string]*Client
	State      RoomState

	//
	// closeChan  chan struct{}
	//

	//
	// Lifecycle methods
	//
	OnMessage func(*Client, string, json.RawMessage)
	OnJoin    func(*Client)
	OnLeave   func(*Client)
	OnDestroy func()
	OnAuth    func(*Client) error
}

func (r *Room) SetId(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.id = id
}

func (r *Room) GetId() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.id
}

func (r *Room) SetName(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.name = name
}

func (r *Room) GetName() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.name
}

func (r *Room) SetTickRate(tickRate time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tickRate = tickRate
}

func (r *Room) GetTickRate() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.tickRate
}

func (r *Room) SetAutoDestroy(autoDestroy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.autoDestroy = autoDestroy
}

func (r *Room) GetAutoDestroy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.autoDestroy
}

func (r *Room) AddClient(client *Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	err := r.OnAuth(client)
	if err != nil {
		return fmt.Errorf("Unauthorized %v", err)
	}

	if r.Locked {
		return fmt.Errorf("room is locked")
	}

	if len(r.Clients) >= r.MaxClients {
		return fmt.Errorf("room is full")
	}

	r.Clients[client.SessionID] = client
	client.Room = r

	if r.State != nil {
		client.SendMessage(RoomStateChange, r.State.GetState())
	}

	if r.OnJoin != nil {
		go r.OnJoin(client)
	}

	return nil
}

func (r *Room) Destroy() {
	//
	// close(r.closeChan)
	//

	r.mu.Lock()
	for _, client := range r.Clients {
		close(client.Send)
	}
	r.Clients = make(map[string]*Client)
	r.mu.Unlock()

	if r.OnDestroy != nil {
		go r.OnDestroy()
	}
}

func (r *Room) RemoveClient(client *Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, has := r.Clients[client.SessionID]; !has {
		return fmt.Errorf("client not found")
	}

	delete(r.Clients, client.SessionID)
	clientCount := len(r.Clients)

	if r.OnLeave != nil {
		go r.OnLeave(client)
	}

	if autoDestroy := r.GetAutoDestroy(); autoDestroy &&
		clientCount == 0 {
		go r.Destroy()
	}

	return nil
}

package types

import (
	"sync"

	"github.com/gobwas/ws"
)

type Server struct {
	rooms       map[string]*Room
	roomFactory map[string]func() *Room
	mu          sync.RWMutex
	upgrader    ws.Upgrader
}

type ServerConfig struct {
	ws.Upgrader
}

func (s *Server) DefineRoom(name string, factory func() *Room) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.roomFactory[name] = factory
}

func NewServer(serverConfig ServerConfig) *Server {
	return &Server{
		rooms:       make(map[string]*Room),
		roomFactory: make(map[string]func() *Room),
		upgrader:    serverConfig.Upgrader,
	}
}

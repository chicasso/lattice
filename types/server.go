package types

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chicasso/lattice/utils"
	"github.com/gobwas/ws"
	"golang.org/x/time/rate"
)

type Server struct {
	rooms      map[string]*Room
	mu         sync.RWMutex
	factories  map[string]func() *Room
	httpServer *http.Server

	// Limits
	maxConnections    int32
	activeConnections atomic.Int32
	connLimiter       *rate.Limiter

	// Graceful shutdown
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Metrics
	totalConnections atomic.Uint64
	totalDisconnects atomic.Uint64
	startTime        time.Time
	isShuttingDown   atomic.Bool
}

type ServerConfig struct {
	MaxConnections int32 // Maximum concurrent connections
	// RateLimit       rate.Limit    // Connections per second
	// RateBurst       int           // Burst size for rate limiter
	ReadTimeout     time.Duration // HTTP read timeout
	WriteTimeout    time.Duration // HTTP write timeout
	IdleTimeout     time.Duration // HTTP idle timeout
	ShutdownTimeout time.Duration // Graceful shutdown timeout
}

// func DefaultServerConfig() ServerConfig {
// 	return ServerConfig{
// 		MaxConnections:  10000,
// 		RateLimit:       rate.Limit(100), // 100 conn/sec
// 		RateBurst:       200,
// 		ReadTimeout:     15 * time.Second,
// 		WriteTimeout:    15 * time.Second,
// 		IdleTimeout:     60 * time.Second,
// 		ShutdownTimeout: 30 * time.Second,
// 	}
// }

func NewServer(serverConfig ServerConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		rooms:          make(map[string]*Room),
		factories:      make(map[string]func() *Room),
		maxConnections: serverConfig.MaxConnections,
		connLimiter:    rate.NewLimiter(rate.Limit(100), 200),
		ctx:            ctx,
		cancel:         cancel,
		startTime:      time.Now(),
	}
}

func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if s.isShuttingDown.Load() {
		http.Error(w, "Server is shutting down", http.StatusServiceUnavailable)
		return
	}

	if s.activeConnections.Load() >= s.maxConnections {
		http.Error(w, "Server out of capacity", http.StatusServiceUnavailable)
		return
	}

	if !s.connLimiter.Allow() {
		http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	s.activeConnections.Add(1)
	s.totalConnections.Add(1)

	sessionID := utils.RandomUUID()

	client := NewClientWithContext(s.ctx, sessionID, conn)
	log.Printf("New connection: %s (total: %d)", sessionID, s.activeConnections.Load())

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.activeConnections.Add(-1)
		defer s.totalDisconnects.Add(1)
		defer client.Close()

		client.WritePump()
	}()

	s.wg.Add(1)
	defer func() {
		defer s.wg.Done()

		go client.ReadPump()
	}()
}

func (s *Server) Shutdown(ctx context.Context) error {
	log.Println("Shutting down server...")

	s.isShuttingDown.Store(true)
	s.cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("HTTP server shutdown: %w", err)
	}

	s.mu.RLock()
	rooms := make([]*Room, 0, len(s.rooms))
	for _, room := range s.rooms {
		rooms = append(rooms, room)
	}
	s.mu.RUnlock()

	for _, room := range rooms {
		room.Destroy()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("All connections closed gracefully")
	case <-ctx.Done():
		log.Println("Shutdown timeout - forcing close")
		return ctx.Err()
	}

	return nil
}

func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	roomCount := len(s.rooms)
	maxConnections := s.maxConnections
	s.mu.RUnlock()

	activeConnections := s.activeConnections.Load()
	isShuttingDown := s.isShuttingDown.Load()

	status := "healthy"
	if isShuttingDown {
		status = "shutting_down"
	} else if activeConnections >= maxConnections {
		status = "at_capacity"
	} else if float64(activeConnections) > float64(s.maxConnections)*0.9 {
		status = "degraded"
	}

	health := map[string]any{
		"status":             status,
		"active_connections": activeConnections,
		"total_rooms":        roomCount,
		"uptime":             time.Since(s.startTime).String(),
	}

	statusCode := http.StatusOK
	switch status {
	case "shutting_down":
		statusCode = http.StatusServiceUnavailable
	case "at_capacity":
		statusCode = http.StatusServiceUnavailable
	case "degraded":
		statusCode = http.StatusOK
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(health); err != nil {
		log.Printf("Error encoding health response: %v", err)
	}
	w.WriteHeader(statusCode)
}

// TODO
func (s *Server) JoinRoomById(w http.ResponseWriter, r *http.Request) {
	// roomId := r.PathValue("roomId")
	// _room, exists := s.GetRoomByID(roomId)
	// if !exists {
	// 	w.WriteHeader(http.StatusNotFound)
	// 	w.Header().Set("Content-Type", "application/json")
	// 	if err := json.NewEncoder(w).Encode(map[string]any{}); err != nil {
	// 		log.Printf("Error encoding health response: %v", err)
	// 	}
	// 	return
	// }
	//
	// TODO
	// room.AddClient() // client object
}

func (s *Server) Listen(addr string, config ServerConfig) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.HandleWebSocket)
	mux.HandleFunc("GET /health", s.HandleHealth)
	mux.HandleFunc("GET /join-room-by-roomId/{roomId}", s.JoinRoomById)

	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
		IdleTimeout:  config.IdleTimeout,
	}

	log.Printf("Server listening on %s", addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) DefineRoom(name string, factory func() *Room) {
	s.mu.Lock()
	s.factories[name] = factory
	s.mu.Unlock()
}

func (s *Server) CreateRoom(roomType string) (*Room, error) {
	if roomType == "" {
		return nil, fmt.Errorf("roomType cannot be empty")
	}
	s.mu.Lock()
	factory, exists := s.factories[roomType]
	if !exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("no factory defined for room type: %s", roomType)
	}

	room := factory()
	roomDefaultId := fmt.Sprintf("%v#%v", roomType, utils.RandomUUID())
	room.SetId(roomDefaultId)
	s.rooms[roomDefaultId] = room
	s.mu.Unlock()

	log.Printf("Room created: %s", roomType)
	go room.Run()

	return room, nil
}

func (s *Server) RemoveRoom(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	room, exists := s.rooms[name]
	if exists {
		if err := room.Destroy(); err != nil {
			fmt.Printf("%v\n", err.Error())
		} else {
			delete(s.rooms, name)
			log.Printf("Room removed: %s", name)
		}
	}
}

func (s *Server) GetRoomByID(roomId string) (*Room, bool) {
	s.mu.RLock()
	room, exists := s.rooms[roomId]
	s.mu.RUnlock()

	if !exists {
		return nil, false
	}
	return room, true
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohhcgan/lattice/types"
)

func main() {
	var (
		addr            = flag.String("addr", ":8080", "Server address to listen on")
		maxConnections  = flag.Int("max-connections", 10000, "Maximum concurrent connections")
		readTimeout     = flag.Duration("read-timeout", 15*time.Second, "HTTP read timeout")
		writeTimeout    = flag.Duration("write-timeout", 15*time.Second, "HTTP write timeout")
		idleTimeout     = flag.Duration("idle-timeout", 60*time.Second, "HTTP idle timeout")
		shutdownTimeout = flag.Duration("shutdown-timeout", 30*time.Second, "Graceful shutdown timeout")
		rateLimit       = flag.Float64("rate-limit", 100, "Connection rate limit (per second)")
		rateBurst       = flag.Int("rate-burst", 200, "Connection rate burst size")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("Starting Lattice WebSocket Server...")

	config := types.ServerConfig{
		MaxConnections: int32(*maxConnections),
		// RateLimit:       *rateLimit,
		// RateBurst:       *rateBurst,
		ReadTimeout:     *readTimeout,
		WriteTimeout:    *writeTimeout,
		IdleTimeout:     *idleTimeout,
		ShutdownTimeout: *shutdownTimeout,
	}
	server := types.NewServer(config)

	registerRoomFactories(server)
	room, err := server.CreateRoom("lobby")
	fmt.Println("Room", room)
	fmt.Println("Err", err)

	// Setup graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Server configuration:")
		log.Printf("  - Address: %s", *addr)
		log.Printf("  - Max Connections: %d", *maxConnections)
		log.Printf("  - Rate Limit: %.0f conn/sec (burst: %d)", *rateLimit, *rateBurst)
		log.Printf("  - Timeouts: read=%v, write=%v, idle=%v", *readTimeout, *writeTimeout, *idleTimeout)
		log.Printf("Server ready to accept connections")

		serverErrors <- server.Listen(*addr, config)
	}()

	// Wait for shutdown signal or server error
	select {
	case err := <-serverErrors:
		log.Fatalf("Server error: %v", err)

	case sig := <-shutdown:
		log.Printf("Received shutdown signal: %v", sig)

		// Create shutdown context with timeout
		ctx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
		defer cancel()

		// Attempt graceful shutdown
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown failed: %v", err)
			log.Printf("Forcing shutdown...")
			os.Exit(1)
		}

		log.Printf("Server stopped gracefully")
	}
}

func registerRoomFactories(server *types.Server) {
	server.DefineRoom("lobby", func() *types.Room {
		room := types.New()

		room.OnMessage("message", func(client *types.Client, msg types.Message) {
			log.Printf("Chat room - Client %s sent: %v", client.SessionID, msg.Type)
			room.Broadcast("message", []string{client.SessionID}, msg.Data)
		})

		room.OnJoin = func(client *types.Client, payload any) {
			log.Printf("Client %s joined chat room", client.SessionID)
		}

		room.OnLeave = func(client *types.Client) {
			log.Printf("Client %s left chat room", client.SessionID)
		}

		return room
	})
}

func init() {
	os.Setenv("TZ", "UTC")

	if os.Getenv("ENV") == "production" {
		log.SetFlags(log.LstdFlags)
	} else {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}
}

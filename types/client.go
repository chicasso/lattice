package types

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"golang.org/x/time/rate"
)

type ClientStatus int32

const (
	maxQueueSize   = 256              /* Maximum number of queued messages before blocking */
	maxMessageSize = 1024 * 1024      /* Maximum message size allowed from peer (1MB) */
	writeWait      = 10 * time.Second /* Time allowed to write a message to the peer */
	pongWait       = 60 * time.Second /* Time allowed to read the next pong message from the peer */
	pingPeriod     = 54 * time.Second /* Send pings to peer with this period (must be less than pongWait) */
	SendTimeout    = 3 * time.Second  /* Time before client message should be sent to the channel */

	maxMessagesPerSecond = 10 /* Maximum messages per second from client */
	maxBurstSize         = 20 /* Maximum burst size for rate limiter */
)

const (
	JOINING ClientStatus = iota
	READY
	CLOSING
	CLOSED
)

func (s ClientStatus) String() string {
	switch s {
	case JOINING:
		return "JOINING"
	case READY:
		return "READY"
	case CLOSING:
		return "CLOSING"
	case CLOSED:
		return "CLOSED"
	default:
		return "UNKNOWN"
	}
}

type Client struct {
	//
	// Client - Room related fields
	//
	SessionID  string
	Connection net.Conn

	//
	// Client status
	//
	status atomic.Int32

	//
	// Private
	//
	metadata map[string]any

	//
	// Chan
	//
	sendRaw chan []byte
	send    chan []byte

	//
	// Room
	//
	Room *Room

	//
	// Concurrency & Context
	//
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once

	//
	// Rate limiting
	//
	rateLimiter *rate.Limiter

	//
	// Client metrics
	//
	messagesSent     atomic.Uint64
	messagesReceived atomic.Uint64
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64
	connectedAt      time.Time
	lastMessageAt    atomic.Int64
}

func NewClient(sessionID string, conn net.Conn) *Client {
	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())

	client := &Client{
		SessionID:   sessionID,
		Connection:  conn,
		metadata:    make(map[string]any),
		sendRaw:     make(chan []byte, maxQueueSize),
		send:        make(chan []byte, maxQueueSize),
		Room:        nil,
		mu:          sync.RWMutex{},
		ctx:         ctx,
		cancel:      cancel,
		closeOnce:   sync.Once{},
		rateLimiter: rate.NewLimiter(maxMessagesPerSecond, maxBurstSize),
		connectedAt: now,
	}

	client.status.Store(int32(JOINING))
	client.lastMessageAt.Store(now.Unix())
	return client
}

func NewClientWithContext(ctx context.Context, sessionID string, conn net.Conn) *Client {
	ctx, cancel := context.WithCancel(ctx)
	client := NewClient(sessionID, conn)
	client.cancel = cancel
	client.ctx = ctx

	return client
}

func (c *Client) GetStatus() ClientStatus {
	return ClientStatus(c.status.Load())
}

func (c *Client) SetStatus(newStatus ClientStatus) {
	c.status.Store(int32(newStatus))
}

func (c *Client) Send(topic string, data any) error {
	msgBuff, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("unable to marshal payload: %w", err)
	}

	msg := Message{Type: topic, Data: msgBuff}
	buff, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("unable to marshal message: %#v, %w", msg, err)
	}

	select {
	case c.send <- buff:
		return nil
	case <-c.ctx.Done():
		return fmt.Errorf("client connection closed")
	case <-time.After(SendTimeout):
		return fmt.Errorf("send timeout - client may be slow or disconnected")
	}
}

func (c *Client) SendRaw(topic string, msgBuff []byte) error {
	msg := Message{Type: topic, Data: msgBuff}
	buff, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("unable to marshal message: %#v, %w", msg, err)
	}

	select {
	case c.sendRaw <- buff:
		return nil
	case <-c.ctx.Done():
		return fmt.Errorf("client connection closed")
	case <-time.After(SendTimeout):
		return fmt.Errorf("send timeout - client may be slow or disconnected")
	}
}

func (c *Client) ReadPump() {
	defer c.Close()

	var err error
	var header ws.Header
	var state ws.State = ws.StateServerSide
	var limitedReader *io.LimitedReader = &io.LimitedReader{R: c.Connection, N: maxMessageSize}

	c.Connection.SetReadDeadline(time.Now().Add(pongWait))

	for {
		now := time.Now()

		select {
		case <-c.ctx.Done():
			return
		default:
		}

		header, err = ws.ReadHeader(limitedReader)
		if err != nil {
			if err != io.EOF {
				log.Printf("ws read header error: %v", err)
			}
			break
		}

		if err := ws.CheckHeader(header, state); err != nil {
			log.Printf("invalid header: %v", err)
			c.Send(Error, ErrUnprocessableMessage)
			break
		}

		if header.Length > maxMessageSize {
			log.Printf("message too large: %d bytes", header.Length)
			c.Send(Error, ErrMessageTooLarge)
			break
		}

		payload := make([]byte, header.Length)
		if _, err := io.ReadFull(limitedReader, payload); err != nil {
			log.Printf("error reading payload: %v", err)
			c.Send(Error, ErrUnexpected)
			break
		}

		if header.Masked {
			ws.Cipher(payload, header.Mask, 0)
		}

		c.bytesReceived.Add(uint64(header.Length))
		c.lastMessageAt.Store(now.Unix())

		c.Connection.SetReadDeadline(now.Add(pongWait))

		switch header.OpCode {
		case ws.OpText, ws.OpBinary:
			if !c.rateLimiter.Allow() {
				log.Printf("rate limit exceeded for client %s", c.SessionID)
				continue
			}
			var message Message
			if err := json.Unmarshal(payload, &message); err != nil {
				log.Printf("error unmarshaling message: %v", err)
				continue
			}

			c.messagesReceived.Add(1)

			if c.Room != nil {
				c.Room.SendMessage <- message
			}

		case ws.OpClose:
			code, reason := ws.ParseCloseFrameData(payload)
			log.Printf("Client %s closing: code=%d reason=%s", c.SessionID, code, reason)
			return

		case ws.OpPing:
			if err := c.writeFrame(ws.OpPong, payload); err != nil {
				log.Printf("error sending pong: %v", err)
				return
			}

		case ws.OpPong:
			//
			// Connection is still alive
			//
		case ws.OpContinuation:
			//
			// TODO
			//
		}
	}
}

func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Connection.Close()
	}()

	for {
		select {
		case msg, ok := <-c.sendRaw:
			{
				if !ok {
					c.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
					return
				}

				if err := c.writeMessage(msg); err != nil {
					log.Printf("error writing message: %v", err)
					return
				}
			}

		case msg, ok := <-c.send:
			{
				if !ok {
					c.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
					return
				}

				if err := c.writeMessage(msg); err != nil {
					log.Printf("error writing message: %v", err)
					return
				}
			}

		case <-ticker.C:
			if err := c.writeFrame(ws.OpPing, nil); err != nil {
				log.Printf("error sending ping: %v", err)
				return
			}

		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Client) writeMessage(data []byte) error {
	c.Connection.SetWriteDeadline(time.Now().Add(writeWait))

	if err := wsutil.WriteServerMessage(c.Connection, ws.OpText, data); err != nil {
		return err
	}
	c.bytesSent.Add(uint64(len(data)))
	c.messagesSent.Add(1)
	return nil
}

func (c *Client) writeFrame(op ws.OpCode, payload []byte) error {
	c.Connection.SetWriteDeadline(time.Now().Add(writeWait))
	return wsutil.WriteServerMessage(c.Connection, op, payload)
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.SetStatus(CLOSING)
		c.cancel()

		defer func() {
			close(c.send)
			close(c.sendRaw)
			c.Connection.Close()

			c.SetStatus(CLOSED)
		}()

		if c.Room != nil {
			c.Room.RemoveClient(c)
		}

		c.Connection.SetWriteDeadline(time.Now().Add(1 * time.Second))
		c.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
	})
}

func (c *Client) SetMetadata(key string, value any) {
	c.mu.Lock()
	c.metadata[key] = value
	c.mu.Unlock()
}

func (c *Client) GetMetadata(key string) (val any, ok bool) {
	c.mu.RLock()
	val, ok = c.metadata[key]
	c.mu.RUnlock()

	return
}

func (c *Client) GetAllMetadata() (copy map[string]any) {
	c.mu.RLock()
	copy = make(map[string]any)
	maps.Copy(copy, c.metadata)
	c.mu.RUnlock()

	return copy
}

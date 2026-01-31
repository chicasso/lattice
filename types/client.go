package types

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

const (
	maxQueueSize   = 256              /* Maximum number of queued messages before blocking */
	maxMessageSize = 1024 * 1024      /* Maximum message size allowed from peer (1MB) */
	writeWait      = 10 * time.Second /* Time allowed to write a message to the peer */
	pongWait       = 60 * time.Second /* Time allowed to read the next pong message from the peer */
	pingPeriod     = 54 * time.Second /* Send pings to peer with this period (must be less than pongWait) */
	SendTimeout    = 3 * time.Second  /* Time before client message should be sent to the channel */
)

type Client struct {
	//
	// Client - Room related fields
	//
	SessionID  string
	Connection net.Conn
	SendRaw    chan []byte
	Send       chan any
	Room       *Room
	mu         sync.RWMutex
	metadata   map[string]any
	done       chan struct{}
	closeOnce  sync.Once

	//
	// Client metrics
	//
	messagesSent     atomic.Uint64
	messagesReceived atomic.Uint64
	bytesReceived    atomic.Uint64
	bytesSent        atomic.Uint64
	connectedAt      time.Time
	lastMessageAt    time.Time
}

type ClientStats struct {
	MessagesSent     uint64
	MessagesReceived uint64
	BytesSent        uint64
	BytesReceived    uint64
	ConnectedAt      time.Time
	LastMessageAt    time.Time
	Uptime           time.Duration
}

func NewClient(sessionID string, conn net.Conn) *Client {
	now := time.Now()
	return &Client{
		SessionID:     sessionID,
		Connection:    conn,
		SendRaw:       make(chan []byte, maxQueueSize),
		Send:          make(chan any, maxQueueSize),
		Room:          nil,
		metadata:      make(map[string]any),
		done:          make(chan struct{}),
		connectedAt:   now,
		lastMessageAt: now,
	}
}

func (c *Client) SendMessage(topic string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("unable to marshal payload: %w", err)
	}

	msg := Message{Type: topic, Data: payload}
	buff, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("unable to marshal message: %w", err)
	}

	select {
	case c.SendRaw <- buff:
		return nil
	case <-c.done:
		return fmt.Errorf("client connection closed")
	case <-time.After(SendTimeout):
		return fmt.Errorf("send timeout - client may be slow or disconnected")
	}
}

func (c *Client) ReadPump(handleMessage func(*Client, Message) error) {
	defer func() { c.Close() }()

	var (
		err           error
		header        ws.Header
		state         ws.State          = ws.StateServerSide
		limitedReader *io.LimitedReader = &io.LimitedReader{R: c.Connection, N: maxMessageSize}
	)

	now := time.Now()
	c.Connection.SetReadDeadline(now.Add(pongWait))

	for {
		limitedReader.N = maxMessageSize

		header, err = ws.ReadHeader(limitedReader)
		if err != nil {
			if err != io.EOF && !isClosedError(err) {
				log.Printf("ws read header error: %v", err)
			}
			break
		}

		if header.Length > maxMessageSize {
			log.Printf("message too large: %d bytes", header.Length)
			c.SendMessage(Error, ErrMessageTooLarge)
			break
		}

		if err := ws.CheckHeader(header, state); err != nil {
			log.Printf("invalid header: %v", err)
			c.SendMessage(Error, ErrUnprocessableMessage)
			break
		}

		payload := make([]byte, header.Length)
		if _, err := io.ReadFull(limitedReader, payload); err != nil {
			log.Printf("error reading payload: %v", err)
			c.SendMessage(Error, ErrUnexpected)
			break
		}

		if header.Masked {
			ws.Cipher(payload, header.Mask, 0)
		}

		c.bytesReceived.Add(uint64(header.Length))
		c.lastMessageAt = now

		c.Connection.SetReadDeadline(now.Add(pongWait))

		switch header.OpCode {
		case ws.OpText, ws.OpBinary:
			var message Message
			if err := json.Unmarshal(payload, &message); err != nil {
				log.Printf("error unmarshaling message: %v", err)
				continue
			}

			c.messagesReceived.Add(1)

			if err := handleMessage(c, message); err != nil {
				log.Printf("error handling message: %v", err)
				c.SendMessage(Error, ErrUnexpected)
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
		case message, ok := <-c.SendRaw:
			{
				if !ok {
					c.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
					return
				}

				if err := c.writeMessage(message); err != nil {
					log.Printf("error writing message: %v", err)
					return
				}
			}

		case message, ok := <-c.Send:
			{
				if !ok {
					c.writeFrame(ws.OpClose, ws.NewCloseFrameBody(ws.StatusNormalClosure, ""))
					return
				}

				buffer, err := json.Marshal(message)
				if err != nil {
					log.Printf("error marshaling message: %v", err)
					continue
				}

				if err := c.writeMessage(buffer); err != nil {
					log.Printf("error writing message: %v", err)
					return
				}
			}

		case <-ticker.C:
			if err := c.writeFrame(ws.OpPing, nil); err != nil {
				log.Printf("error sending ping: %v", err)
				return
			}

		case <-c.done:
			return
		}
	}
}

// TODO: which message topic is this message part of?
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
		defer func() {
			close(c.done)
			close(c.Send)
			close(c.SendRaw)
			c.Connection.Close()
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
	defer c.mu.Unlock()

	c.metadata[key] = value
}

func (c *Client) GetMetadata(key string) (val any, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	val, ok = c.metadata[key]
	return
}

func (c *Client) GetAllMetadata() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()

	copy := make(map[string]any)
	for k, v := range c.metadata {
		copy[k] = v
	}
	return copy
}

func (c *Client) GetStats() ClientStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return ClientStats{
		MessagesSent:     c.messagesSent.Load(),
		MessagesReceived: c.messagesReceived.Load(),
		BytesSent:        c.bytesSent.Load(),
		BytesReceived:    c.bytesReceived.Load(),
		ConnectedAt:      c.connectedAt,
		LastMessageAt:    c.lastMessageAt,
		Uptime:           time.Since(c.connectedAt),
	}
}

func isClosedError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	return errStr == "EOF" ||
		errStr == "use of closed network connection" ||
		errStr == "broken pipe"
}

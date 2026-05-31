package utils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type rTypes interface {
	int | string | []byte | bool
}

type RParam struct {
	key   string
	value string
	Type  string
}

type RClient struct {
	rdb *redis.Client
	ctx context.Context
}

func (r *RClient) get(key string) *redis.StringCmd {
	return r.rdb.Get(r.ctx, key)
}

func (r *RClient) set(key string, value any, exp time.Duration) *redis.StatusCmd {
	return r.rdb.Set(r.ctx, key, value, exp)
}

func (r *RClient) publish(topic string, msg any) *redis.IntCmd {
	return r.rdb.Publish(r.ctx, topic, msg)
}

func (r *RClient) Subscribe(topic string, cb func(string)) {
	pubSub := r.rdb.Subscribe(r.ctx, topic)
	if _, err := pubSub.Receive(r.ctx); err != nil {
		return
	}

	channel := pubSub.Channel()

	go func() {
		defer pubSub.Close()

		for {
			select {
			case msg, ok := <-channel:
				if !ok {
					return
				}
				cb(msg.Payload)

			case <-r.ctx.Done():
				return
			}
		}
	}()
}

// New creates a Redis client from a Redis URL (e.g. "redis://user:pass@localhost:6379/0").
func New(url string) (RClient, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return RClient{}, fmt.Errorf("invalid redis URL: %w", err)
	}

	opt.ClientName = "lattice"
	opt.OnConnect = func(ctx context.Context, cn *redis.Conn) error {
		fmt.Println("redis connection established")
		return nil
	}
	opt.MaxRetries = 3
	opt.MinRetryBackoff = 100 * time.Millisecond
	opt.MaxRetryBackoff = 1 * time.Second
	opt.ReadTimeout = 10 * time.Second
	opt.WriteTimeout = 10 * time.Second
	opt.MinIdleConns = 1
	opt.MaxIdleConns = 10
	opt.ConnMaxIdleTime = 30 * time.Minute

	return RClient{
		rdb: redis.NewClient(opt),
		ctx: context.Background(),
	}, nil
}

func Get[T rTypes](r *RClient, key string) (T, error) {
	var instance T

	resp := r.get(key)
	if err := resp.Err(); err != nil {
		return any("").(T), err
	}

	var val any
	var err error

	switch any(instance).(type) {
	case int:
		val, err = resp.Int()
	case string:
		val = resp.String()
	case []byte:
		val, err = resp.Bytes()
	case bool:
		val, err = resp.Bool()
	default:
		val = nil
		err = errors.New("invalid type")
	}
	return val.(T), err
}

func Set[T rTypes](r *RClient, key string, value T, exp time.Duration) error {
	resp := r.set(key, value, exp)
	if err := resp.Err(); err != nil {
		return err
	}
	return nil
}

func Publish(r *RClient, key string, msg any) error {
	resp := r.publish(key, msg)
	if err := resp.Err(); err != nil {
		return err
	}
	return nil
}

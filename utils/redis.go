package utils

import (
	"context"
	"errors"
	"fmt"
	"time"

	"crypto/tls"

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

func (redis *RClient) get(key string) *redis.StringCmd {
	return redis.rdb.Get(redis.ctx, key)
}

func (redis *RClient) set(key string, value any, exp time.Duration) *redis.StatusCmd {
	return redis.rdb.Set(redis.ctx, key, value, exp)
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

func New(_url string) RClient {
	//
	// opt, err := redis.ParseURL(url)
	// if err != nil {
	// 	panic(err)
	// }
	//
	return RClient{
		rdb: redis.NewClient(
			&redis.Options{
				Network:    "tcp",
				Addr:       "localhost:6379",
				ClientName: "lattice",
				OnConnect: func(ctx context.Context, cn *redis.Conn) error {
					fmt.Println("redis connection established")
					return nil
				},
				Username:        "",
				Password:        "",
				MaxRetries:      3,
				MinRetryBackoff: -1, // sec
				MaxRetryBackoff: -1, // sec
				ReadTimeout:     10, // sec
				WriteTimeout:    10, // sec
				MinIdleConns:    1,
				MaxIdleConns:    10,
				MaxActiveConns:  0,
				ConnMaxIdleTime: 30, // min
				ConnMaxLifetime: 0,
				TLSConfig:       &tls.Config{},
			},
		),
		ctx: context.Background(),
	}
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

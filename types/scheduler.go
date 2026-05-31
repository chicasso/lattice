package types

import (
	"errors"
	"sync/atomic"
	"time"
)

const (
	TIMEOUT  = 1
	INTERVAL = 2
)

type Timer interface {
	GetTimeElapsed() time.Duration
	// Unpause() error
	// Pause() error
	Execute() error
	// Reset() error
	// Clear() error
}

type Timed struct {
	callbackFunc func()
	paused       atomic.Bool
	active       atomic.Bool
	timedType    uint
	duration     uint
	invokedTime  time.Time
	// expiryTime   time.Time // TODO
	ticker *time.Ticker
	timer  *time.Timer
}

func NewTimed(dur, timedType uint, cb func()) *Timed {
	if timedType != INTERVAL && timedType != TIMEOUT {
		panic("Invalid timedType")
	}

	timed := &Timed{
		duration:     dur,
		timedType:    timedType,
		invokedTime:  time.Now(),
		callbackFunc: cb,
	}

	switch timedType {
	case INTERVAL:
		timed.ticker = time.NewTicker(time.Duration(dur) * time.Millisecond)
	case TIMEOUT:
		timed.timer = time.NewTimer(time.Duration(dur) * time.Millisecond)
	}

	timed.active.Store(true)
	timed.paused.Store(false)

	return timed
}

func (t *Timed) GetTimeElapsed() time.Duration {
	if !t.active.Load() {
		// TODO
	}
	return time.Since(t.invokedTime)
}

func (t *Timed) Pause() error {
	if !t.active.Load() {
		return errors.New("Timer / Ticker is not active anymore")
	}
	t.paused.Store(true)

	switch t.timedType {
	case INTERVAL:
		t.ticker.Stop()
	case TIMEOUT:
		t.timer.Stop()
	}

	return nil
}

func (t *Timed) Unpause() error {
	if !t.active.Load() {
		return errors.New("Timer / Ticker is not active anymore")
	}
	return nil
}

func (t *Timed) Execute() error {
	if !t.active.Load() {
		return errors.New("Timer / Ticker is not active anymore")
	}

	if t.timedType != TIMEOUT {
		return errors.New("Cannot execute non-timeout timed objects")
	}
	stopped := t.timer.Stop()
	if !stopped {
		return errors.New("Timer already in stopped state")
	}

	t.active.Store(false)
	t.callbackFunc()

	return nil
}

func (t *Timed) Reset() {
}

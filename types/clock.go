package types

import (
	"context"
)

type Scheduled interface {
	ScheduleInterval(context.Context, func())
	ScheduleTimeout(context.Context, func())
}

type Clock struct{}

func (c *Clock) ScheduleInterval(ctx context.Context, cb func(), dur uint) Timer {
	timed := NewTimed(dur, INTERVAL, cb)

	go func() {
		defer timed.ticker.Stop()
		for {
			select {
			case <-timed.ticker.C:
				timed.callbackFunc()
			case <-ctx.Done():
				return
			}
		}
	}()

	return timed
}

func (c *Clock) ScheduleTimeout(ctx context.Context, cb func(), dur uint) Timer {
	timed := NewTimed(dur, TIMEOUT, cb)

	go func() {
		defer timed.timer.Stop()
		for {
			select {
			case <-timed.timer.C:
				timed.callbackFunc()
			case <-ctx.Done():
				return
			}
		}
	}()

	return timed
}

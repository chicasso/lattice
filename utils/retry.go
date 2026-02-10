package utils

import (
	"fmt"
	"time"
)

func Retry(cb func(...any) any, maxTries, backOffMs int, params ...any) (any, error) {
	var lastErr error = nil
	for i := 0; i < maxTries; i += 1 {
		resp := cb(params...)

		if err, ok := resp.(error); ok {
			lastErr = err
			if backOffMs > 0 {
				time.Sleep(time.Duration(backOffMs) * time.Millisecond)
			}
			continue
		}
		return resp, nil

	}
	if lastErr == nil {
		lastErr = fmt.Errorf("retry failed after %d attempts", maxTries)
	}
	return nil, lastErr
}

func Safe(cb func(...any) any, params ...any) (resp any, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic recovered: %v", p)
		}
	}()

	resp = cb(params...)
	return resp, nil
}

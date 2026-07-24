// Package retry implements context-aware exponential backoff with full
// jitter, used for transient failures against Postgres, Redis, and MinIO.
package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

// Policy configures backoff behavior.
type Policy struct {
	// MaxAttempts includes the first try. Must be >= 1.
	MaxAttempts int
	// BaseDelay is the cap for the first backoff window.
	BaseDelay time.Duration
	// MaxDelay caps the exponential growth.
	MaxDelay time.Duration
	// RetryIf decides whether an error is transient. nil retries everything.
	RetryIf func(error) bool
}

// DefaultPolicy suits infrastructure calls: 4 attempts, 100ms base, 2s cap.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: 2 * time.Second}
}

// Do runs fn under the policy. It returns nil on the first success, the last
// error once attempts are exhausted or fn returns a non-retryable error, and
// ctx.Err() joined with the last error if the context ends mid-backoff.
//
// Full jitter (sleep uniformly in [0, window)) avoids thundering-herd
// synchronization when many replicas retry the same downstream outage.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < p.MaxAttempts; attempt++ {
		if attempt > 0 {
			window := p.BaseDelay << (attempt - 1)
			if window > p.MaxDelay || window <= 0 {
				window = p.MaxDelay
			}
			sleep := time.Duration(rand.Int63n(int64(window) + 1)) //nolint:gosec // jitter, not secret
			select {
			case <-ctx.Done():
				return errors.Join(ctx.Err(), lastErr)
			case <-time.After(sleep):
			}
		}
		if err := fn(ctx); err != nil {
			lastErr = err
			if p.RetryIf != nil && !p.RetryIf(err) {
				return err
			}
			continue
		}
		return nil
	}
	return lastErr
}

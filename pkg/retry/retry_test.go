package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastPolicy() Policy {
	return Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond}
}

func TestDoSucceedsAfterTransientFailures(t *testing.T) {
	attempts := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, attempts)
}

func TestDoExhaustsAttempts(t *testing.T) {
	boom := errors.New("boom")
	attempts := 0
	err := Do(context.Background(), fastPolicy(), func(context.Context) error {
		attempts++
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 4, attempts)
}

func TestDoStopsOnNonRetryable(t *testing.T) {
	fatal := errors.New("fatal")
	p := fastPolicy()
	p.RetryIf = func(err error) bool { return !errors.Is(err, fatal) }

	attempts := 0
	err := Do(context.Background(), p, func(context.Context) error {
		attempts++
		return fatal
	})
	assert.ErrorIs(t, err, fatal)
	assert.Equal(t, 1, attempts, "non-retryable errors must not be retried")
}

func TestDoHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxAttempts: 10, BaseDelay: time.Hour, MaxDelay: time.Hour}

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, p, func(context.Context) error { return errors.New("always") })
	assert.ErrorIs(t, err, context.Canceled)
}

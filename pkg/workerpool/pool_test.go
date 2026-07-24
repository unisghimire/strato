package workerpool

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolExecutesAllTasks(t *testing.T) {
	p := New(4, 16)
	var count atomic.Int64
	for i := 0; i < 100; i++ {
		require.NoError(t, p.Submit(context.Background(), func(context.Context) {
			count.Add(1)
		}))
	}
	require.NoError(t, p.Shutdown(context.Background()))
	assert.Equal(t, int64(100), count.Load())
}

func TestSubmitAfterShutdownFails(t *testing.T) {
	p := New(1, 0)
	require.NoError(t, p.Shutdown(context.Background()))
	err := p.Submit(context.Background(), func(context.Context) {})
	assert.ErrorIs(t, err, ErrClosed)
}

func TestShutdownTimeoutCancelsTasks(t *testing.T) {
	p := New(1, 0)
	started := make(chan struct{})
	canceled := make(chan struct{})

	require.NoError(t, p.Submit(context.Background(), func(ctx context.Context) {
		close(started)
		<-ctx.Done() // block until the pool context is canceled
		close(canceled)
	}))
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.Shutdown(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("task was not canceled on shutdown timeout")
	}
}

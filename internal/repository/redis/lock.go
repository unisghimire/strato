package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/unisghimire/strato/internal/domain"
	"github.com/unisghimire/strato/pkg/crypto"
)

// Lock implements domain.DistributedLock with SET NX + owner-checked
// release — the standard single-node Redis lock. The TTL bounds the damage
// of a crashed holder; release verifies ownership so an expired holder
// cannot delete a lock someone else has since acquired.
type Lock struct {
	client *redis.Client
}

// NewLock constructs a Lock.
func NewLock(client *redis.Client) *Lock { return &Lock{client: client} }

var _ domain.DistributedLock = (*Lock)(nil)

var releaseScript = redis.NewScript(`
	if redis.call('GET', KEYS[1]) == ARGV[1] then
		return redis.call('DEL', KEYS[1])
	end
	return 0
`)

// TryLock attempts to acquire the named lock for ttl.
func (l *Lock) TryLock(ctx context.Context, key string, ttl time.Duration) (func(), bool, error) {
	token, err := crypto.RandomToken(16)
	if err != nil {
		return nil, false, err
	}
	fullKey := "lock:" + key
	ok, err := l.client.SetNX(ctx, fullKey, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	release := func() {
		// Release must succeed even if the acquiring request's context is
		// done (e.g. client disconnected mid-upload) — use a fresh timeout.
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = releaseScript.Run(rctx, l.client, []string{fullKey}, token).Err()
	}
	return release, true, nil
}

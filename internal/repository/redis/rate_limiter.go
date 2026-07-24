package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/unisghimire/strato/internal/domain"
)

// RateLimiter implements domain.RateLimiter with a sliding-window log in a
// Redis sorted set. Compared to a fixed window it has no boundary bursts
// (2× the limit straddling a window edge); the ZSET holds one member per
// request, scored and trimmed by timestamp.
type RateLimiter struct {
	client *redis.Client
}

// NewRateLimiter constructs a RateLimiter.
func NewRateLimiter(client *redis.Client) *RateLimiter {
	return &RateLimiter{client: client}
}

var _ domain.RateLimiter = (*RateLimiter)(nil)

// slidingWindow is executed atomically server-side: trim expired entries,
// count, and only record the new request if under the limit.
var slidingWindow = redis.NewScript(`
	local key = KEYS[1]
	local now = tonumber(ARGV[1])
	local window = tonumber(ARGV[2])
	local limit = tonumber(ARGV[3])
	local member = ARGV[4]

	redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
	local count = redis.call('ZCARD', key)
	if count >= limit then
		return 0
	end
	redis.call('ZADD', key, now, member)
	redis.call('PEXPIRE', key, window)
	return 1
`)

// Allow reports whether the caller identified by key may proceed.
// Fail-open on Redis outage is the deliberate trade-off: degraded protection
// beats a hard dependency of every request on Redis. The error is still
// returned so middleware can log and count it.
func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	now := time.Now().UnixMilli()
	member := fmt.Sprintf("%d-%s", now, uuid.NewString()[:8])
	res, err := slidingWindow.Run(ctx, r.client,
		[]string{"ratelimit:" + key}, now, window.Milliseconds(), limit, member).Int()
	if err != nil {
		return true, fmt.Errorf("rate limiter unavailable: %w", err)
	}
	return res == 1, nil
}

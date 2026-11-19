package app

import (
	"sync"
	"time"
)

// Anonymous SAML routes write a request-state row or run a signature verification per call, so they are
// rate limited per peer address (ADR 0018 section 1: browser routes are public and rate limited). The budget
// is generous because a reverse proxy makes every browser share one peer address.
const (
	samlRateBurst  = 120
	samlRateWindow = time.Minute
	// samlRateMaxClients bounds the limiter's memory: past it, idle entries are dropped first.
	samlRateMaxClients = 10000
)

// samlRateLimiter is a per-key token bucket that refills samlRateBurst tokens per samlRateWindow.
type samlRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]samlBucket
}

type samlBucket struct {
	tokens float64
	seen   time.Time
}

// allow spends one token of key at now and reports whether one was available.
func (l *samlRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]samlBucket{}
	}
	if len(l.buckets) >= samlRateMaxClients {
		for client, bucket := range l.buckets {
			if now.Sub(bucket.seen) >= samlRateWindow {
				delete(l.buckets, client)
			}
		}
		if len(l.buckets) >= samlRateMaxClients {
			// Every tracked client is active: refuse new ones rather than grow without bound.
			if _, tracked := l.buckets[key]; !tracked {
				return false
			}
		}
	}
	bucket, ok := l.buckets[key]
	if !ok {
		bucket = samlBucket{tokens: samlRateBurst, seen: now}
	}
	if elapsed := now.Sub(bucket.seen); elapsed > 0 {
		bucket.tokens = min(samlRateBurst, bucket.tokens+elapsed.Seconds()*samlRateBurst/samlRateWindow.Seconds())
	}
	bucket.seen = now
	allowed := bucket.tokens >= 1
	if allowed {
		bucket.tokens--
	}
	l.buckets[key] = bucket
	return allowed
}

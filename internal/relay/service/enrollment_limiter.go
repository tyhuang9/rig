package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"net"
	"sync"
	"time"
)

const (
	globalEnrollmentBurst  = 32
	prefixEnrollmentBurst  = 8
	maximumLimiterPrefixes = 4096
	limiterPrefixTTL       = 30 * time.Minute
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type prefixBucket struct {
	tokenBucket
	used time.Time
}

// enrollmentLimiter stores only process-keyed HMAC digests of network
// prefixes. Raw addresses are neither retained nor made available to logs.
type enrollmentLimiter struct {
	mu       sync.Mutex
	key      []byte
	global   tokenBucket
	prefixes map[[sha256.Size]byte]prefixBucket
	closed   bool
}

func newEnrollmentLimiter(key []byte, now time.Time) *enrollmentLimiter {
	return &enrollmentLimiter{key: append([]byte(nil), key...), global: tokenBucket{tokens: globalEnrollmentBurst, last: now}, prefixes: make(map[[sha256.Size]byte]prefixBucket)}
}

func (l *enrollmentLimiter) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	clear(l.key)
	l.key = nil
	clear(l.prefixes)
	l.prefixes = nil
}

func (l *enrollmentLimiter) Allow(remoteAddress string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.key) == 0 || l.prefixes == nil {
		return false
	}
	digest, ok := l.prefixDigest(remoteAddress)
	if !ok {
		return false
	}
	l.global.refill(now, globalEnrollmentBurst, 5*time.Second)
	prefix, exists := l.prefixes[digest]
	if !exists && l.global.tokens < 1 {
		return false
	}
	if !exists {
		l.evict(now)
	}
	if prefix.last.IsZero() {
		prefix.tokenBucket = tokenBucket{tokens: prefixEnrollmentBurst, last: now}
	}
	prefix.refill(now, prefixEnrollmentBurst, 30*time.Second)
	prefix.used = now
	if l.global.tokens < 1 || prefix.tokens < 1 {
		l.prefixes[digest] = prefix
		return false
	}
	l.global.tokens--
	prefix.tokens--
	l.prefixes[digest] = prefix
	return true
}

func (l *enrollmentLimiter) prefixDigest(remoteAddress string) ([sha256.Size]byte, bool) {
	var zero [sha256.Size]byte
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return zero, false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return zero, false
	}
	var prefix []byte
	if v4 := ip.To4(); v4 != nil {
		prefix = []byte{4, v4[0], v4[1], v4[2]}
	} else {
		v6 := ip.To16()
		if v6 == nil {
			return zero, false
		}
		prefix = make([]byte, 9)
		prefix[0] = 6
		copy(prefix[1:], v6[:8])
	}
	mac := hmac.New(sha256.New, l.key)
	_, _ = mac.Write(prefix)
	clear(prefix)
	var digest [sha256.Size]byte
	copy(digest[:], mac.Sum(nil))
	return digest, true
}

func (b *tokenBucket) refill(now time.Time, burst int, interval time.Duration) {
	if now.Before(b.last) {
		b.last = now
		return
	}
	b.tokens += float64(now.Sub(b.last)) / float64(interval)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
}

func (l *enrollmentLimiter) evict(now time.Time) {
	for digest, bucket := range l.prefixes {
		if now.Sub(bucket.used) >= limiterPrefixTTL {
			delete(l.prefixes, digest)
		}
	}
	for len(l.prefixes) >= maximumLimiterPrefixes {
		var oldest [sha256.Size]byte
		var oldestAt time.Time
		for digest, bucket := range l.prefixes {
			if oldestAt.IsZero() || bucket.used.Before(oldestAt) {
				oldest, oldestAt = digest, bucket.used
			}
		}
		delete(l.prefixes, oldest)
	}
}

// The ten-minute enrollment lifetime bounds outstanding work. Even if every
// globally admitted request persists, 32+600/5=152, below 75% of the
// authoritative PostgreSQL cap (192 of 256). Keep this executable invariant
// beside the limiter constants.
const enrollmentLimiterWorstCase = globalEnrollmentBurst + int(enrollmentLifetime/(5*time.Second))

var _ [192 - enrollmentLimiterWorstCase]struct{}

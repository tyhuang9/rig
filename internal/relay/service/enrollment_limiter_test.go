package service

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrollmentLimiterUsesRemotePrefixAndAtomicBuckets(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	limiter := newEnrollmentLimiter(make([]byte, 32), now)
	defer limiter.Close()
	for i := 0; i < prefixEnrollmentBurst; i++ {
		if !limiter.Allow("203.0.113.9:1234", now) {
			t.Fatalf("request %d rejected", i)
		}
	}
	if limiter.Allow("203.0.113.200:9876", now) {
		t.Fatal("same IPv4 /24 exceeded prefix burst")
	}
	if !limiter.Allow("203.0.114.1:1234", now) {
		t.Fatal("different IPv4 /24 was coupled")
	}
	before := limiter.global.tokens
	if limiter.Allow("invalid-forwarded-value", now) || limiter.global.tokens != before {
		t.Fatal("invalid peer affected limiter")
	}
}

func TestEnrollmentLimiterIPv6PrefixRefillAndExactLRU(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	limiter := newEnrollmentLimiter(make([]byte, 32), now)
	defer limiter.Close()
	for i := 0; i < prefixEnrollmentBurst; i++ {
		if !limiter.Allow("[2001:db8:1:2::1]:443", now) {
			t.Fatalf("request %d rejected", i)
		}
	}
	if limiter.Allow("[2001:db8:1:2:ffff::1]:443", now) {
		t.Fatal("same IPv6 /64 exceeded prefix burst")
	}
	now = now.Add(30 * time.Second)
	if !limiter.Allow("[2001:db8:1:2::2]:443", now) {
		t.Fatal("prefix token did not refill")
	}
	limiter.mu.Lock()
	limiter.global = tokenBucket{tokens: globalEnrollmentBurst, last: now}
	limiter.prefixes = make(map[[32]byte]prefixBucket)
	var oldest, survivor [32]byte
	for i := 0; i < maximumLimiterPrefixes; i++ {
		var digest [32]byte
		digest[0], digest[1] = byte(i), byte(i>>8)
		used := now.Add(-time.Duration(maximumLimiterPrefixes-i) * time.Millisecond)
		limiter.prefixes[digest] = prefixBucket{tokenBucket: tokenBucket{tokens: 1, last: used}, used: used}
		if i == 0 {
			oldest = digest
		}
		if i == maximumLimiterPrefixes-1 {
			survivor = digest
		}
	}
	limiter.mu.Unlock()
	if !limiter.Allow("198.51.100.1:443", now) {
		t.Fatal("new prefix rejected at LRU capacity")
	}
	limiter.mu.Lock()
	_, oldestPresent := limiter.prefixes[oldest]
	_, survivorPresent := limiter.prefixes[survivor]
	count := len(limiter.prefixes)
	limiter.mu.Unlock()
	if count != maximumLimiterPrefixes || oldestPresent || !survivorPresent {
		t.Fatalf("entries=%d oldest=%v survivor=%v", count, oldestPresent, survivorPresent)
	}
}

func TestEnrollmentLimiterGlobalBurstRefillAndConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	limiter := newEnrollmentLimiter(make([]byte, 32), now)
	defer limiter.Close()
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			address := "10." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256) + ".1:443"
			if limiter.Allow(address, now) {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != globalEnrollmentBurst {
		t.Fatalf("concurrent allowed=%d", allowed.Load())
	}
	if limiter.Allow("203.0.113.1:443", now) {
		t.Fatal("global burst exceeded")
	}
	if !limiter.Allow("203.0.113.1:443", now.Add(5*time.Second)) {
		t.Fatal("global token did not refill")
	}
}

func TestEnrollmentLimiterExhaustedGlobalBucketDoesNotSweepForNewPrefix(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	limiter := newEnrollmentLimiter(make([]byte, 32), now)
	defer limiter.Close()
	if !limiter.Allow("203.0.113.1:443", now) {
		t.Fatal("existing prefix setup failed")
	}
	limiter.mu.Lock()
	limiter.global.tokens = 0
	before := len(limiter.prefixes)
	limiter.mu.Unlock()
	if limiter.Allow("198.51.100.1:443", now) {
		t.Fatal("new prefix bypassed exhausted global bucket")
	}
	limiter.mu.Lock()
	after := len(limiter.prefixes)
	limiter.mu.Unlock()
	if after != before {
		t.Fatalf("new rejected prefix mutated LRU: before=%d after=%d", before, after)
	}
	if limiter.Allow("203.0.113.2:443", now) {
		t.Fatal("existing prefix bypassed exhausted global bucket")
	}
}

func TestEnrollmentHandlerIgnoresForwardedHeadersBeforeJSON(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	s := &Service{now: func() time.Time { return now }, enrollmentLimiter: newEnrollmentLimiter(make([]byte, 32), now)}
	defer s.enrollmentLimiter.Close()
	for i := 0; i < prefixEnrollmentBurst+1; i++ {
		request := httptest.NewRequest(http.MethodPost, startEnrollmentPath, strings.NewReader("not-json"))
		request.RemoteAddr = "203.0.113.9:443"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Forwarded", "for=198.51.100."+strconv.Itoa(i+1))
		request.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i+1))
		response := httptest.NewRecorder()
		s.handleStartEnrollment(response, request)
		if i < prefixEnrollmentBurst && response.Code != http.StatusBadRequest {
			t.Fatalf("request %d status=%d", i, response.Code)
		}
		if i == prefixEnrollmentBurst && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "30" || !strings.Contains(response.Body.String(), `"code":"enrollment.rate_limited"`)) {
			t.Fatalf("rate limit status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	}
}

func TestEnrollmentLimiterWorstCaseBelowDatabaseSafetyMargin(t *testing.T) {
	if enrollmentLimiterWorstCase != 152 || enrollmentLimiterWorstCase >= 192 {
		t.Fatalf("worst-case=%d", enrollmentLimiterWorstCase)
	}
}

func TestEnrollmentLimiterCloseSerializesWithAllowAndFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	limiter := newEnrollmentLimiter(make([]byte, 32), now)
	limiter.mu.Lock()
	allowDone := make(chan bool, 1)
	closeDone := make(chan struct{})
	go func() { allowDone <- limiter.Allow("203.0.113.1:443", now) }()
	go func() { limiter.Close(); close(closeDone) }()
	limiter.mu.Unlock()
	<-allowDone
	<-closeDone
	if limiter.Allow("203.0.113.1:443", now) {
		t.Fatal("closed limiter admitted a request")
	}
	limiter.Close()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if !limiter.closed || limiter.key != nil || limiter.prefixes != nil {
		t.Fatalf("closed=%v key=%v prefixes=%v", limiter.closed, limiter.key, limiter.prefixes)
	}
}

package httpserver

import (
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	maxRequestCost    = 20
	maxRateClients    = 4096
	rateClientTTL     = 10 * time.Minute
	rateCleanupStride = 128
)

type clientRateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientRateEntry
	overflow *rate.Limiter
	limit    rate.Limit
	burst    int
	requests uint64
}

type clientRateEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newClientRateLimiter(perMinute, burst int) *clientRateLimiter {
	limit := rate.Every(time.Minute / time.Duration(perMinute))
	return &clientRateLimiter{
		clients: make(map[string]*clientRateEntry), overflow: rate.NewLimiter(limit, burst),
		limit: limit, burst: burst,
	}
}

func (limiter *clientRateLimiter) allow(r *http.Request, cost int) bool {
	now := time.Now()
	key := clientIdentity(r.RemoteAddr)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.requests++
	if limiter.requests%rateCleanupStride == 0 {
		limiter.cleanup(now)
	}
	entry := limiter.clients[key]
	if entry == nil && len(limiter.clients) < maxRateClients {
		entry = &clientRateEntry{limiter: rate.NewLimiter(limiter.limit, limiter.burst)}
		limiter.clients[key] = entry
	}
	if entry == nil {
		return limiter.overflow.AllowN(now, cost)
	}
	entry.lastSeen = now
	return entry.limiter.AllowN(now, cost)
}

func (limiter *clientRateLimiter) cleanup(now time.Time) {
	cutoff := now.Add(-rateClientTTL)
	for key, entry := range limiter.clients {
		if entry.lastSeen.Before(cutoff) {
			delete(limiter.clients, key)
		}
	}
}

func clientIdentity(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	return address.Unmap().String()
}

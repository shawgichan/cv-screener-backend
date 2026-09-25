package ratelimit

import (
	"sync"
	"time"
)

// RateLimiter uses client IP for identity. When deployed behind a reverse proxy,
// the caller is responsible for extracting a trustworthy IP from X-Forwarded-For;
// without a trusted proxy, this header is trivially spoofable.
type RateLimiter struct {
	mu          sync.Mutex
	clients     map[string]clientData
	limitPerDay int
}

type clientData struct {
	count        int
	lastSeenDay  int
	lastSeenYear int
}

func NewRateLimiter(limitPerDay int) *RateLimiter {
	rl := &RateLimiter{
		clients:     make(map[string]clientData),
		limitPerDay: limitPerDay,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) cleanup() {
	for {
		time.Sleep(24 * time.Hour)
		rl.mu.Lock()
		now := time.Now()
		currentDay, currentYear := now.YearDay(), now.Year()
		for ip, data := range rl.clients {
			if data.lastSeenYear != currentYear || data.lastSeenDay != currentDay {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	currentDay, currentYear := now.YearDay(), now.Year()

	data, exists := rl.clients[ip]
	if !exists || data.lastSeenYear != currentYear || data.lastSeenDay != currentDay {
		rl.clients[ip] = clientData{count: 1, lastSeenDay: currentDay, lastSeenYear: currentYear}
		return true
	}

	if data.count >= rl.limitPerDay {
		return false
	}

	data.count++
	rl.clients[ip] = data
	return true
}

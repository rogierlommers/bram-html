package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type rateWindow struct {
	started time.Time
	count   int
}

type requestLimiter struct {
	mu          sync.Mutex
	perIP       map[string]rateWindow
	global      rateWindow
	now         func() time.Time
	perIPLimit  int
	globalLimit int
	lastCleanup time.Time
}

func newRequestLimiter(perIPLimit, globalLimit int) *requestLimiter {
	return &requestLimiter{
		perIP: make(map[string]rateWindow), now: time.Now,
		perIPLimit: perIPLimit, globalLimit: globalLimit,
	}
}

func (l *requestLimiter) Allow(r *http.Request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.global = advanceWindow(l.global, now, time.Minute)
	if l.globalLimit > 0 && l.global.count >= l.globalLimit {
		return false
	}

	if l.perIPLimit > 0 {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		window := advanceWindow(l.perIP[host], now, 10*time.Minute)
		if window.count >= l.perIPLimit {
			return false
		}
		window.count++
		l.perIP[host] = window
		if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
			for key, candidate := range l.perIP {
				if now.Sub(candidate.started) >= 10*time.Minute {
					delete(l.perIP, key)
				}
			}
			l.lastCleanup = now
		}
	}
	l.global.count++
	return true
}

func advanceWindow(window rateWindow, now time.Time, duration time.Duration) rateWindow {
	if window.started.IsZero() || now.Sub(window.started) >= duration {
		return rateWindow{started: now}
	}
	return window
}

package auth

import (
	"sync"
	"time"

	"github.com/tikhonp/proxier/internal/platform/vault"
)

// ephemeral holds short-lived state between two requests of one sign-in or
// passkey ceremony, under a random token. It lives in memory: a restart only
// makes the admin start that sign-in again.
type ephemeral[T any] struct {
	ttl time.Duration
	max int // the oldest entry makes room past it, so a flood can't grow memory

	mu    sync.Mutex
	items map[string]ephemeralItem[T]
}

type ephemeralItem[T any] struct {
	v       T
	expires time.Time
}

func newEphemeral[T any](ttl time.Duration, max int) *ephemeral[T] {
	return &ephemeral[T]{ttl: ttl, max: max, items: map[string]ephemeralItem[T]{}}
}

func (e *ephemeral[T]) put(now time.Time, v T) string {
	token := vault.NewToken()
	e.putAt(token, v, now.Add(e.ttl), now)
	return token
}

func (e *ephemeral[T]) putAt(token string, v T, expires, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, it := range e.items {
		if !now.Before(it.expires) {
			delete(e.items, k)
		}
	}
	for len(e.items) >= e.max {
		oldest := ""
		for k, it := range e.items {
			if oldest == "" || it.expires.Before(e.items[oldest].expires) {
				oldest = k
			}
		}
		delete(e.items, oldest)
	}
	e.items[token] = ephemeralItem[T]{v, expires}
}

// take removes the entry: every token is good for one try.
func (e *ephemeral[T]) take(token string, now time.Time) (v T, expires time.Time, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, found := e.items[token]
	delete(e.items, token)
	if !found || !now.Before(it.expires) {
		return v, time.Time{}, false
	}
	return it.v, it.expires, true
}

package mailbox

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kacperkwapisz/mail-mcp/internal/config"
)

func newTestPool(t *testing.T, idleConnTTL time.Duration) *Pool {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := NewPool(&config.Config{IdleConnTTL: idleConnTTL}, logger)
	t.Cleanup(pool.Close)
	return pool
}

func TestCloseIdleConnections(t *testing.T) {
	const idleConnTTL = time.Hour
	pool := newTestPool(t, idleConnTTL)
	now := time.Now()

	idle := &pooledConn{lastUsed: now.Add(-idleConnTTL), selected: "INBOX"}
	recent := &pooledConn{lastUsed: now.Add(-idleConnTTL + time.Second), selected: "INBOX"}
	busy := &pooledConn{lastUsed: now.Add(-2 * idleConnTTL), selected: "INBOX"}
	busy.mu.Lock()
	pool.mu.Lock()
	pool.conns["idle"] = idle
	pool.conns["recent"] = recent
	pool.conns["busy"] = busy
	pool.mu.Unlock()

	pool.closeIdleConnections(now)

	if _, ok := pool.conns["idle"]; ok {
		t.Error("idle connection was not removed from the pool")
	}
	if idle.selected != "" {
		t.Error("idle connection state was not reset")
	}
	if _, ok := pool.conns["recent"]; !ok {
		t.Error("recent connection was removed")
	}
	if _, ok := pool.conns["busy"]; !ok {
		t.Error("busy connection was removed")
	}

	busy.mu.Unlock()
	pool.closeIdleConnections(now)
	if _, ok := pool.conns["busy"]; ok {
		t.Error("idle connection was not removed after it became available")
	}
}

func TestIdleConnectionCleanupRunsPeriodically(t *testing.T) {
	const idleConnTTL = 20 * time.Millisecond
	pool := newTestPool(t, idleConnTTL)
	pool.mu.Lock()
	pool.conns["idle"] = &pooledConn{lastUsed: time.Now()}
	pool.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for {
		pool.mu.Lock()
		_, exists := pool.conns["idle"]
		pool.mu.Unlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic cleanup did not remove the idle connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPoolCloseIsIdempotentAndRejectsNewWork(t *testing.T) {
	pool := newTestPool(t, time.Hour)
	pool.Close()
	pool.Close()

	err := pool.Do(context.Background(), &config.Account{ID: "test"}, func(*Session) error {
		return nil
	})
	if err == nil {
		t.Fatal("Do succeeded after the pool was closed")
	}
}

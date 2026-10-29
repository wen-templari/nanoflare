// Package browserstore defines serialized storage for encrypted OIDC browser records.
package browserstore

import (
	"context"
	"sync"
	"time"
)

// Store must serialize updates for a key across all clients. An error rolls back
// the update; nil data deletes the record. Expired records are returned as absent.
type Store interface {
	UpdateBrowserRecord(context.Context, string, func([]byte) ([]byte, time.Time, error)) error
	CleanupBrowserRecords(context.Context) error
}

type record struct {
	data    []byte
	expires time.Time
}
type Memory struct {
	mu      sync.Mutex
	records map[string]record
}

func NewMemory() *Memory { return &Memory{records: make(map[string]record)} }
func (m *Memory) UpdateBrowserRecord(ctx context.Context, key string, fn func([]byte) ([]byte, time.Time, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	r := m.records[key]
	if !r.expires.After(time.Now()) {
		r.data = nil
	}
	data, expires, err := fn(r.data)
	if err != nil {
		return err
	}
	if data == nil {
		delete(m.records, key)
	} else {
		m.records[key] = record{data, expires}
	}
	return nil
}
func (m *Memory) CleanupBrowserRecords(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.records {
		if !r.expires.After(time.Now()) {
			delete(m.records, k)
		}
	}
	return ctx.Err()
}

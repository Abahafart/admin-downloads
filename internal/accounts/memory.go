package accounts

import (
	"context"
	"maps"
	"sync"
)

// MemoryStore is an in-memory SecretStore for tests and ephemeral runs.
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]Secret
}

var _ SecretStore = (*MemoryStore)(nil)

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore { return &MemoryStore{m: make(map[string]Secret)} }

// Get implements SecretStore.
func (s *MemoryStore) Get(_ context.Context, id string) (Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec, ok := s.m[id]
	if !ok {
		return Secret{}, ErrSecretNotFound
	}
	return cloneSecret(sec), nil
}

// Set implements SecretStore.
func (s *MemoryStore) Set(_ context.Context, id string, sec Secret) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = cloneSecret(sec)
	return nil
}

// Delete implements SecretStore.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

// cloneSecret deep-copies mutable fields so callers cannot alias the store.
func cloneSecret(s Secret) Secret {
	if s.Extra != nil {
		s.Extra = maps.Clone(s.Extra)
	}
	if s.OAuthToken != nil {
		t := *s.OAuthToken
		s.OAuthToken = &t
	}
	return s
}

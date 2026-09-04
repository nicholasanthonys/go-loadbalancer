package config

import "sync/atomic"

// Store holds the currently active Config behind an atomic pointer so
// reads and reloads never race: a reader that calls Get gets a fully
// formed *Config — either the old one or the new one, never a struct
// with some fields swapped and some not — because Store's single
// atomic.Pointer write is the only thing that ever changes what Get
// returns. Nothing ever mutates a Config's fields in place.
type Store struct {
	current atomic.Pointer[Config]
}

// NewStore builds a Store already holding cfg, so callers don't have to
// special-case "no config yet" on startup.
func NewStore(cfg *Config) *Store {
	s := &Store{}
	s.current.Store(cfg)
	return s
}

// Get returns the config currently in effect. Safe to call concurrently
// with Reload from any number of goroutines.
func (s *Store) Get() *Config {
	return s.current.Load()
}

// Reload re-reads and re-validates the config file at path and, only if
// that succeeds, atomically swaps it in. A bad edit — a typo, an unknown
// algorithm — leaves the previously loaded config serving traffic
// instead of taking the load balancer down or half-applying the change.
func (s *Store) Reload(path string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	s.current.Store(cfg)
	return nil
}

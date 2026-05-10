package plugin

import (
	"fmt"
	"sync"
)

// Registry holds the active set of SourcePlugin implementations and supports
// lookup by ID, by intent, and by region (for EU-mode gating).
//
// Cycle-1 status: skeleton — Register / Resolve work; ListByIntent and
// ListByRegion return empty until the SourcePlugin interface gains
// QueryIntents and Residency() (cycles 1-task-#4 and 2 respectively).
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]SourcePlugin
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]SourcePlugin)}
}

// Register adds a plugin. Returns an error if a plugin with the same ID is
// already registered (deliberate strictness — duplicate registration is a
// configuration bug, not something to silently overwrite).
func (r *Registry) Register(p SourcePlugin) error {
	if p == nil {
		return fmt.Errorf("retrievr/plugin: register: nil plugin")
	}
	id := p.ID()
	if id == "" {
		return fmt.Errorf("retrievr/plugin: register: plugin returned empty ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[id]; exists {
		return fmt.Errorf("retrievr/plugin: register: duplicate plugin id %q", id)
	}
	r.plugins[id] = p
	return nil
}

// Resolve looks up a plugin by ID. Returns nil, false when not found.
func (r *Registry) Resolve(id string) (SourcePlugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	return p, ok
}

// All returns every registered plugin (in unspecified order). Callers must
// not mutate the returned slice.
func (r *Registry) All() []SourcePlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]SourcePlugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// Len returns the number of registered plugins.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.plugins)
}

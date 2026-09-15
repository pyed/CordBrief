package bot

import "sync"

// ModelCache stores discovered provider models in memory during the daemon lifetime.
// It tracks the provider base URL and an integer generation to ensure stale Telegram
// callback queries cannot accidentally select models from an outdated list.
type ModelCache struct {
	mu         sync.RWMutex
	baseURL    string
	generation int64
	models     []string
}

// NewModelCache creates an empty in-memory ModelCache.
func NewModelCache() *ModelCache {
	return &ModelCache{}
}

// Get returns cached models and the active cache generation for the specified base URL.
// Returns false if the cache is empty or belongs to a different base URL.
func (c *ModelCache) Get(baseURL string) ([]string, int64, bool) {
	if c == nil {
		return nil, 0, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.models == nil || c.baseURL != baseURL {
		return nil, 0, false
	}

	out := make([]string, len(c.models))
	copy(out, c.models)
	return out, c.generation, true
}

// Set replaces the cached models, updates the base URL, and increments the cache generation.
func (c *ModelCache) Set(baseURL string, models []string) int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.baseURL = baseURL
	c.models = make([]string, len(models))
	copy(c.models, models)
	c.generation++
	return c.generation
}

// Validate checks whether the given generation and index point to a valid model in the active cache snapshot
// for the specified base URL.
func (c *ModelCache) Validate(baseURL string, gen int64, index int) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.baseURL != baseURL || c.generation != gen || index < 0 || index >= len(c.models) {
		return "", false
	}

	return c.models[index], true
}

// Invalidate clears the model cache.
func (c *ModelCache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.models = nil
	c.baseURL = ""
}

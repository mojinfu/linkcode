// Package pricing calculates API costs from token usage and model pricing rules.
// It is model-agnostic: add a model to the config YAML and it works without code changes.
package pricing

import (
	"math"
	"sync"
)

// ModelPricing holds per-million-token prices and currency symbol for a specific model.
type ModelPricing struct {
	CacheHitInput  float64 `yaml:"cache_hit_input"`  // per 1M tokens for cache hits
	CacheMissInput float64 `yaml:"cache_miss_input"` // per 1M tokens for cache misses
	Output         float64 `yaml:"output"`           // per 1M tokens for output
	Symbol         string  `yaml:"symbol"`           // currency symbol, e.g. "¥", "$"
}

// Calculator computes API cost from token usage and model pricing.
type Calculator interface {
	// Cost returns the cost for the given token usage on the given model.
	// Returns NaN if the model is not configured (caller should display "?").
	Cost(model string, inputTokens, cacheReadTokens, outputTokens int) float64

	// Symbol returns the currency symbol for a model, or "?" if unknown.
	Symbol(model string) string
}

type calculator struct {
	mu       sync.RWMutex
	pricings map[string]ModelPricing
}

// New creates a Calculator from a map of model name → pricing.
func New(pricings map[string]ModelPricing) Calculator {
	// Defensive copy so the caller can't mutate the map after construction.
	m := make(map[string]ModelPricing, len(pricings))
	for k, v := range pricings {
		m[k] = v
	}
	return &calculator{pricings: m}
}

func (c *calculator) Cost(model string, inputTokens, cacheReadTokens, outputTokens int) float64 {
	c.mu.RLock()
	p, ok := c.pricings[model]
	c.mu.RUnlock()
	if !ok {
		return math.NaN()
	}

	cacheRead := cacheReadTokens
	if cacheRead > inputTokens {
		cacheRead = inputTokens
	}
	cacheMissTokens := inputTokens - cacheRead

	cost := (p.CacheHitInput*float64(cacheRead) +
		p.CacheMissInput*float64(cacheMissTokens) +
		p.Output*float64(outputTokens)) / 1_000_000
	return cost
}

func (c *calculator) Symbol(model string) string {
	c.mu.RLock()
	p, ok := c.pricings[model]
	c.mu.RUnlock()
	if !ok || p.Symbol == "" {
		return "?"
	}
	return p.Symbol
}

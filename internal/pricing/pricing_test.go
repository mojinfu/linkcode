package pricing

import (
	"math"
	"testing"
)

func TestCalculator_Cost(t *testing.T) {
	c := New(map[string]ModelPricing{
		"deepseek-v4-pro": {
			CacheHitInput:  0.0036,
			CacheMissInput: 0.435,
			Output:         0.87,
			Symbol:         "¥",
		},
	})

	tests := []struct {
		name           string
		model          string
		inputTokens    int
		cacheReadTokens int
		outputTokens   int
		want           float64
	}{
		{
			name:           "zero tokens",
			model:          "deepseek-v4-pro",
			inputTokens:    0,
			cacheReadTokens: 0,
			outputTokens:   0,
			want:           0,
		},
		{
			name:           "basic calculation",
			model:          "deepseek-v4-pro",
			inputTokens:    10000,
			cacheReadTokens: 5000,
			outputTokens:   100,
			want:           ((0.0036*5000 + 0.435*5000 + 0.87*100) / 1_000_000),
		},
		{
			name:           "all cache hits",
			model:          "deepseek-v4-pro",
			inputTokens:    10000,
			cacheReadTokens: 10000,
			outputTokens:   0,
			want:           (0.0036 * 10000 / 1_000_000),
		},
		{
			name:           "no cache hits",
			model:          "deepseek-v4-pro",
			inputTokens:    1000000,
			cacheReadTokens: 0,
			outputTokens:   0,
			want:           0.435,
		},
		{
			name:           "cacheRead exceeds input (clamped)",
			model:          "deepseek-v4-pro",
			inputTokens:    1000,
			cacheReadTokens: 5000,
			outputTokens:   0,
			want:           0.0036 * 1000 / 1_000_000,
		},
		{
			name:           "unknown model returns NaN",
			model:          "unknown-model",
			inputTokens:    1000,
			cacheReadTokens: 0,
			outputTokens:   500,
		},
		{
			name:           "empty model returns NaN",
			model:          "",
			inputTokens:    1000,
			cacheReadTokens: 0,
			outputTokens:   500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Cost(tt.model, tt.inputTokens, tt.cacheReadTokens, tt.outputTokens)
			if tt.model == "unknown-model" || tt.model == "" {
				if !math.IsNaN(got) {
					t.Errorf("Cost(%q) = %v, want NaN", tt.model, got)
				}
				return
			}
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("Cost(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestCalculator_Symbol(t *testing.T) {
	c := New(map[string]ModelPricing{
		"deepseek-v4-pro": {
			CacheHitInput:  0.0036,
			CacheMissInput: 0.435,
			Output:         0.87,
			Symbol:         "¥",
		},
		"deepseek-v4-flash": {
			CacheHitInput:  0.0028,
			CacheMissInput: 0.14,
			Output:         0.28,
			Symbol:         "",
		},
	})

	if got := c.Symbol("deepseek-v4-pro"); got != "¥" {
		t.Errorf("Symbol(deepseek-v4-pro) = %q, want %q", got, "¥")
	}
	if got := c.Symbol("deepseek-v4-flash"); got != "?" {
		t.Errorf("Symbol(deepseek-v4-flash) = %q, want %q (empty symbol → ?)", got, "?")
	}
	if got := c.Symbol("unknown"); got != "?" {
		t.Errorf("Symbol(unknown) = %q, want %q", got, "?")
	}
}

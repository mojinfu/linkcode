// Package channel defines the abstraction for IM platform channels.
package channel

import "time"

// Styler formats messages with platform-specific markup.
// Each IM platform (WeCom, Slack, DingTalk, etc.) provides its own implementation.
// Business logic depends only on this interface, never on a concrete platform.
type Styler interface {
	// Box wraps content in a titled container (e.g. code block, card).
	Box(title, content string) string

	// Bar renders a thin status-bar-style header (inline code, bold, etc.).
	Bar(title string) string

	// Bold applies bold/strong emphasis.
	Bold(text string) string

	// DiffSuffix returns a platform-specific suffix that varies with seq,
	// defeating content-based dedup in streaming protocols.
	DiffSuffix(seq int) string

	// Quote wraps text in platform-specific blockquote markup.
	Quote(text string) string

	// StreamWarning returns a platform-formatted timeout warning.
	StreamWarning(remaining time.Duration) string
}

// PlainStyler returns text without any markup, suitable as a fallback
// when no platform-specific styler is available.
type PlainStyler struct{}

func (PlainStyler) Box(_, content string) string              { return content }
func (PlainStyler) Bar(title string) string                   { return title }
func (PlainStyler) Bold(text string) string                   { return text }
func (PlainStyler) DiffSuffix(seq int) string                 { return "" }
func (PlainStyler) Quote(text string) string                  { return text }
func (PlainStyler) StreamWarning(remaining time.Duration) string { return "" }

// Package channel defines the abstraction for IM platform channels.
package channel

// Styler formats messages with platform-specific markup.
// Each IM platform (WeCom, Slack, DingTalk, etc.) provides its own implementation.
// Business logic depends only on this interface, never on a concrete platform.
type Styler interface {
	// Box wraps content in a titled container (e.g. code block, card).
	Box(title, content string) string

	// Bar renders a thin status-bar-style header (empty-body code block).
	Bar(title string) string

	// Bold applies bold/strong emphasis.
	Bold(text string) string
}

// PlainStyler returns text without any markup, suitable as a fallback
// when no platform-specific styler is available.
type PlainStyler struct{}

func (PlainStyler) Box(_, content string) string { return content }
func (PlainStyler) Bar(title string) string      { return title }
func (PlainStyler) Bold(text string) string      { return text }

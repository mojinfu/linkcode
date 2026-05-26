// Package browser provides a CDP-based browser controller using Chrome DevTools Protocol.
// It is a standalone module with no LinkCode dependencies, suitable for independent use.
package browser

import "context"

// PageContent holds the extracted content of a web page.
type PageContent struct {
	URL   string
	Title string
	Text  string // visible text content, no HTML tags
	HTML  string // full inner HTML of <body>
}

// Browser controls a headless Chrome instance via CDP.
type Browser interface {
	// Open navigates to a URL and returns the page content.
	Open(ctx context.Context, url string) (*PageContent, error)

	// Screenshot captures a full-page PNG screenshot of the given URL.
	Screenshot(ctx context.Context, url string) ([]byte, error)

	// Close shuts down the browser process and releases resources.
	Close() error
}

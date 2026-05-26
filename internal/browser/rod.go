package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// rodBrowser implements Browser using the Rod CDP library.
type rodBrowser struct {
	browser *rod.Browser
}

// New creates a new Browser instance. It connects to a headless Chrome
// process that Rod manages automatically.
func New() (Browser, error) {
	b := rod.New()
	if b == nil {
		return nil, fmt.Errorf("browser: rod.New returned nil")
	}
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("browser: connect to Chrome: %w", err)
	}
	return &rodBrowser{browser: b}, nil
}

// Open navigates to url and extracts page content.
func (r *rodBrowser) Open(ctx context.Context, url string) (*PageContent, error) {
	page, err := r.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("browser: create page: %w", err)
	}
	defer page.MustClose()

	if err := page.Context(ctx).Navigate(url); err != nil {
		return nil, fmt.Errorf("browser: navigate to %s: %w", url, err)
	}
	page.MustWaitLoad()

	info, err := page.Info()
	if err != nil {
		return nil, fmt.Errorf("browser: page info: %w", err)
	}

	text, err := page.Element("body")
	if err != nil {
		return nil, fmt.Errorf("browser: find body: %w", err)
	}
	bodyText := text.MustText()

	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("browser: page html: %w", err)
	}

	return &PageContent{
		URL:   info.URL,
		Title: info.Title,
		Text:  bodyText,
		HTML:  html,
	}, nil
}

// Screenshot captures a full-page PNG screenshot of url.
func (r *rodBrowser) Screenshot(ctx context.Context, url string) ([]byte, error) {
	page, err := r.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("browser: create page: %w", err)
	}
	defer page.MustClose()

	if err := page.Context(ctx).Navigate(url); err != nil {
		return nil, fmt.Errorf("browser: navigate to %s: %w", url, err)
	}
	page.MustWaitLoad()

	return page.Screenshot(true, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
}

// Close shuts down the browser process.
func (r *rodBrowser) Close() error {
	return r.browser.Close()
}

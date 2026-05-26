package browser

import (
	"context"
	"testing"
	"time"
)

// TestOpen verifies that Open navigates to a public page and extracts content.
func TestOpen(t *testing.T) {
	b, err := New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pc, err := b.Open(ctx, "https://example.com")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if pc.Title == "" {
		t.Error("title is empty")
	}
	if pc.Text == "" {
		t.Error("body text is empty")
	}
	if pc.HTML == "" {
		t.Error("html is empty")
	}

	t.Logf("Title: %s", pc.Title)
	t.Logf("Text (first 200 chars): %.200s", pc.Text)
	t.Logf("URL: %s", pc.URL)
}

// TestScreenshot verifies that Screenshot returns a non-empty PNG.
func TestScreenshot(t *testing.T) {
	b, err := New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	png, err := b.Screenshot(ctx, "https://example.com")
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}

	if len(png) == 0 {
		t.Error("screenshot is empty")
	}

	// PNG signature: 137 80 78 71 13 10 26 10
	if len(png) < 8 || png[0] != 0x89 || png[1] != 0x50 || png[2] != 0x4E || png[3] != 0x47 {
		t.Error("screenshot is not a valid PNG")
	}

	t.Logf("Screenshot: %d bytes", len(png))
}

// TestTimeout verifies that context cancellation stops Open.
func TestTimeout(t *testing.T) {
	b, err := New()
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond) // ensure context has expired

	_, err = b.Open(ctx, "https://example.com")
	if err == nil {
		t.Error("expected error from expired context, got nil")
	}
	t.Logf("Expected error: %v", err)
}

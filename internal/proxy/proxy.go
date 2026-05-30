// Package proxy provides a local HTTP reverse proxy that transforms Anthropic API
// requests for DeepSeek compatibility. DeepSeek's Anthropic-compatible endpoint
// rejects "system" as a message role. The proxy handles two cases:
//
//  1. Top-level "system" field — moved into the messages array as a system message,
//     then converted to role "user".
//  2. "role":"system" entries in the messages array — converted to "role":"user".
//
// The core transformation is available standalone (TransformBody) or as an
// http.RoundTripper (Transport) that can be injected into any http.Client.
package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Transport is an http.RoundTripper that transforms Anthropic API requests for
// DeepSeek compatibility before sending them upstream. It wraps the next
// RoundTripper in the chain (typically http.DefaultTransport).
//
// Usage:
//
//	client := &http.Client{
//	    Transport: &proxy.Transport{Next: http.DefaultTransport},
//	}
//	client.Do(req) // request body is automatically transformed
type Transport struct {
	Next http.RoundTripper
}

// RoundTrip implements http.RoundTripper. It transforms the request body before
// delegating to the next transport.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return t.next().RoundTrip(req)
	}

	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return t.next().RoundTrip(req)
	}

	transformed, err := transformRoles(body)
	if err != nil {
		log.Printf("[proxy] transform error: %v, passing through unchanged", err)
		transformed = body
	}

	req.Body = io.NopCloser(bytes.NewReader(transformed))
	req.ContentLength = int64(len(transformed))
	return t.next().RoundTrip(req)
}

func (t *Transport) next() http.RoundTripper {
	if t.Next != nil {
		return t.Next
	}
	return http.DefaultTransport
}

// TransformBody applies DeepSeek-compatible role transformation to an Anthropic
// API request body. It handles top-level "system" fields and "role":"system"
// entries in the messages array.
func TransformBody(body []byte) ([]byte, error) {
	return transformRoles(body)
}

// Server is an HTTP reverse proxy that transforms Anthropic API requests for
// DeepSeek compatibility. It listens locally and forwards to the upstream API,
// using Transport to rewrite request bodies on the way out.
type Server struct {
	upstream string
	addr     string
	listener net.Listener
	ready    chan struct{}
}

// New creates a proxy server with the given listen address and upstream API base URL.
// The upstream URL should be the Anthropic-compatible base URL (e.g. https://api.deepseek.com).
func New(addr, upstream string) *Server {
	return &Server{
		addr:     addr,
		upstream: upstream,
		ready:    make(chan struct{}),
	}
}

// Addr returns the listen address.
func (s *Server) Addr() string { return s.addr }

// Ready returns a channel that closes when the server is accepting connections.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Start begins listening and blocks until the server stops.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.listener = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.forward)

	log.Printf("[proxy] DeepSeek API proxy on %s -> %s", s.addr, s.upstream)
	close(s.ready)
	return http.Serve(ln, mux)
}

// Close shuts down the proxy server.
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// upstreamBase returns scheme+host+path from the upstream URL, preserving any path prefix.
func (s *Server) upstreamBase() string {
	u, err := url.Parse(s.upstream)
	if err != nil {
		return s.upstream
	}
	base := u.Scheme + "://" + u.Host
	if u.Path != "" && u.Path != "/" {
		base += strings.TrimSuffix(u.Path, "/")
	}
	return base
}

// forward receives the incoming request and relays it upstream. Request bodies are
// transformed for DeepSeek compatibility by the Transport during the outbound call.
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	upstreamURL := s.upstreamBase() + r.URL.Path
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequest(r.Method, upstreamURL, r.Body)
	if err != nil {
		http.Error(w, "proxy: create request", http.StatusBadGateway)
		return
	}

	for k, vs := range r.Header {
		if strings.EqualFold(k, "Host") {
			if u, parseErr := url.Parse(s.upstream); parseErr == nil {
				req.Header[k] = []string{u.Host}
			}
			continue
		}
		req.Header[k] = vs
	}
	req.ContentLength = r.ContentLength

	client := &http.Client{Transport: &Transport{Next: http.DefaultTransport}}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[proxy] upstream error: %v", err)
		http.Error(w, "proxy: upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// transformRoles rewrites the request body to be DeepSeek-compatible:
//   - Top-level "system" field → prepended as a message with role "user"
//   - Any "role":"system" in messages → "role":"user"
func transformRoles(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	systemContent := extractSystem(raw)

	messages, ok := raw["messages"].([]any)
	if !ok {
		return json.Marshal(raw)
	}

	if systemContent != "" {
		sysMsg := map[string]any{
			"role":    "user",
			"content": "[system]\n" + systemContent,
		}
		messages = append([]any{sysMsg}, messages...)
		raw["messages"] = messages
	}

	for i, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role == "system" {
			msg["role"] = "user"
			messages[i] = msg
		}
	}

	return json.Marshal(raw)
}

// extractSystem removes and returns the top-level "system" field content as a string.
// It handles both string and array-of-content-blocks formats.
func extractSystem(raw map[string]any) string {
	sys, ok := raw["system"]
	if !ok {
		return ""
	}
	delete(raw, "system")

	switch v := sys.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, block := range v {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := b["type"].(string); t == "text" {
				if text, _ := b["text"].(string); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		data, _ := json.Marshal(sys)
		return string(data)
	}
}

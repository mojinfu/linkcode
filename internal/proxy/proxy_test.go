package proxy

import (
	"encoding/json"
	"testing"
)

// ClaudeCodeRequest is a realistic Claude Code stream-json input that would be
// sent to the Anthropic Messages API. Claude Code typically includes system
// instructions via a top-level "system" field.
const ClaudeCodeRequest = `{
	"model": "claude-sonnet-4-6",
	"max_tokens": 4096,
	"system": "You are an expert Go programmer. Always respond in Chinese.",
	"messages": [
		{"role": "user", "content": "Write a hello world HTTP server"}
	]
}`

// ClaudeCodeRequestWithSystemMsg represents the case where Claude Code puts
// system instructions inside the messages array as role:"system".
const ClaudeCodeRequestWithSystemMsg = `{
	"model": "claude-sonnet-4-6",
	"max_tokens": 4096,
	"messages": [
		{"role": "system", "content": "You are an expert Go programmer."},
		{"role": "user", "content": "Write a hello world HTTP server"}
	]
}`

// hasSystemRole reports whether a JSON request body contains any message with
// role "system" — the exact pattern DeepSeek's Anthropic-compatible endpoint rejects.
func hasSystemRole(body []byte) bool {
	var raw struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	for _, m := range raw.Messages {
		if role, _ := m["role"].(string); role == "system" {
			return true
		}
	}
	return false
}

// hasTopLevelSystem reports whether the JSON has a top-level "system" key.
func hasTopLevelSystem(body []byte) bool {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return false
	}
	_, ok := raw["system"]
	return ok
}

// =============================================================================
// Part 1: Demonstrate the problem — DeepSeek rejects these requests
// =============================================================================

// TestProblem_DeepSeekRejectsTopLevelSystem shows that a typical Claude Code
// API request contains a top-level "system" field. DeepSeek's API rejects this.
func TestProblem_DeepSeekRejectsTopLevelSystem(t *testing.T) {
	body := []byte(ClaudeCodeRequest)

	// PROVE the problem: raw request has a top-level "system" field.
	if !hasTopLevelSystem(body) {
		t.Fatal("BUG: test input is wrong — expected top-level 'system' field")
	}
	t.Log("CONFIRMED: raw Claude Code request has top-level 'system' field")
	t.Log("           → DeepSeek API would REJECT this request")
	t.Logf("Raw request:\n%s", body)
}

// TestProblem_DeepSeekRejectsMessagesSystemRole shows that when Claude Code
// puts system instructions as role:"system" inside messages, DeepSeek also
// rejects it.
func TestProblem_DeepSeekRejectsMessagesSystemRole(t *testing.T) {
	body := []byte(ClaudeCodeRequestWithSystemMsg)

	// PROVE the problem: raw request contains "role":"system".
	if !hasSystemRole(body) {
		t.Fatal("BUG: test input is wrong — expected 'role':'system' in messages")
	}
	t.Log("CONFIRMED: raw request has 'role':'system' in messages array")
	t.Log("           → DeepSeek API would REJECT this request")
	t.Logf("Raw request:\n%s", body)
}

// =============================================================================
// Part 2: Demonstrate the fix — proxy eliminates all system roles
// =============================================================================

// TestFix_ProxyEliminatesTopLevelSystem shows that after transformation,
// the top-level "system" field is removed and converted to a user message.
func TestFix_ProxyEliminatesTopLevelSystem(t *testing.T) {
	body := []byte(ClaudeCodeRequest)

	// Step 1: Confirm the raw request IS problematic.
	if !hasTopLevelSystem(body) {
		t.Fatal("precondition: raw request must have top-level system")
	}

	// Step 2: Apply proxy transformation.
	result, err := transformRoles(body)
	if err != nil {
		t.Fatalf("transformRoles: %v", err)
	}

	// Step 3: Confirm the problem is GONE.
	if hasTopLevelSystem(result) {
		t.Error("FAIL: top-level 'system' field still present after transform")
	}
	if hasSystemRole(result) {
		t.Error("FAIL: 'role':'system' still present in output — DeepSeek would STILL reject")
	}

	t.Log("FIX VERIFIED: after proxy transform, no 'system' role remains")
	t.Logf("Transformed request:\n%s", result)
}

// TestFix_ProxyEliminatesMessagesSystemRole shows that role:"system" in
// messages is converted to role:"user".
func TestFix_ProxyEliminatesMessagesSystemRole(t *testing.T) {
	body := []byte(ClaudeCodeRequestWithSystemMsg)

	if !hasSystemRole(body) {
		t.Fatal("precondition: raw request must have role:system in messages")
	}

	result, err := transformRoles(body)
	if err != nil {
		t.Fatalf("transformRoles: %v", err)
	}

	if hasSystemRole(result) {
		t.Error("FAIL: 'role':'system' still present after transform")
	}

	t.Log("FIX VERIFIED: messages with role:system converted to role:user")
	t.Logf("Transformed request:\n%s", result)
}

// TestFix_ProxyHandlesBothCasesSimultaneously tests the combined case:
// top-level system field + messages-level system role.
func TestFix_ProxyHandlesBothCasesSimultaneously(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 4096,
		"system": "Top-level instructions.",
		"messages": [
			{"role": "system", "content": "Message-level instructions."},
			{"role": "user", "content": "Do something"}
		]
	}`)

	if !hasTopLevelSystem(body) || !hasSystemRole(body) {
		t.Fatal("precondition: raw request must have both top-level system AND role:system in messages")
	}
	t.Log("CONFIRMED: raw request has BOTH top-level system AND role:system in messages")
	t.Log("           → This is the worst case for DeepSeek compatibility")

	result, err := transformRoles(body)
	if err != nil {
		t.Fatalf("transformRoles: %v", err)
	}

	if hasTopLevelSystem(result) {
		t.Error("FAIL: top-level 'system' still present")
	}
	if hasSystemRole(result) {
		t.Error("FAIL: 'role':'system' still present")
	}

	// Verify all messages are now role:"user".
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	msgs := parsed["messages"].([]any)
	for i, m := range msgs {
		msg := m.(map[string]any)
		if msg["role"] != "user" {
			t.Errorf("message %d: role is '%s', expected 'user'", i, msg["role"])
		}
	}

	t.Log("FIX VERIFIED: all system roles eliminated, all messages are role:user")
	t.Logf("Transformed request:\n%s", result)
}

// =============================================================================
// Part 3: Edge cases — ensure proxy doesn't break normal requests
// =============================================================================

// TestEdge_NoSystemPassesThrough verifies that a request without any system
// content is not corrupted by the proxy.
func TestEdge_NoSystemPassesThrough(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	result, err := transformRoles(body)
	if err != nil {
		t.Fatalf("transformRoles: %v", err)
	}

	// The result should still be valid JSON with 1 message.
	var raw map[string]any
	if err := json.Unmarshal(result, &raw); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	msgs := raw["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

// TestEdge_UserContentWithSystemText is NOT corrupted.
// The old string-replace approach would have broken this; JSON-level transform
// leaves user content alone.
func TestEdge_UserContentWithSystemText(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-6",
		"max_tokens": 4096,
		"messages": [
			{"role": "user", "content": "What does role:system mean? Is system role supported?"}
		]
	}`)

	result, err := transformRoles(body)
	if err != nil {
		t.Fatalf("transformRoles: %v", err)
	}

	var raw map[string]any
	json.Unmarshal(result, &raw)
	msgs := raw["messages"].([]any)
	content := msgs[0].(map[string]any)["content"].(string)

	if content != "What does role:system mean? Is system role supported?" {
		t.Errorf("user content was corrupted: %s", content)
	}
	t.Log("EDGE CASE PASSED: user content containing 'role:system' text was not touched")
}

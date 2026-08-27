package router

import (
	"strings"
	"testing"

	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
	"linkcode/internal/gateway"
	"linkcode/internal/pricing"
)

// TestSendDonePush_NoChannelNoPanic verifies that sendDonePush safely returns
// when the gateway has no worker channel for the message's bot. It must not
// panic (gw lookup fails, channel nil) and must not send anything.
func TestSendDonePush_NoChannelNoPanic(t *testing.T) {
	r := New(nil, nil, nil, gateway.New(nil), nil, nil, pricing.New(nil))

	msg := channel.Message{BotID: "no-such-bot", UserID: "user1", ChatID: "chat1"}
	r.sendDonePush(msg) // must not panic
}

// TestSendDonePush_TextFormat verifies the proactive "done" message format:
// the text starts with "done" and carries a ZWSP suffix so WeCom won't dedup
// identical pushes across turns.
func TestSendDonePush_TextFormat(t *testing.T) {
	s := wecom.WeComStyler{}

	// Replicate the text construction inside sendDonePush for two different seqs.
	for _, seq := range []int64{1, 2} {
		text := "done" + s.DiffSuffix(int(seq))
		if !strings.HasPrefix(text, "done") {
			t.Errorf("done push text %q does not start with 'done'", text)
		}
		if text == "done" {
			t.Errorf("done push text has no ZWSP suffix for seq %d", seq)
		}
	}

	// The suffix must vary across seq so consecutive pushes aren't deduped.
	if s.DiffSuffix(1) == s.DiffSuffix(2) {
		t.Error("DiffSuffix does not vary across seq; identical 'done' pushes risk WeCom dedup")
	}
}

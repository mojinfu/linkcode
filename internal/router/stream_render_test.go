package router

import (
	"fmt"
	"strings"
	"testing"

	"linkcode/internal/channel/wecom"
)

func TestBarFormat_Thinking(t *testing.T) {
	s := wecom.WeComStyler{}
	bar := s.Bar("[⣷] thinking（18s）...")
	t.Logf("Bar thinking:\n%s", bar)
}

func TestBarFormat_Standby(t *testing.T) {
	s := wecom.WeComStyler{}
	bar := s.Bar("[✓] stand by")
	t.Logf("Bar standby:\n%s", bar)
}

func TestBarFormat_Interrupted(t *testing.T) {
	s := wecom.WeComStyler{}
	bar := s.Bar("[✗] interrupted, stand by")
	t.Logf("Bar interrupted:\n%s", bar)
}

func TestBarFormat_Error(t *testing.T) {
	s := wecom.WeComStyler{}
	bar := s.Bar("[💫] error")
	t.Logf("Bar error:\n%s", bar)
}

func TestBox_WithWarning(t *testing.T) {
	s := wecom.WeComStyler{}
	box := s.Box("[⣷] thinking（5m32s）...", "⚡ stream 28s 后断联，Agent 继续后台运行")
	t.Logf("Box warning:\n%s", box)
}

func TestFullStreamSimulation(t *testing.T) {
	s := wecom.WeComStyler{}

	t.Log("=== SIMULATED STREAM ===")

	f1 := s.Bar("[⣷] thinking（0s）...") + "\n"
	t.Logf("Frame 1 (initial):\n%s", f1)

	f2 := s.Bar("[⣷] thinking（12s）...") + "\n"
	t.Logf("Frame 2 (thinking):\n%s", f2)

	f3 := s.Bar("[⣷] thinking（24s）...") + "\nHello, this is Claude."
	t.Logf("Frame 3 (text arrives):\n%s", f3)

	f4 := s.Bar("[⣿] thinking（30s）...") + "\nHello, this is Claude.\nHere is more content."
	t.Logf("Frame 4 (more text):\n%s", f4)

	f5 := s.Bar("[✓] stand by") + "\n\nHello, this is Claude.\nHere is more content."
	t.Logf("Frame 5 (done):\n%s", f5)

	f6 := s.Box("[⣷] thinking（5m32s）...", "⚡ stream 28s 后断联，Agent 继续后台运行")
	t.Logf("Frame 6 (warning):\n%s", f6)
}

func TestSpinPrefixStability(t *testing.T) {
	spinnerRunes := []rune(spinnerFrames)
	prevThinkingPos := -1
	for i := 0; i < 8; i++ {
		prefix := spinPrefix(spinnerRunes, i, i, "")
		thinkingPos := strings.Index(prefix, "thinking")
		if prevThinkingPos >= 0 && thinkingPos != prevThinkingPos {
			t.Errorf("JITTER: thinking position shifted from %d to %d at frame %d", prevThinkingPos, thinkingPos, i)
		}
		prevThinkingPos = thinkingPos
		_ = fmt.Sprintf("%s", prefix)
	}
	if prevThinkingPos < 0 {
		t.Error("thinking not found in spinPrefix output")
	}
}

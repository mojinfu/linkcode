package router

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linkcode/internal/channel"
	"linkcode/internal/gateway"
	"linkcode/internal/pricing"
)

// TestCmdInvocation verifies the extension -> invocation mapping, including
// case-insensitivity and rejection of unsupported types.
func TestCmdInvocation(t *testing.T) {
	cases := []struct {
		path  string
		shell string
		ok    bool
	}{
		{`C:\scripts\backup.ps1`, "powershell", true},
		{`C:\scripts\RUN.PS1`, "powershell", true},
		{`C:\scripts\deploy.bat`, "cmd", true},
		{`C:\scripts\deploy.cmd`, "cmd", true},
		{`C:\tools\app.exe`, "", true},
		{`C:\tools\app.py`, "", false},
		{`C:\tools\notext`, "", false},
	}
	for _, tc := range cases {
		shell, args, ok := cmdInvocation(tc.path)
		if ok != tc.ok {
			t.Errorf("cmdInvocation(%q) ok = %v, want %v", tc.path, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if shell != tc.shell {
			t.Errorf("cmdInvocation(%q) shell = %q, want %q", tc.path, shell, tc.shell)
		}
		// For shell-based invocations the path must be in the args so the right
		// file runs. (exe runs directly; the caller promotes shell=="" to the path.)
		if tc.shell != "" && !strings.Contains(strings.Join(args, " "), tc.path) {
			t.Errorf("cmdInvocation(%q) args %v don't include the path", tc.path, args)
		}
	}
}

// TestBuildCmdResult verifies result formatting: error prefix, plain output,
// and truncation of overly long output.
func TestBuildCmdResult(t *testing.T) {
	if got := buildCmdResult(`C:\x.ps1`, []byte("hello\n"), nil); got != "hello\n" {
		t.Errorf("plain output = %q, want %q", got, "hello\n")
	}

	got := buildCmdResult(`C:\x.ps1`, []byte("boom"), errors.New("exit status 1"))
	if !strings.HasPrefix(got, "执行失败：exit status 1") || !strings.Contains(got, "boom") {
		t.Errorf("error result = %q, want 执行失败 prefix + output", got)
	}

	long := bytes.Repeat([]byte("a"), cmdOutputMaxLen+500)
	got = buildCmdResult(`C:\x.ps1`, long, nil)
	if len(got) > cmdOutputMaxLen+100 {
		t.Errorf("truncated result length = %d, want ~%d", len(got), cmdOutputMaxLen)
	}
	if !strings.Contains(got, "已截断") {
		t.Errorf("truncated result missing 已截断 note: %q", got)
	}
}

// TestHandleRunFile_Executes verifies /cmd actually runs the file, regardless of
// who sends it (no whitelist). The temp dir path contains a space, covering
// cmd /c quoting too; a marker file appearing proves the file ran.
func TestHandleRunFile_Executes(t *testing.T) {
	r := New(nil, nil, nil, gateway.New(nil), nil, nil, pricing.New(nil))

	dir := filepath.Join(t.TempDir(), "my dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(dir, "done.txt")
	bat := filepath.Join(dir, "run.bat")
	content := "@echo off\r\necho done > \"" + marker + "\"\r\n"
	if err := os.WriteFile(bat, []byte(content), 0o644); err != nil {
		t.Fatalf("write bat: %v", err)
	}

	r.handleRunFile(channel.Message{UserID: "anyone", BotID: "bot1", Content: "/cmd " + bat})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return // executed
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("/cmd did not execute the file")
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetJJRootDir_RetriesWithoutVmuxEnv(t *testing.T) {
	t.Setenv("VMUX", "true")
	t.Setenv("VMUX_TERMINAL_ID", "6")

	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "jj-calls.log")
	writeFakeJJ(t, binDir, callLog, `
if [ "$1" = "root" ]; then
  if [ -n "$VMUX" ] || [ -n "$VMUX_TERMINAL_ID" ]; then
    echo "fatal runtime error: assertion failed: output.write(&bytes).is_ok(), aborting" >&2
    exit 1
  fi
  echo "/tmp/repo"
  exit 0
fi
echo "unexpected args" >&2
exit 2
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	root, err := getJJRootDir(t.TempDir())
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if root != "/tmp/repo" {
		t.Fatalf("expected root /tmp/repo, got %q", root)
	}

	assertCallCount(t, callLog, 2)
}

func TestGetJJRootDir_NoVmux_DoesNotRetry(t *testing.T) {
	t.Setenv("VMUX", "")
	t.Setenv("VMUX_TERMINAL_ID", "")

	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "jj-calls.log")
	writeFakeJJ(t, binDir, callLog, `
if [ "$1" = "root" ]; then
  echo "/tmp/repo"
  exit 0
fi
echo "unexpected args" >&2
exit 2
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	root, err := getJJRootDir(t.TempDir())
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if root != "/tmp/repo" {
		t.Fatalf("expected root /tmp/repo, got %q", root)
	}

	assertCallCount(t, callLog, 1)
}

func writeFakeJJ(t *testing.T, dir string, callLogPath string, body string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"echo call >> " + shellQuote(callLogPath) + "\n" +
		strings.TrimSpace(body) + "\n"
	path := filepath.Join(dir, "jj")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write fake jj: %v", err)
	}
}

func assertCallCount(t *testing.T, logPath string, expected int) {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	count := strings.Count(strings.TrimSpace(string(content)), "call")
	if count != expected {
		t.Fatalf("expected %d jj calls, got %d", expected, count)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

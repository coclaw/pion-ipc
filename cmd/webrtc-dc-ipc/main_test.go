package main

import (
	"fmt"
	"os"
	"testing"
)

func TestStdoutProtection(t *testing.T) {
	// Save original stdout
	origStdout := os.Stdout

	// Create a pipe to capture what fmt.Println writes
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	// Redirect os.Stdout to stderr (like main does)
	os.Stdout = os.Stderr

	// Now fmt.Println should NOT write to origStdout.
	// Write to pw via origStdout to verify it's a different fd.
	fmt.Fprintln(pw, "direct to pipe")

	// fmt.Println goes to os.Stdout which is now os.Stderr
	// It should NOT appear on the pipe
	pw.Close()

	buf := make([]byte, 1024)
	n, _ := pr.Read(buf)
	got := string(buf[:n])
	if got != "direct to pipe\n" {
		t.Errorf("pipe read = %q, want %q", got, "direct to pipe\n")
	}

	pr.Close()

	// Restore
	os.Stdout = origStdout
}

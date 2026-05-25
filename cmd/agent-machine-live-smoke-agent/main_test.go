package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weskor/agent-machine/internal/livesmoke"
)

func TestWriteSmokeMarkerRefusesToOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.md")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeSmokeMarker(path, "CAG-1")

	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("writeSmokeMarker error = %v, want os.ErrExist", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original" {
		t.Fatalf("existing marker was overwritten: %q", data)
	}
}

func TestWriteSmokeMarkerCreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.md")

	if err := writeSmokeMarker(path, "CAG-1"); err != nil {
		t.Fatalf("writeSmokeMarker returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), livesmoke.SmokeMarkerContent("CAG-1", path)) {
		t.Fatalf("marker content = %q", data)
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEmbeddedFileIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chrome.png")

	changed, err := writeEmbeddedFileIfChanged(path, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected missing asset to be written")
	}

	changed, err = writeEmbeddedFileIfChanged(path, []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected identical asset to be reused")
	}

	changed, err = writeEmbeddedFileIfChanged(path, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed asset to be replaced")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("unexpected asset contents: %q", data)
	}
}

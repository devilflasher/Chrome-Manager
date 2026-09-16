package utils

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeneratedIconIsCurrent(t *testing.T) {
	tempDir := t.TempDir()
	iconPath := filepath.Join(tempDir, "1.ico")
	templatePath := filepath.Join(tempDir, "chrome.png")

	if err := os.WriteFile(iconPath, []byte("icon"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, []byte("template"), 0644); err != nil {
		t.Fatal(err)
	}

	base := time.Now().Add(-time.Hour)
	if err := os.Chtimes(iconPath, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(templatePath, base.Add(time.Minute), base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if generatedIconIsCurrent(iconPath, templatePath) {
		t.Fatal("expected icon older than template to be regenerated")
	}

	if err := os.Chtimes(iconPath, base.Add(2*time.Minute), base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !generatedIconIsCurrent(iconPath, templatePath) {
		t.Fatal("expected icon newer than template to be reused")
	}
}

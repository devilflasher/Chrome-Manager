package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateShortcutSupportsUnicodePaths(t *testing.T) {
	shortcutDir := filepath.Join(t.TempDir(), "中文路径")
	if err := os.MkdirAll(shortcutDir, 0755); err != nil {
		t.Fatalf("create shortcut directory: %v", err)
	}

	targetPath := filepath.Join(os.Getenv("WINDIR"), "System32", "notepad.exe")
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("test target is unavailable: %v", err)
	}

	shortcutPath := filepath.Join(shortcutDir, "测试快捷方式.lnk")
	if err := CreateShortcut(shortcutPath, targetPath, "", filepath.Dir(targetPath)); err != nil {
		t.Fatalf("CreateShortcut() failed for Unicode path: %v", err)
	}
	if _, err := os.Stat(shortcutPath); err != nil {
		t.Fatalf("shortcut was not created: %v", err)
	}
}

package main

import (
	"testing"

	"chromemanager/platform/common"
)

func TestResolveChromeWindowNumbersUsesLegacyFallbacks(t *testing.T) {
	windows := []common.WindowInfo{
		{UserDataDir: `D:\chrome duo\data\7`},
		{UserDataDir: `D:\profiles\legacy-wallet`},
		{UserDataDir: `D:\profiles\port-only`, DebugPort: 9225},
		{UserDataDir: `D:\profiles\unknown`},
		{UserDataDir: `D:\profiles\known`, Number: 4},
	}
	shortcuts := []common.ShortcutInfo{
		{Number: 12, Arguments: `--user-data-dir="D:\profiles\legacy-wallet" --disable-sync`},
	}

	resolved := resolveChromeWindowNumbers(windows, shortcuts)
	want := []int{7, 12, 3, 1001, 4}
	for i, expected := range want {
		if resolved[i].Number != expected {
			t.Fatalf("window %d: got number %d, want %d", i, resolved[i].Number, expected)
		}
	}
}

func TestResolveChromeWindowNumbersNeverReturnsZeroOrDuplicates(t *testing.T) {
	windows := []common.WindowInfo{
		{UserDataDir: `D:\profiles\first`, Number: 1001},
		{UserDataDir: `D:\profiles\second`},
		{UserDataDir: `D:\profiles\duplicate`, Number: 1001},
	}

	resolved := resolveChromeWindowNumbers(windows, nil)
	seen := make(map[int]bool)
	for i, window := range resolved {
		if window.Number <= 0 {
			t.Fatalf("window %d retained invalid number %d", i, window.Number)
		}
		if seen[window.Number] {
			t.Fatalf("window %d reused number %d", i, window.Number)
		}
		seen[window.Number] = true
	}
}

func TestExtractUserDataDirArgument(t *testing.T) {
	tests := map[string]string{
		`chrome.exe --user-data-dir="D:\profiles\with spaces" --remote-debugging-port=9223`: `D:\profiles\with spaces`,
		`chrome.exe --user-data-dir=D:\profiles\plain --disable-sync`:                       `D:\profiles\plain`,
		`chrome.exe --user-data-dir D:\profiles\separate`:                                   `D:\profiles\separate`,
	}

	for commandLine, expected := range tests {
		if actual := extractUserDataDirArgument(commandLine); actual != expected {
			t.Fatalf("extractUserDataDirArgument(%q) = %q, want %q", commandLine, actual, expected)
		}
	}
}

func TestResolveChromeWindowNumbersRejectsAmbiguousBasenameMatch(t *testing.T) {
	windows := []common.WindowInfo{{UserDataDir: `E:\moved\same-name`}}
	shortcuts := []common.ShortcutInfo{
		{Number: 8, Arguments: `--user-data-dir="D:\profiles\same-name"`},
		{Number: 9, Arguments: `--user-data-dir="D:\backup\same-name"`},
	}

	resolved := resolveChromeWindowNumbers(windows, shortcuts)
	if resolved[0].Number != temporaryWindowNumberStart {
		t.Fatalf("ambiguous basename resolved to %d, want temporary number %d", resolved[0].Number, temporaryWindowNumberStart)
	}
}

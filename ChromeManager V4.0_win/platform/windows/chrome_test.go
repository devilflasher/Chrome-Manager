//go:build windows

package windows

import "testing"

func TestParseChromeCmdLineUserDataDir(t *testing.T) {
	tests := []struct {
		name        string
		commandLine string
		wantDir     string
		wantPort    int
		wantNumber  int
	}{
		{
			name:        "quoted path with spaces",
			commandLine: `chrome.exe --user-data-dir="D:\chrome duo\data\8" --remote-debugging-port=9230`,
			wantDir:     `D:\chrome duo\data\8`,
			wantPort:    9230,
			wantNumber:  8,
		},
		{
			name:        "unquoted path",
			commandLine: `chrome.exe --user-data-dir=D:\profiles\9 --disable-sync`,
			wantDir:     `D:\profiles\9`,
			wantNumber:  9,
		},
		{
			name:        "space separated argument",
			commandLine: `chrome.exe --user-data-dir D:\profiles\chrome_env_11`,
			wantDir:     `D:\profiles\chrome_env_11`,
			wantNumber:  11,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, port, number := parseChromeCmdLine(test.commandLine)
			if dir != test.wantDir || port != test.wantPort || number != test.wantNumber {
				t.Fatalf("parseChromeCmdLine() = (%q, %d, %d), want (%q, %d, %d)", dir, port, number, test.wantDir, test.wantPort, test.wantNumber)
			}
		})
	}
}

package main

import "testing"

func TestNodeVersionOK(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"v20.18.1", false},
		{"v20.19.0", true},
		{"v21.7.3", false},
		{"v22.11.0", false},
		{"v22.12.0", true},
		{"v23.0.0", true},
	}

	for _, tt := range tests {
		if got := nodeVersionOK(tt.version); got != tt.want {
			t.Fatalf("nodeVersionOK(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

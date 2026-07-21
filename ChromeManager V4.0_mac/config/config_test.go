package config

import "testing"

func TestIsDarwinAppBundle(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/Applications/ChromeManager.app/Contents/MacOS/ChromeManager", true},
		{"/private/var/folders/x/AppTranslocation/id/d/ChromeManager.app/Contents/MacOS/ChromeManager", true},
		{"/var/folders/x/go-build123/exe/ChromeManager", false},
	}

	for _, test := range tests {
		if got := isDarwinAppBundle(test.path); got != test.want {
			t.Errorf("isDarwinAppBundle(%q) = %t, want %t", test.path, got, test.want)
		}
	}
}

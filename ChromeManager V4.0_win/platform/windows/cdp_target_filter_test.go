package windows

import "testing"

func TestIsPrerenderTarget(t *testing.T) {
	tests := []struct {
		name       string
		targetType string
		subtype    string
		want       bool
	}{
		{name: "omnibox prerender", targetType: "page", subtype: "prerender", want: true},
		{name: "normal page", targetType: "page", subtype: "", want: false},
		{name: "popup", targetType: "popup", subtype: "prerender", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPrerenderTarget(tt.targetType, tt.subtype); got != tt.want {
				t.Fatalf("isPrerenderTarget(%q, %q) = %v, want %v", tt.targetType, tt.subtype, got, tt.want)
			}
		})
	}
}

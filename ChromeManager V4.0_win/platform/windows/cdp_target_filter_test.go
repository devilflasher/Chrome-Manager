package windows

import (
	"testing"
	"time"
)

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

func TestTransientPagePromotionDoesNotCreateTab(t *testing.T) {
	c := newCDPClient(0)
	c.initialized = true
	created := make(chan string, 1)
	c.onTargetCreated = func(targetID, _ string) { created <- targetID }

	c.handleMessage(targetLifecycleMessage("Target.targetCreated", map[string]interface{}{
		"targetId": "omnibox-transient",
		"type":     "other",
		"url":      "",
	}))
	c.handleMessage(targetLifecycleMessage("Target.targetInfoChanged", map[string]interface{}{
		"targetId": "omnibox-transient",
		"type":     "page",
		"url":      "",
	}))
	c.handleMessage(map[string]interface{}{
		"method": "Target.targetDestroyed",
		"params": map[string]interface{}{"targetId": "omnibox-transient"},
	})

	select {
	case targetID := <-created:
		t.Fatalf("transient omnibox target unexpectedly created a tab: %s", targetID)
	case <-time.After(transientPagePromotionGracePeriod + 100*time.Millisecond):
	}
}

func TestPersistentPagePromotionStillCreatesTab(t *testing.T) {
	c := newCDPClient(0)
	c.initialized = true
	created := make(chan string, 1)
	c.onTargetCreated = func(targetID, _ string) { created <- targetID }

	c.handleMessage(targetLifecycleMessage("Target.targetCreated", map[string]interface{}{
		"targetId": "persistent-page",
		"type":     "other",
		"url":      "",
	}))
	c.handleMessage(targetLifecycleMessage("Target.targetInfoChanged", map[string]interface{}{
		"targetId": "persistent-page",
		"type":     "page",
		"url":      "",
	}))

	select {
	case targetID := <-created:
		if targetID != "persistent-page" {
			t.Fatalf("created target = %q, want persistent-page", targetID)
		}
	case <-time.After(time.Second):
		t.Fatal("persistent page promotion did not create a tab")
	}
}

func targetLifecycleMessage(method string, targetInfo map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"method": method,
		"params": map[string]interface{}{"targetInfo": targetInfo},
	}
}

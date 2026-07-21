//go:build darwin

package darwin

import (
	"chromemanager/platform/common"
	"reflect"
	"testing"
)

func TestButtonTransitionsUseReliableDelivery(t *testing.T) {
	for _, evtType := range []int{1, 2, 3, 4} {
		if !isReliableNativeMouseEvent(evtType) {
			t.Fatalf("event type %d must use reliable delivery", evtType)
		}
	}
	for _, evtType := range []int{5, 6, 7, 22} {
		if isReliableNativeMouseEvent(evtType) {
			t.Fatalf("event type %d should not block the native callback", evtType)
		}
	}
}

func TestPageDOMClickRouteIsDeterministic(t *testing.T) {
	if !usesPageDOMClickRoute("https://example.com", 120, 90) {
		t.Fatal("HTTP page viewport click must use the DOM route")
	}
	if usesPageDOMClickRoute("https://example.com", 80, 90) {
		t.Fatal("Chrome toolbar click must not use the page DOM route")
	}
	if usesPageDOMClickRoute("chrome://newtab/", 120, 90) {
		t.Fatal("Chrome internal page must retain its native route")
	}
}

func TestChromeTopUICoordinatesAreNotReplayed(t *testing.T) {
	if !isChromeTopUIEvent(80, 90) {
		t.Fatal("Chrome toolbar click must stay local to the master")
	}
	if isChromeTopUIEvent(120, 90) {
		t.Fatal("page viewport click must remain eligible for synchronization")
	}
}

func TestMasterDOMClickSourceRejectsChromeInternalPages(t *testing.T) {
	for _, targetURL := range []string{
		"chrome://extensions/",
		"chrome://settings/",
		"chrome://newtab/",
	} {
		if acceptsMasterDOMClickSourceURL(targetURL) {
			t.Fatalf("internal page %q must not replay a stale DOM click", targetURL)
		}
	}
	for _, targetURL := range []string{
		"https://example.com/",
		"http://127.0.0.1:8080/",
		"chrome-extension://example/popup.html",
	} {
		if !acceptsMasterDOMClickSourceURL(targetURL) {
			t.Fatalf("supported page %q must retain DOM click routing", targetURL)
		}
	}
}

func TestPreferredTargetWinsBeforeChromeVisibilitySettles(t *testing.T) {
	tabs := []browserTabDescriptor{
		{TargetID: "old-target", URL: "https://example.com", Active: true},
		{TargetID: "new-target", URL: "chrome://newtab/", Active: false},
	}

	selected, err := selectActiveBrowserTab(tabs, "new-target")
	if err != nil {
		t.Fatal(err)
	}
	if selected.TargetID != "new-target" {
		t.Fatalf("selected target = %q, want new-target", selected.TargetID)
	}
	if _, err := selectActiveBrowserTab(tabs, "missing-target"); err == nil {
		t.Fatal("missing preferred target must return an error")
	}
}

func TestExtensionPageDismissalOnlyUsesViewportLeftMouseDown(t *testing.T) {
	const extensionURL = "chrome-extension://example/popup.html"
	if got := extensionIDForPageDismissal(1, extensionURL, 120, 90); got != "example" {
		t.Fatalf("extensionIDForPageDismissal() = %q, want example", got)
	}
	for _, test := range []struct {
		name    string
		evtType int
		y       float64
		url     string
	}{
		{name: "mouse up", evtType: 2, y: 120, url: extensionURL},
		{name: "toolbar", evtType: 1, y: 80, url: extensionURL},
		{name: "no extension", evtType: 1, y: 120, url: "https://example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := extensionIDForPageDismissal(test.evtType, test.url, test.y, 90); got != "" {
				t.Fatalf("extensionIDForPageDismissal() = %q, want empty", got)
			}
		})
	}
}

func TestProgrammaticMasterTargetSuppressionEndsOnLoad(t *testing.T) {
	sm := &SyncManager{}
	sm.BeginProgrammaticMasterTarget("https://example.com")
	if !sm.shouldSuppressProgrammaticMasterTarget("", "https://example.com/") {
		t.Fatal("programmatic navigation without a target ID was not suppressed by URL")
	}
	if !sm.shouldSuppressProgrammaticMasterTarget("target-1", "https://example.com/") {
		t.Fatal("programmatic target was not bound by URL")
	}
	if !sm.shouldSuppressProgrammaticMasterTarget("target-1", "https://redirect.example/path") {
		t.Fatal("bound programmatic target was not suppressed after redirect")
	}
	if sm.shouldSuppressProgrammaticMasterTarget("target-2", "https://example.com/") {
		t.Fatal("unrelated target was suppressed")
	}
	sm.finishProgrammaticMasterTarget("target-1", "https://redirect.example/path")
	if sm.shouldSuppressProgrammaticMasterTarget("target-1", "https://redirect.example/path") {
		t.Fatal("programmatic target suppression survived load completion")
	}
}

func TestSameNativeScrollRoute(t *testing.T) {
	base := NativeSyncEvent{
		Type:                  "native_mouse",
		Value:                 22,
		ExtensionURL:          "chrome-extension://example/popup.html",
		ExtensionWindowRoute:  true,
		ExtensionContentRoute: false,
	}
	same := base
	same.X = 20
	same.Y = 30
	if !sameNativeScrollRoute(base, same) {
		t.Fatal("coordinate changes within one extension route should coalesce")
	}

	different := same
	different.ExtensionURL = ""
	if sameNativeScrollRoute(base, different) {
		t.Fatal("extension and ordinary page scroll routes must stay separate")
	}

	differentTarget := same
	differentTarget.MasterTargetID = "another-target"
	if sameNativeScrollRoute(base, differentTarget) {
		t.Fatal("scroll events from different master targets must not coalesce")
	}

}

func TestMappedSessionForEventUsesExplicitTargetMap(t *testing.T) {
	const slavePID = 202
	slave := &CDPClient{
		targetSessions: map[string]string{"slave-target": "slave-session"},
	}
	sm := &SyncManager{
		masterWindow: 101,
		targetMap: map[string]map[int]string{
			"master-target": {slavePID: "slave-target"},
		},
	}

	msg := NativeSyncEvent{MasterTargetID: "master-target", MasterSessionID: "master-session"}
	if got := sm.mappedSessionForEvent(slavePID, slave, msg); got != "slave-session" {
		t.Fatalf("mappedSessionForEvent() = %q, want slave-session", got)
	}
	msg.MasterTargetID = "unmapped-target"
	if got := sm.mappedSessionForEvent(slavePID, slave, msg); got != "" {
		t.Fatalf("unmapped event guessed session %q", got)
	}
}

func TestPageViewportMappingUsesMappedSessionMetrics(t *testing.T) {
	const (
		masterPID = 101
		slavePID  = 202
	)
	master := &CDPClient{
		viewportMetricsBySession: map[string]CDPViewportMetrics{
			"master-session": {InnerWidth: 1000, InnerHeight: 626},
		},
	}
	slave := &CDPClient{
		viewportMetricsBySession: map[string]CDPViewportMetrics{
			"visible-session": {InnerWidth: 1000, InnerHeight: 626},
			"hidden-session":  {InnerWidth: 555, InnerHeight: 348},
		},
	}
	sm := &SyncManager{
		masterWindow: masterPID,
		masterCDP:    master,
		rectCache: map[int]common.Rect{
			masterPID: {Width: 1000, Height: 717},
			slavePID:  {Width: 1000, Height: 717},
		},
		uiOffsets: map[int]float64{masterPID: 91, slavePID: 91},
		// Reproduce the stale PID-level cache that previously came from the hidden,
		// zoomed duplicate tab. Page routing must ignore it.
		viewportMetrics: map[int]viewportMetricsSnapshot{
			slavePID: {Width: 555, Height: 348},
		},
	}

	x, y, ok := sm.mapPageMouseToViewport(
		slavePID,
		slave,
		"master-session",
		"visible-session",
		500,
		617,
		false,
	)
	if !ok {
		t.Fatal("mapped page viewport point was rejected")
	}
	if x < 499.9 || x > 500.1 || y < 525.9 || y > 526.1 {
		t.Fatalf("mapped point = (%.1f, %.1f), want (500.0, 526.0)", x, y)
	}
}

func TestViewportClickDeduplicationIsTargetSpecific(t *testing.T) {
	sm := &SyncManager{}
	sm.touchViewportClick("target-a", false)
	if sm.hasRecentReplayedViewportClick("target-a") {
		t.Fatal("DOM-routed click was considered replayed before its DOM action")
	}
	sm.markViewportClickReplayed("target-a")
	if !sm.hasRecentReplayedViewportClick("target-a") {
		t.Fatal("replayed click was not associated with its source target")
	}
	if sm.hasRecentReplayedViewportClick("target-b") {
		t.Fatal("click state leaked to another target")
	}
}

func TestPopupPlanRaisesMasterLast(t *testing.T) {
	owners := []macPopupArrangeOwner{
		{pid: 1, number: 1, order: 0, top: 0, popups: []common.WindowInfo{{HWND: 101}}},
		{pid: 2, number: 2, order: 1, top: 0, popups: []common.WindowInfo{{HWND: 102}}},
		{pid: 3, number: 3, order: 2, top: 400, popups: []common.WindowInfo{{HWND: 103}}},
	}
	sortExtensionPopupOwners(owners)
	plan, _ := extensionPopupPlan(owners)
	if len(plan) != 3 || plan[0] != 103 || plan[1] != 102 || plan[2] != 101 {
		t.Fatalf("unexpected popup raise order: %v", plan)
	}
}

func TestFocusedPopupDuplicateUsesGeometry(t *testing.T) {
	listed := common.WindowInfo{
		HWND:     101,
		Position: common.Rect{Left: 100, Top: 80, Width: 360, Height: 600},
	}
	focused := common.WindowInfo{
		HWND:     202,
		Position: common.Rect{Left: 102, Top: 82, Width: 358, Height: 598},
	}
	if !sameEnumeratedWindow(listed, focused) {
		t.Fatal("separate AX references for the same popup should deduplicate by geometry")
	}

}

func TestExtensionMouseTargetUsesVisibleTopLevelPage(t *testing.T) {
	areaURL := "chrome-extension://okx/ses.html#/settings"
	visibleURL := "chrome-extension://okx/popup.html#/settings"
	activeURL := "chrome-extension://okx/popup.html#/"
	if got := extensionMouseTargetURL(areaURL, visibleURL, activeURL); got != visibleURL {
		t.Fatalf("expected visible popup target, got %q", got)
	}
}

func TestExtensionMouseTargetDoesNotCrossExtensions(t *testing.T) {
	areaURL := "chrome-extension://okx/ses.html#/settings"
	visibleURL := "chrome-extension://phantom/popup.html"
	if got := extensionMouseTargetURL(areaURL, visibleURL, ""); got != areaURL {
		t.Fatalf("expected original area target, got %q", got)
	}
}

func TestRequiresNativeChromeZoomMenu(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"chrome://newtab/", true},
		{"chrome-untrusted://new-tab-page/", true},
		{"devtools://devtools/bundled/", true},
		{"https://example.com/", false},
		{"chrome-extension://example/popup.html", false},
	}
	for _, test := range tests {
		if got := requiresNativeChromeZoomMenu(test.url); got != test.want {
			t.Fatalf("requiresNativeChromeZoomMenu(%q) = %v, want %v", test.url, got, test.want)
		}
	}
}

func TestNativeChromeZoomMenuActions(t *testing.T) {
	tests := []struct {
		name    string
		current float64
		target  float64
		want    []int
	}{
		{"already aligned", 0.9, 0.9, nil},
		{"zoom out twice", 1.0, 0.8, []int{-1, -1}},
		{"zoom in across reset", 0.8, 1.1, []int{1, 1, 1}},
		{"actual size", 0.5, 1.0, []int{0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := nativeChromeZoomMenuActions(test.current, test.target)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("nativeChromeZoomMenuActions(%v, %v) = %v, want %v", test.current, test.target, got, test.want)
			}
		})
	}
}

func TestChromeNativeZoomDirection(t *testing.T) {
	tests := []struct {
		key       string
		direction int
		ok        bool
	}{
		{"+", 1, true},
		{"=", 1, true},
		{"-", -1, true},
		{"0", 0, true},
		{"a", 0, false},
	}
	for _, test := range tests {
		direction, ok := chromeNativeZoomDirection(test.key)
		if direction != test.direction || ok != test.ok {
			t.Fatalf("chromeNativeZoomDirection(%q) = (%d, %v), want (%d, %v)", test.key, direction, ok, test.direction, test.ok)
		}
	}
}

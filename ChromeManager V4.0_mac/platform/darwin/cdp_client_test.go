package darwin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFrameNavigationAfterTargetInfoChangeIsNotReload(t *testing.T) {
	c := NewCDPClient(0)
	c.isInitialized = true
	c.sessionURLs["session"] = "https://www.google.com/"
	c.sessionCommittedURLs["session"] = "https://www.google.com/"
	c.sessionTargets["session"] = "target"
	c.sessionTypes["session"] = "page"
	c.sessionLoaderIDs["session"] = "loader-home"
	c.targetSessions["target"] = "session"
	c.targets["target"] = "https://www.google.com/"
	c.knownPageTargets["target"] = true

	reloaded := make(chan string, 1)
	c.OnURLReloaded = func(_ string, url string) {
		reloaded <- url
	}

	searchURL := "https://www.google.com/search?q=chromemanager"
	c.handleMessage(map[string]interface{}{
		"method": "Target.targetInfoChanged",
		"params": map[string]interface{}{
			"targetInfo": map[string]interface{}{
				"targetId": "target",
				"type":     "page",
				"url":      searchURL,
			},
		},
	})
	c.handleMessage(map[string]interface{}{
		"method":    "Page.frameNavigated",
		"sessionId": "session",
		"params": map[string]interface{}{
			"frame": map[string]interface{}{
				"id":       "target",
				"url":      searchURL,
				"loaderId": "loader-search",
			},
		},
	})

	select {
	case url := <-reloaded:
		t.Fatalf("ordinary navigation was reported as reload: %s", url)
	case <-time.After(50 * time.Millisecond):
	}
	if got := c.sessionCommittedURLs["session"]; got != searchURL {
		t.Fatalf("committed URL = %q, want %q", got, searchURL)
	}

	c.handleMessage(map[string]interface{}{
		"method":    "Page.frameNavigated",
		"sessionId": "session",
		"params": map[string]interface{}{
			"frame": map[string]interface{}{
				"id":       "target",
				"url":      searchURL,
				"loaderId": "loader-reload",
			},
		},
	})

	select {
	case url := <-reloaded:
		if url != searchURL {
			t.Fatalf("reload URL = %q, want %q", url, searchURL)
		}
	case <-time.After(time.Second):
		t.Fatal("real reload was not reported")
	}
}

func TestInstalledExtensionDetectionDoesNotRequireRuntimeTarget(t *testing.T) {
	userDataDir := t.TempDir()
	client := NewCDPClient(9222)
	client.setUserDataDir(userDataDir)
	const extensionID = "bfnaelmomeimhlpmgjnjophhpkkoljpa"

	if client.hasInstalledExtensionOnDisk(extensionID) {
		t.Fatal("extension must not be detected before its install directory exists")
	}
	if err := os.MkdirAll(filepath.Join(userDataDir, "Default", "Extensions", extensionID), 0o755); err != nil {
		t.Fatal(err)
	}
	if !client.hasInstalledExtensionOnDisk(extensionID) {
		t.Fatal("installed extension directory must be detected while its service worker is dormant")
	}
}

func TestTabVisibilityBindingUsesPageLifecycle(t *testing.T) {
	for _, token := range []string{"document.visibilityState", "visibilitychange", "pageshow"} {
		if !strings.Contains(tabVisibilityScript, token) {
			t.Fatalf("tab visibility binding missing %q", token)
		}
	}
	for _, forbidden := range []string{"chrome.tabs", "chrome.debugger", "setTimeout", "setInterval"} {
		if strings.Contains(tabVisibilityScript, forbidden) {
			t.Fatalf("tab visibility binding must not depend on %q", forbidden)
		}
	}
}

func TestExtensionChildTargetHost(t *testing.T) {
	if !isExtensionChildTargetHost("page", "chrome-extension://example/popup.html") {
		t.Fatal("visible extension page must auto-attach child targets")
	}
	if isExtensionChildTargetHost("iframe", "chrome-extension://example/ses.html") {
		t.Fatal("extension iframe must not recursively auto-attach children")
	}
	if isExtensionChildTargetHost("page", "https://example.com") {
		t.Fatal("normal web page must not enable extension child auto-attach")
	}
}

func TestExtensionSecureInputScriptEmitsEditOperations(t *testing.T) {
	for _, token := range []string{`inputType:`, `data:`, `emit("edit"`} {
		if !strings.Contains(extensionSecureInputScript, token) {
			t.Fatalf("secure input script missing %q", token)
		}
	}
	if strings.Contains(extensionSecureInputScript, `emit("value"`) {
		t.Fatal("secure input script must emit input operations, not full-value updates")
	}
}

func TestExtensionInputFrameIDsIncludesNestedFrames(t *testing.T) {
	result := map[string]interface{}{
		"frameTree": map[string]interface{}{
			"frame": map[string]interface{}{"id": "top"},
			"childFrames": []interface{}{
				map[string]interface{}{"frame": map[string]interface{}{"id": "child"}},
			},
		},
	}
	frameIDs := extensionInputFrameIDs(result)
	if len(frameIDs) != 2 || frameIDs[0] != "top" || frameIDs[1] != "child" {
		t.Fatalf("extensionInputFrameIDs() = %v, want [top child]", frameIDs)
	}
}

func TestExtensionInputEvaluationErrorDetectsJavaScriptException(t *testing.T) {
	if err := extensionInputEvaluationError(map[string]interface{}{"result": map[string]interface{}{"value": true}}); err != nil {
		t.Fatalf("successful evaluation returned error: %v", err)
	}
	if err := extensionInputEvaluationError(map[string]interface{}{"exceptionDetails": map[string]interface{}{"text": "blocked"}}); err == nil {
		t.Fatal("javascript exception was not detected")
	}
}

func TestTargetInfoChangedDoesNotReannounceUnchangedVisibleExtension(t *testing.T) {
	const metamaskSidePanel = "chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/sidepanel.html#/unlock"
	if shouldNotifyTargetInfoChanged(true, "page", metamaskSidePanel, metamaskSidePanel) {
		t.Fatal("unchanged visible extension target must not consume a later toolbar action trigger")
	}
	if !shouldNotifyTargetInfoChanged(false, "page", "", "chrome-extension://bfnaelmomeimhlpmgjnjophhpkkoljpa/popup.html") {
		t.Fatal("new Phantom popup target must be announced")
	}
	if !shouldNotifyTargetInfoChanged(true, "page", "about:blank", "chrome-extension://example/popup.html") {
		t.Fatal("target becoming a visible extension popup must be announced")
	}
	if !shouldNotifyTargetInfoChanged(true, "page", "", "chrome://newtab/") {
		t.Fatal("blank target becoming a real new tab must be announced")
	}
}

func TestBlankTargetIsNotAnnouncedAsNewTab(t *testing.T) {
	if shouldNotifyTargetCreated("page", "") || shouldNotifyTargetCreated("page", "about:blank") {
		t.Fatal("transient blank targets must not be broadcast to slave windows")
	}
	if !shouldNotifyTargetCreated("page", "chrome://newtab/") {
		t.Fatal("a concrete Chrome new tab must still be announced")
	}
}

func TestIsChromeNewTabURL(t *testing.T) {
	for _, targetURL := range []string{
		"chrome://newtab",
		"chrome://newtab/",
		"chrome://new-tab-page",
		"chrome://new-tab-page/",
	} {
		if !isChromeNewTabURL(targetURL) {
			t.Fatalf("expected %q to be recognized as Chrome New Tab", targetURL)
		}
	}

	for _, targetURL := range []string{
		"https://www.google.com/",
		"chrome://settings/",
		"about:blank",
	} {
		if isChromeNewTabURL(targetURL) {
			t.Fatalf("did not expect %q to be recognized as Chrome New Tab", targetURL)
		}
	}
}

func TestWebAndExtensionSurfacesUseDOMClickRouting(t *testing.T) {
	if !isComparablePageTarget("page", "chrome://newtab/") {
		t.Fatal("Chrome New Tab must participate in explicit target mapping")
	}
	for _, targetURL := range []string{
		"https://example.com/",
		"http://127.0.0.1:8080/",
	} {
		if !supportsDOMClickBinding("page", targetURL) {
			t.Fatalf("ordinary web target %q must use deterministic DOM click routing", targetURL)
		}
	}
	for _, targetURL := range []string{
		"chrome://newtab/",
		"chrome://new-tab-page/",
		"chrome://settings/",
		"chrome-untrusted://new-tab-page/",
	} {
		if supportsDOMClickBinding("page", targetURL) {
			t.Fatalf("browser-internal target %q must use the viewport-coordinate route", targetURL)
		}
	}
	if !supportsDOMClickBinding("page", "chrome-extension://example/popup.html") {
		t.Fatal("visible extension pages must retain deterministic DOM click routing")
	}
	if !strings.Contains(domClickCaptureScript, "data-test-id") || !strings.Contains(domClickCaptureScript, "composedPath") {
		t.Fatal("DOM click capture must retain complex control and composed-tree support")
	}
	if strings.Contains(tabVisibilityScript, "click") || strings.Contains(tabVisibilityScript, "pointer") {
		t.Fatal("tab visibility binding must not intercept New Tab input")
	}
}

func TestSelectTabTargetForActivationPrefersActiveDuplicateURL(t *testing.T) {
	targets := []interface{}{
		map[string]interface{}{
			"type": "tab", "targetId": "inactive", "url": "chrome://newtab/",
			"embedderData": map[string]interface{}{"tabActive": false},
		},
		map[string]interface{}{
			"type": "tab", "targetId": "active", "url": "chrome://newtab/",
			"embedderData": map[string]interface{}{"tabActive": true},
		},
	}
	if got := selectTabTargetForActivation(targets, "chrome://newtab/"); got != "active" {
		t.Fatalf("selected target %q, want active", got)
	}
}

func TestTabVisibilityBindingOnlyEmitsVisibleDocuments(t *testing.T) {
	if !strings.Contains(tabVisibilityScript, `document.visibilityState !== "visible"`) {
		t.Fatal("tab visibility binding must ignore background documents")
	}
	if !strings.Contains(tabVisibilityScript, "chromeManagerTabVisibility") {
		t.Fatal("tab visibility binding must report through the CDP session binding")
	}
}

func TestDOMClickCaptureUsesDeterministicSemanticResolution(t *testing.T) {
	for _, token := range []string{"event.isTrusted", "composedPath", " >>> ", `kind: "new_tab"`, "semanticSelector", "semanticIndex", "semanticText", `document.addEventListener("pointerdown"`, "path.find(explicitInteractive)"} {
		if !strings.Contains(domClickCaptureScript, token) {
			t.Fatalf("DOM click capture missing %q", token)
		}
	}
	if strings.Contains(domClickCaptureScript, `document.addEventListener("click", handler`) {
		t.Fatal("DOM click capture must run before controls can remount during pointerdown")
	}
	stableAttribute := strings.Index(domClickCaptureScript, `for (const name of ["data-testid"`)
	stableID := strings.Index(domClickCaptureScript, "if (stableID(element.id))")
	if stableAttribute < 0 || stableID < 0 || stableAttribute > stableID {
		t.Fatal("DOM click capture must prefer stable semantic attributes over framework-generated IDs")
	}
	for _, forbidden := range []string{"setTimeout", "setInterval", "new Promise"} {
		if strings.Contains(domClickCaptureScript, forbidden) {
			t.Fatalf("DOM click capture must not poll or retry via %q", forbidden)
		}
	}
}

func TestDOMClickActionCarriesDuplicateSemanticIndex(t *testing.T) {
	payload := []byte(`{"kind":"click","selector":"button:nth-of-type(2)","semanticSelector":"button[aria-label=\"Select ETH\"]","semanticIndex":1,"semanticText":""}`)
	action := DOMClickAction{}
	if err := json.Unmarshal(payload, &action); err != nil {
		t.Fatal(err)
	}
	if action.SemanticSelector != `button[aria-label="Select ETH"]` || action.SemanticIndex != 1 {
		t.Fatalf("unexpected semantic action: %+v", action)
	}
}

func TestExtensionEditInputTypes(t *testing.T) {
	for _, inputType := range []string{"insertText", "insertCompositionText", "deleteContentBackward", "deleteContentForward"} {
		if !isExtensionEditInputType(inputType) {
			t.Fatalf("input type %q must use full-value synchronization", inputType)
		}
	}
	if isExtensionEditInputType("historyUndo") {
		t.Fatal("historyUndo must not be treated as a direct edit")
	}
}

func TestApplyExtensionEditUsesFullValueSynchronization(t *testing.T) {
	c := &CDPClient{}
	tests := []struct {
		name  string
		state extensionEditableState
		want  string
	}{
		{name: "insert", state: extensionEditableState{InputType: "insertText", InputData: "x", Value: "x"}, want: "not connected"},
		{name: "empty insert", state: extensionEditableState{InputType: "insertText", Value: ""}, want: "not connected"},
		{name: "backspace", state: extensionEditableState{InputType: "deleteContentBackward", Value: "ab"}, want: "not connected"},
		{name: "delete", state: extensionEditableState{InputType: "deleteContentForward", Value: "ab"}, want: "not connected"},
		{name: "unsupported", state: extensionEditableState{InputType: "historyUndo"}, want: "unsupported extension input type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.ApplyExtensionEditToSession("session", tt.state)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ApplyExtensionEditToSession() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestActiveSessionRejectsExtensionPopupAndFrame(t *testing.T) {
	c := &CDPClient{
		activeSessionID:  "web-session",
		activeSessionURL: "https://example.com",
		sessionURLs: map[string]string{
			"web-session":   "https://example.com",
			"popup-session": "chrome-extension://example/popup.html",
			"frame-session": "chrome-extension://example/ses.html",
			"page-session":  "chrome-extension://example/home.html#/settings",
		},
		sessionTypes: map[string]string{
			"web-session":   "page",
			"popup-session": "page",
			"frame-session": "iframe",
			"page-session":  "page",
		},
	}

	c.setActiveSession("popup-session")
	c.setActiveSession("frame-session")
	if c.activeSessionID != "web-session" {
		t.Fatalf("extension popup/frame replaced active web session: %q", c.activeSessionID)
	}

	c.setActiveSession("page-session")
	if c.activeSessionID != "page-session" {
		t.Fatalf("full-page extension was not activated: %q", c.activeSessionID)
	}
	if got := c.cachedActiveExtensionURL(); got != "chrome-extension://example/home.html#/settings" {
		t.Fatalf("cachedActiveExtensionURL() = %q", got)
	}
}

func TestPageDOMClickPromotesNewTabSession(t *testing.T) {
	c := &CDPClient{
		activeSessionID:  "old-session",
		activeSessionURL: "https://www.baidu.com/",
		sessionURLs: map[string]string{
			"old-session": "https://www.baidu.com/",
			"new-session": "https://www.baidu.com/more/",
		},
		sessionTargets: map[string]string{
			"old-session": "old-target",
			"new-session": "new-target",
		},
		sessionTypes: map[string]string{
			"old-session": "page",
			"new-session": "page",
		},
	}

	targetID, sessionID := c.pageRouteForDOMClick("new-session", "https://www.baidu.com/more/")
	if targetID != "new-target" || sessionID != "new-session" {
		t.Fatalf("pageRouteForDOMClick() = (%q, %q), want new target/session", targetID, sessionID)
	}
	if c.activeSessionID != "new-session" || c.activeSessionURL != "https://www.baidu.com/more/" {
		t.Fatalf("new tab was not promoted: session=%q url=%q", c.activeSessionID, c.activeSessionURL)
	}
}

func TestPageDOMClickRejectsMismatchedSessionURL(t *testing.T) {
	c := &CDPClient{
		activeSessionID:  "old-session",
		activeSessionURL: "https://www.baidu.com/",
		sessionURLs: map[string]string{
			"old-session": "https://www.baidu.com/",
			"new-session": "https://www.baidu.com/more/",
		},
		sessionTargets: map[string]string{"new-session": "new-target"},
		sessionTypes:   map[string]string{"new-session": "page"},
	}

	targetID, sessionID := c.pageRouteForDOMClick("new-session", "https://example.com/")
	if targetID != "" || sessionID != "" {
		t.Fatalf("pageRouteForDOMClick() accepted mismatched URL: (%q, %q)", targetID, sessionID)
	}
	if c.activeSessionID != "old-session" || c.activeSessionURL != "https://www.baidu.com/" {
		t.Fatalf("mismatched callback changed active route: session=%q url=%q", c.activeSessionID, c.activeSessionURL)
	}
}

func TestExtensionRoutePrefersActiveFullPageOverStalePopup(t *testing.T) {
	c := &CDPClient{
		activeSessionID:              "full-session",
		lastVisibleExtensionTargetID: "popup-target",
		lastVisibleExtensionURL:      "chrome-extension://example/popup.html",
		sessionURLs: map[string]string{
			"full-session":  "chrome-extension://example/home.html#/settings",
			"popup-session": "chrome-extension://example/popup.html",
		},
		sessionTargets: map[string]string{
			"full-session":  "full-target",
			"popup-session": "popup-target",
		},
		sessionTypes: map[string]string{
			"full-session":  "page",
			"popup-session": "page",
		},
		targetSessions: map[string]string{
			"full-target":  "full-session",
			"popup-target": "popup-session",
		},
	}

	targetID, sessionID := c.extensionRouteSnapshot("chrome-extension://example/home.html#/settings")
	if targetID != "full-target" || sessionID != "full-session" {
		t.Fatalf("extension route = (%q, %q), want active full page", targetID, sessionID)
	}
}

func TestExtensionRoutePrefersVisiblePopupForPopupPage(t *testing.T) {
	c := &CDPClient{
		activeSessionID:              "full-session",
		lastVisibleExtensionTargetID: "popup-target",
		lastVisibleExtensionURL:      "chrome-extension://example/popup.html",
		sessionURLs: map[string]string{
			"full-session":  "chrome-extension://example/home.html#/settings",
			"popup-session": "chrome-extension://example/popup.html",
		},
		sessionTargets: map[string]string{
			"full-session":  "full-target",
			"popup-session": "popup-target",
		},
		sessionTypes: map[string]string{
			"full-session":  "page",
			"popup-session": "page",
		},
		targetSessions: map[string]string{
			"full-target":  "full-session",
			"popup-target": "popup-session",
		},
	}

	targetID, sessionID := c.extensionRouteSnapshot("chrome-extension://example/popup.html")
	if targetID != "popup-target" || sessionID != "popup-session" {
		t.Fatalf("extension route = (%q, %q), want visible popup", targetID, sessionID)
	}
}

func TestCachedActiveExtensionURLRejectsFrame(t *testing.T) {
	c := &CDPClient{
		activeSessionID: "frame-session",
		sessionURLs: map[string]string{
			"frame-session": "chrome-extension://example/ses.html",
		},
		sessionTypes: map[string]string{
			"frame-session": "iframe",
		},
	}

	if got := c.cachedActiveExtensionURL(); got != "" {
		t.Fatalf("cachedActiveExtensionURL() = %q, want empty for iframe", got)
	}
}

func TestDestroyedExtensionSessionRestoresLastActivePage(t *testing.T) {
	c := &CDPClient{
		sessions: map[string]bool{
			"web-session":       true,
			"extension-session": true,
		},
		sessionURLs: map[string]string{
			"web-session":       "https://example.com",
			"extension-session": "chrome-extension://example/home.html",
		},
		sessionTargets: map[string]string{
			"web-session":       "web-target",
			"extension-session": "extension-target",
		},
		sessionTypes: map[string]string{
			"web-session":       "page",
			"extension-session": "page",
		},
		sessionLoaderIDs: map[string]string{},
		targetSessions: map[string]string{
			"web-target":       "web-session",
			"extension-target": "extension-session",
		},
		targets: map[string]string{
			"web-target":       "https://example.com",
			"extension-target": "chrome-extension://example/home.html",
		},
		knownPageTargets: map[string]bool{
			"web-target":       true,
			"extension-target": true,
		},
		destroyedTargets: map[string]time.Time{},
		sessionOrder:     []string{"web-session", "extension-session"},
	}
	c.setActiveSession("web-session")
	c.setActiveSession("extension-session")
	if c.activeSessionID != "extension-session" || c.lastActivePageSessionID != "web-session" {
		t.Fatalf("unexpected active history: active=%q page=%q", c.activeSessionID, c.lastActivePageSessionID)
	}

	c.handleMessage(map[string]interface{}{
		"method": "Target.targetDestroyed",
		"params": map[string]interface{}{"targetId": "extension-target"},
	})

	if c.activeSessionID != "web-session" || c.activeSessionURL != "https://example.com" {
		t.Fatalf("active page was not restored: session=%q url=%q", c.activeSessionID, c.activeSessionURL)
	}
}

func TestFindPageSessionForActivationPrefersLastActivePage(t *testing.T) {
	c := &CDPClient{
		activeSessionID:         "extension-session",
		lastActivePageSessionID: "recent-page-session",
		sessionURLs: map[string]string{
			"old-page-session":    "https://old.example.com",
			"recent-page-session": "https://example.com",
			"extension-session":   "chrome-extension://example/popup.html",
		},
		sessionTypes: map[string]string{
			"old-page-session":    "page",
			"recent-page-session": "page",
			"extension-session":   "page",
		},
		sessionOrder: []string{"old-page-session", "recent-page-session", "extension-session"},
	}

	if got := c.findPageSessionForActivation(); got != "recent-page-session" {
		t.Fatalf("findPageSessionForActivation() = %q, want recent-page-session", got)
	}
}

func TestDetachedExtensionSessionRestoresLastActivePage(t *testing.T) {
	c := &CDPClient{
		sessions: map[string]bool{
			"web-session":       true,
			"extension-session": true,
		},
		sessionURLs: map[string]string{
			"web-session":       "https://example.com",
			"extension-session": "chrome-extension://example/home.html",
		},
		sessionTargets: map[string]string{
			"web-session":       "web-target",
			"extension-session": "extension-target",
		},
		sessionTypes: map[string]string{
			"web-session":       "page",
			"extension-session": "page",
		},
		sessionLoaderIDs: map[string]string{},
		targetSessions: map[string]string{
			"web-target":       "web-session",
			"extension-target": "extension-session",
		},
		sessionOrder: []string{"web-session", "extension-session"},
	}
	c.setActiveSession("web-session")
	c.setActiveSession("extension-session")

	c.handleMessage(map[string]interface{}{
		"method": "Target.detachedFromTarget",
		"params": map[string]interface{}{"sessionId": "extension-session"},
	})

	if c.activeSessionID != "web-session" || c.activeSessionURL != "https://example.com" {
		t.Fatalf("active page was not restored after detach: session=%q url=%q", c.activeSessionID, c.activeSessionURL)
	}
}

func TestClearCachedVisibleExtensionMatchesExtension(t *testing.T) {
	c := &CDPClient{
		lastVisibleExtensionTargetID: "target",
		lastVisibleExtensionURL:      "chrome-extension://example/popup.html",
	}

	c.clearCachedVisibleExtension("different")
	if c.lastVisibleExtensionTargetID == "" {
		t.Fatal("unrelated extension cleared visible popup cache")
	}
	c.clearCachedVisibleExtension("example")
	if c.lastVisibleExtensionTargetID != "" || c.lastVisibleExtensionURL != "" {
		t.Fatal("matching extension popup cache was not cleared")
	}
}

func TestTargetDestroyedRemovesUnknownExtensionFrameSession(t *testing.T) {
	c := &CDPClient{
		sessions:         map[string]bool{"frame-session": true},
		sessionURLs:      map[string]string{"frame-session": "chrome-extension://example/ses.html"},
		sessionTargets:   map[string]string{"frame-session": "frame-target"},
		sessionTypes:     map[string]string{"frame-session": "iframe"},
		sessionLoaderIDs: map[string]string{"frame-session": "loader"},
		targetSessions:   map[string]string{"frame-target": "frame-session"},
		targets:          map[string]string{"frame-target": "chrome-extension://example/ses.html"},
		knownPageTargets: map[string]bool{},
		destroyedTargets: map[string]time.Time{},
		sessionOrder:     []string{"frame-session"},
	}

	c.handleMessage(map[string]interface{}{
		"method": "Target.targetDestroyed",
		"params": map[string]interface{}{"targetId": "frame-target"},
	})

	if _, ok := c.sessionURLs["frame-session"]; ok {
		t.Fatal("unknown extension frame session was not removed")
	}
	if _, ok := c.targetSessions["frame-target"]; ok {
		t.Fatal("destroyed extension frame target mapping was not removed")
	}
	if len(c.sessionOrder) != 0 {
		t.Fatalf("sessionOrder = %v, want empty", c.sessionOrder)
	}
}

func TestBrowserTabTargetUsesExactTargetIDWithDuplicateURLs(t *testing.T) {
	c := &CDPClient{
		sessionURLs: map[string]string{
			"session-a": "https://example.com/a",
			"session-b": "https://example.com/same",
			"session-c": "https://example.com/same/",
		},
		sessionTargets: map[string]string{
			"session-a": "target-a",
			"session-b": "target-b",
			"session-c": "target-c",
		},
		sessionTypes: map[string]string{
			"session-a": "page",
			"session-b": "page",
			"session-c": "page",
		},
		targetSessions: map[string]string{
			"target-a": "session-a",
			"target-b": "session-b",
			"target-c": "session-c",
		},
		sessionOrder: []string{"session-a", "session-b", "session-c"},
	}

	selected := c.browserTabTargetLocked(browserTabDescriptor{
		TargetID: "target-c",
		Index:    1,
		URL:      "https://example.com/same",
	})
	if selected.sessionID != "session-c" || selected.targetID != "target-c" {
		t.Fatalf("selected = %+v, want exact target-c", selected)
	}
	if guessed := c.browserTabTargetLocked(browserTabDescriptor{Index: 2, URL: "https://example.com/same/"}); guessed.sessionID != "" {
		t.Fatalf("missing target ID must not guess by URL/index: %+v", guessed)
	}
}

func TestExtensionWindowContentInsetsForActionPopup(t *testing.T) {
	left, top := extensionWindowContentInsets(374, 614, 360, 600)
	if left != 7 || top != 7 {
		t.Fatalf("insets = (%.1f, %.1f), want (7, 7)", left, top)
	}
}

func TestDeclaredExtensionPopupFallbackStaysNarrow(t *testing.T) {
	for _, required := range []string{
		"manifest.action&&manifest.action.default_popup",
		"chrome.action.getPopup",
		"chrome.action.setPopup({tabId:tab.id,popup:defaultPopup})",
		"chrome.action.openPopup()",
	} {
		if !strings.Contains(declaredExtensionPopupScript, required) {
			t.Fatalf("declared popup script missing %q", required)
		}
	}
	for _, forbidden := range []string{"chrome.windows.create", "chrome.sidePanel", "setTimeout", "setInterval"} {
		if strings.Contains(declaredExtensionPopupScript, forbidden) {
			t.Fatalf("declared popup script must not use %q", forbidden)
		}
	}
}

func TestExtensionWindowContentInsetsForTitledWindow(t *testing.T) {
	left, top := extensionWindowContentInsets(374, 658, 360, 600)
	if left != 7 || top != 51 {
		t.Fatalf("insets = (%.1f, %.1f), want (7, 51)", left, top)
	}
}

func TestExtensionVisibleTargetClassificationSupportsPopupAndSidePanel(t *testing.T) {
	tests := []string{
		"chrome-extension://example/popup.html",
		"chrome-extension://example/popup-init.html#/unlock",
		"chrome-extension://example/sidepanel.html#/unlock",
		"chrome-extension://example/notification.html#/unlock",
	}
	for _, targetURL := range tests {
		if !isVisibleExtensionTarget("page", targetURL) {
			t.Fatalf("visible extension UI was not classified: %s", targetURL)
		}
	}
}

func TestExtensionContentPointMapsBetweenDifferentViewportSizes(t *testing.T) {
	c := &CDPClient{
		viewportMetricsBySession: map[string]CDPViewportMetrics{
			"sidepanel": {
				VisualViewportWidth:  400,
				VisualViewportHeight: 800,
			},
		},
	}
	x, y, ok := c.ExtensionContentPointToViewport("sidepanel", 180, 150, 360, 600)
	if !ok {
		t.Fatal("extension content point was not mapped")
	}
	if x != 200 || y != 200 {
		t.Fatalf("mapped point = (%.1f, %.1f), want (200, 200)", x, y)
	}
}

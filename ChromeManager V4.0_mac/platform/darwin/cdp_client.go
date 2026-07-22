package darwin

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"chromemanager/config"

	"golang.org/x/net/websocket"
)

var cdpDebugLogs = strings.EqualFold(os.Getenv("CHROMEMANAGER_DEBUG"), "1") || strings.EqualFold(os.Getenv("CHROMEMANAGER_DEBUG"), "true")

const declaredExtensionPopupScript = `(async()=>{
	const manifest=chrome.runtime.getManifest();
	const defaultPopup=manifest.action&&manifest.action.default_popup;
	if(!defaultPopup||!chrome.action||!chrome.action.openPopup) throw new Error("declared action popup unavailable");
	const tabs=chrome.tabs&&chrome.tabs.query?await chrome.tabs.query({active:true}):[];
	const activeURL=%s;
	const tab=(tabs||[]).find(item=>item.url===activeURL)||(tabs&&tabs[0]);
	if(!tab||tab.id===undefined||tab.id===null) throw new Error("active extension tab unavailable");
	let currentPopup="";
	if(chrome.action.getPopup){
	  try{ currentPopup=await chrome.action.getPopup({tabId:tab.id}); }catch(e){}
	}
	if(!currentPopup){
	  if(!chrome.action.setPopup) throw new Error("chrome.action.setPopup unavailable");
	  await chrome.action.setPopup({tabId:tab.id,popup:defaultPopup});
	}
	await chrome.action.openPopup();
	return defaultPopup;
})()`

func cdpDebugf(format string, args ...interface{}) {
	if cdpDebugLogs {
		log.Printf(format, args...)
	}
}

// CDPClient
// CDPClient manages Chrome CDP connection at the Browser Level.
// It supports Session Flattening (Target.setAutoAttach) to handle multiple tabs/pages.
type CDPClient struct {
	debugPort     int
	userDataDir   string
	wsURL         string
	ws            *websocket.Conn
	wsWriteMu     sync.Mutex
	mu            sync.Mutex
	msgID         int
	isConnected   bool // True when WebSocket is active
	isInitialized bool // True 1 second after connect, to ignore initial target dumps

	// Session Management
	activeSessionID         string
	activeSessionURL        string
	lastActivePageSessionID string
	sessions                map[string]bool   // Set of active sessions
	sessionURLs             map[string]string // sessionId -> url
	sessionCommittedURLs    map[string]string // sessionId -> last top-frame committed url
	sessionTargets          map[string]string // sessionId -> targetId
	sessionTypes            map[string]string // sessionId -> target type
	sessionLoaderIDs        map[string]string // sessionId -> top frame loaderId
	targetSessions          map[string]string // targetId -> sessionId
	sessionOrder            []string          // Ordered list of session IDs as they attached
	knownPageTargets        map[string]bool   // Set of targets known to be actual pages
	targets                 map[string]string // targetId -> url
	destroyedTargets        map[string]time.Time
	pendingBrowserTab       *browserTabDescriptor
	domClickSessions        map[string]bool
	tabVisibilitySessions   map[string]bool

	extensionSessionID string
	extensionTargetID  string
	extensionURL       string
	extensionSessionMu sync.Mutex

	lastVisibleExtensionURL      string
	lastVisibleExtensionTargetID string
	viewportMetricsBySession     map[string]CDPViewportMetrics

	// Request Coordination
	pendingRequests map[int]chan map[string]interface{}
	targetEvents    chan struct{}

	// Callbacks
	OnNavigate        func(sessionID string, url string)
	OnURLReloaded     func(sessionID string, url string)
	OnTargetCreated   func(targetID string, openerTargetID string, targetType string, url string)
	OnTargetLoaded    func(targetID string, url string)
	OnTargetDestroyed func(targetID string, targetURL string)
	OnTabActivated    func(targetID string, url string)
	OnExtensionInput  func(state extensionEditableState)
	OnDOMClick        func(sessionID string, action DOMClickAction)
}

type CDPViewportMetrics struct {
	InnerWidth           float64
	InnerHeight          float64
	VisualViewportWidth  float64
	VisualViewportHeight float64
}

type browserTabDescriptor struct {
	TabID    int    `json:"tabId"`
	WindowID int    `json:"windowId"`
	Index    int    `json:"index"`
	URL      string `json:"url"`
	TargetID string `json:"targetId"`
	Active   bool   `json:"active"`
}

type DOMClickAction struct {
	Kind             string  `json:"kind"`
	Selector         string  `json:"selector"`
	SemanticSelector string  `json:"semanticSelector"`
	SemanticIndex    int     `json:"semanticIndex"`
	SemanticText     string  `json:"semanticText"`
	ElementX         float64 `json:"elementX"`
	ElementY         float64 `json:"elementY"`
	SourceURL        string  `json:"-"`
}

func isInternalPageURL(url string) bool {
	return strings.Contains(url, "omnibox-popup") || strings.HasPrefix(url, "devtools://")
}

func isIgnoredCDPTargetURL(targetURL string) bool {
	if isInternalPageURL(targetURL) {
		return true
	}
	if isChromeManagerZoomExtensionURL(targetURL) || isChromeManagerZoomExtensionControlURL(targetURL) {
		return true
	}
	if strings.HasPrefix(targetURL, "chrome-extension://") {
		return strings.Contains(targetURL, "background") || strings.Contains(targetURL, "service_worker")
	}
	return false
}

func isExtensionURL(targetURL string) bool {
	return strings.HasPrefix(targetURL, "chrome-extension://")
}

func extensionIDFromURL(targetURL string) string {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(targetURL, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(targetURL, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func isVisibleExtensionTargetType(targetType string) bool {
	switch targetType {
	case "page", "popup", "other", "webview":
		return true
	default:
		return false
	}
}

func isVisibleExtensionTarget(targetType, targetURL string) bool {
	if !isExtensionURL(targetURL) || !isVisibleExtensionTargetType(targetType) || isIgnoredCDPTargetURL(targetURL) {
		return false
	}
	if targetType == "page" || targetType == "popup" {
		_, ok := extensionPopupID(targetURL)
		return ok
	}
	return true
}

func isExtensionFullPageTarget(targetType, targetURL string) bool {
	if targetType != "page" || !isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return false
	}
	_, isPopup := extensionPopupID(targetURL)
	return !isPopup
}

func isActivePageTarget(targetType, targetURL string) bool {
	if targetType != "page" || isBlankTargetURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return false
	}
	if isExtensionURL(targetURL) {
		return isExtensionFullPageTarget(targetType, targetURL)
	}
	return true
}

func supportsDOMClickBinding(targetType, targetURL string) bool {
	if isBlankTargetURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return false
	}
	if targetType == "page" && (strings.HasPrefix(targetURL, "https://") || strings.HasPrefix(targetURL, "http://")) {
		return true
	}
	return isExtensionURL(targetURL) && isExtensionFocusTarget(targetType, targetURL)
}

func isExtensionFocusTarget(targetType, targetURL string) bool {
	return isVisibleExtensionTarget(targetType, targetURL) ||
		isExtensionFullPageTarget(targetType, targetURL) ||
		isExtensionInputFrameTarget(targetType, targetURL)
}

func shouldNotifyTargetInfoChanged(wasKnown bool, targetType, previousURL, targetURL string) bool {
	if !wasKnown {
		return true
	}
	if targetURL == previousURL {
		return false
	}
	if isBlankTargetURL(previousURL) && !isBlankTargetURL(targetURL) {
		return true
	}

	wasVisibleExtension := isVisibleExtensionTarget(targetType, previousURL)
	isVisibleExtension := isVisibleExtensionTarget(targetType, targetURL)
	return isVisibleExtension && !wasVisibleExtension
}

func isExtensionChildTargetHost(targetType, targetURL string) bool {
	return targetType == "page" && isExtensionURL(targetURL) && !isIgnoredCDPTargetURL(targetURL)
}

func extensionPopupID(targetURL string) (string, bool) {
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(targetURL, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(targetURL, prefix)
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", false
	}

	path := strings.ToLower(parts[1])
	path = strings.SplitN(path, "#", 2)[0]
	path = strings.SplitN(path, "?", 2)[0]
	if strings.Contains(path, "background") ||
		strings.Contains(path, "service-worker") ||
		strings.Contains(path, "service_worker") ||
		strings.Contains(path, "offscreen") ||
		strings.Contains(path, "sandbox") ||
		strings.Contains(path, "snaps/") {
		return "", false
	}

	base := path
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if !strings.Contains(path, "popup") &&
		!strings.Contains(path, "sidepanel") &&
		base != "index.html" &&
		base != "notification.html" {
		return "", false
	}
	return parts[0], true
}

func extensionPageMatchKey(targetURL string) string {
	if !isExtensionURL(targetURL) {
		return ""
	}
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host + parsed.Path
}

func extensionRouteFragment(targetURL string) string {
	if !isExtensionURL(targetURL) {
		return ""
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return ""
	}
	return parsed.Fragment
}

func isExtensionInputFrameTarget(targetType, targetURL string) bool {
	if targetType != "iframe" || !isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return false
	}
	lowerURL := strings.ToLower(targetURL)
	return !strings.Contains(lowerURL, "offscreen") &&
		!strings.Contains(lowerURL, "background")
}

func isSupportedTarget(targetType string, targetURL string) bool {
	if targetType == "page" {
		return true
	}
	if isExtensionURL(targetURL) {
		switch targetType {
		case "popup", "other", "webview", "iframe":
			return true
		}
	}
	return false
}

func shouldNotifyTargetCreated(targetType, targetURL string) bool {
	if isBlankTargetURL(targetURL) {
		return false
	}
	return targetType == "page" || targetType == "popup" || targetType == "other" || targetType == "webview"
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func removeString(items []string, value string) []string {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func normalizeComparableURL(url string) string {
	url = strings.TrimSpace(url)
	if url == "chrome://newtab" || url == "chrome://newtab/" || url == "chrome://new-tab-page" || url == "chrome://new-tab-page/" {
		return "chrome://new-tab-page"
	}
	return strings.TrimSuffix(url, "/")
}

func isChromeNewTabURL(url string) bool {
	return normalizeComparableURL(url) == "chrome://new-tab-page"
}

func isBrowserInternalSyncURL(url string) bool {
	normalized := normalizeComparableURL(url)
	return strings.HasPrefix(normalized, "chrome://") && normalized != "chrome://new-tab-page"
}

func sameComparableURL(a, b string) bool {
	return normalizeComparableURL(a) == normalizeComparableURL(b)
}

func isComparablePageTarget(targetType, targetURL string) bool {
	if targetType != "page" {
		return false
	}
	if isBlankTargetURL(targetURL) || isInternalPageURL(targetURL) {
		return false
	}
	if isExtensionURL(targetURL) {
		return isExtensionFullPageTarget(targetType, targetURL)
	}
	return true
}

func isBlankTargetURL(targetURL string) bool {
	targetURL = strings.TrimSpace(targetURL)
	return targetURL == "" || targetURL == "about:blank"
}

func (m CDPViewportMetrics) ViewportWidth() float64 {
	if m.VisualViewportWidth > 0 {
		return m.VisualViewportWidth
	}
	return m.InnerWidth
}

func (m CDPViewportMetrics) ViewportHeight() float64 {
	if m.VisualViewportHeight > 0 {
		return m.VisualViewportHeight
	}
	return m.InnerHeight
}

func numberFromAny(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}

type NativeZoomResult struct {
	Zoom     float64
	TabID    int
	WindowID int
	URL      string
	Mode     string
	Error    string
}

func isChromeManagerZoomExtensionURL(url string) bool {
	return strings.HasPrefix(url, "chrome-extension://") && strings.Contains(url, "/chromemanager_zoom_worker.js")
}

func isChromeManagerZoomExtensionControlURL(url string) bool {
	return strings.HasPrefix(url, "chrome-extension://") && strings.Contains(url, "/chromemanager_zoom_controller.html")
}

func chromeManagerZoomExtensionScopeURL() string {
	return "chrome-extension://" + config.ChromeManagerZoomExtensionID + "/"
}

func chromeManagerZoomExtensionControlURL() string {
	return chromeManagerZoomExtensionScopeURL() + "chromemanager_zoom_controller.html"
}

func extensionScopeFromWorkerURL(url string) string {
	if !strings.HasPrefix(url, "chrome-extension://") {
		return ""
	}
	withoutScheme := strings.TrimPrefix(url, "chrome-extension://")
	slash := strings.Index(withoutScheme, "/")
	if slash <= 0 {
		return ""
	}
	return "chrome-extension://" + withoutScheme[:slash] + "/"
}

// NewCDPClient creates a new CDP client
func NewCDPClient(debugPort int) *CDPClient {
	return &CDPClient{
		debugPort:                debugPort,
		sessions:                 make(map[string]bool),
		sessionURLs:              make(map[string]string),
		sessionCommittedURLs:     make(map[string]string),
		sessionTargets:           make(map[string]string),
		sessionTypes:             make(map[string]string),
		sessionLoaderIDs:         make(map[string]string),
		targetSessions:           make(map[string]string),
		knownPageTargets:         make(map[string]bool),
		targets:                  make(map[string]string),
		destroyedTargets:         make(map[string]time.Time),
		pendingRequests:          make(map[int]chan map[string]interface{}),
		targetEvents:             make(chan struct{}, 1),
		viewportMetricsBySession: make(map[string]CDPViewportMetrics),
		domClickSessions:         make(map[string]bool),
		tabVisibilitySessions:    make(map[string]bool),
	}
}

func (c *CDPClient) setUserDataDir(userDataDir string) {
	c.userDataDir = strings.TrimSpace(userDataDir)
}

func (c *CDPClient) signalTargetEvent() {
	select {
	case c.targetEvents <- struct{}{}:
	default:
	}
}

// GetActiveSessionURL returns the current active URL safely
func (c *CDPClient) GetActiveSessionURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeSessionURL
}

func (c *CDPClient) activeRouteSnapshot() (targetID, sessionID, targetURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sessionID = c.activeSessionID
	if sessionID == "" {
		return "", "", ""
	}
	targetID = c.sessionTargets[sessionID]
	targetURL = c.sessionURLs[sessionID]
	if targetID == "" || !isActivePageTarget(c.sessionTypes[sessionID], targetURL) {
		return "", "", ""
	}
	return targetID, sessionID, targetURL
}

func (c *CDPClient) pageRouteForDOMClick(sessionID, sourceURL string) (targetID, routeSessionID string) {
	if sessionID == "" || sourceURL == "" {
		return "", ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	targetURL := c.sessionURLs[sessionID]
	targetID = c.sessionTargets[sessionID]
	if targetID == "" || !isActivePageTarget(c.sessionTypes[sessionID], targetURL) ||
		!sameComparableURL(targetURL, sourceURL) {
		return "", ""
	}

	// The DOM binding only reports trusted pointer events. The callback session
	// is therefore authoritative when Chrome's tab-visibility update arrives
	// slightly later than the first click in a newly opened tab.
	c.setActiveSessionLocked(sessionID, targetURL)
	return targetID, sessionID
}

func (c *CDPClient) GetViewportMetrics() (CDPViewportMetrics, error) {
	result, err := c.Evaluate(`(() => {
		const vv = window.visualViewport;
		return {
			innerWidth: Number(window.innerWidth) || 0,
			innerHeight: Number(window.innerHeight) || 0,
			vvWidth: vv ? (Number(vv.width) || 0) : 0,
			vvHeight: vv ? (Number(vv.height) || 0) : 0
		};
	})()`)
	if err != nil {
		return CDPViewportMetrics{}, err
	}

	values, ok := result.(map[string]interface{})
	if !ok {
		return CDPViewportMetrics{}, fmt.Errorf("invalid viewport metrics payload: %T", result)
	}

	return CDPViewportMetrics{
		InnerWidth:           numberFromAny(values["innerWidth"]),
		InnerHeight:          numberFromAny(values["innerHeight"]),
		VisualViewportWidth:  numberFromAny(values["vvWidth"]),
		VisualViewportHeight: numberFromAny(values["vvHeight"]),
	}, nil
}

func clampCDPFloat64(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func extensionWindowContentInsets(windowW, windowH, viewportW, viewportH float64) (float64, float64) {
	leftInset := 0.0
	if windowW > viewportW {
		leftInset = (windowW - viewportW) / 2
	}

	heightDiff := windowH - viewportH
	if heightDiff <= 0 {
		return leftInset, 0
	}
	// Extension action popups have an even shadow/border around the viewport.
	// Standalone extension windows may additionally have title chrome at the top.
	bottomInset := leftInset
	if maxBottom := heightDiff / 2; bottomInset > maxBottom {
		bottomInset = maxBottom
	}
	return leftInset, heightDiff - bottomInset
}

func (c *CDPClient) ExtensionWindowPointToViewport(sessionID string, x, y, windowW, windowH float64) (float64, float64, bool) {
	if sessionID == "" {
		return 0, 0, false
	}
	metrics, ok := c.viewportMetricsForSession(sessionID)
	if !ok {
		return 0, 0, false
	}
	viewportW := metrics.ViewportWidth()
	viewportH := metrics.ViewportHeight()
	if viewportW <= 0 || viewportH <= 0 {
		return 0, 0, false
	}

	// MetaMask's LavaMoat blocks outerWidth/outerHeight, so infer browser chrome from the native popup rect.
	leftInset, topInset := extensionWindowContentInsets(windowW, windowH, viewportW, viewportH)

	return clampCDPFloat64(x-leftInset, 0, viewportW-1), clampCDPFloat64(y-topInset, 0, viewportH-1), true
}

func (c *CDPClient) ExtensionContentPointToViewport(sessionID string, x, y, sourceW, sourceH float64) (float64, float64, bool) {
	if sessionID == "" || sourceW <= 0 || sourceH <= 0 {
		return 0, 0, false
	}
	metrics, ok := c.viewportMetricsForSession(sessionID)
	if !ok {
		return 0, 0, false
	}
	targetW := metrics.ViewportWidth()
	targetH := metrics.ViewportHeight()
	if targetW <= 0 || targetH <= 0 {
		return 0, 0, false
	}
	rx := clampCDPFloat64(x/sourceW, 0, 1)
	ry := clampCDPFloat64(y/sourceH, 0, 1)
	return clampCDPFloat64(rx*targetW, 0, targetW-1), clampCDPFloat64(ry*targetH, 0, targetH-1), true
}

func (c *CDPClient) viewportMetricsForSession(sessionID string) (CDPViewportMetrics, bool) {
	c.mu.Lock()
	metrics, ok := c.viewportMetricsBySession[sessionID]
	c.mu.Unlock()
	if ok && metrics.ViewportWidth() > 0 && metrics.ViewportHeight() > 0 {
		return metrics, true
	}
	return c.refreshViewportMetrics(sessionID)
}

func (c *CDPClient) refreshViewportMetrics(sessionID string) (CDPViewportMetrics, bool) {
	if sessionID == "" {
		return CDPViewportMetrics{}, false
	}
	result, err := c.callCommand("Page.getLayoutMetrics", nil, sessionID, 500*time.Millisecond)
	if err != nil {
		return CDPViewportMetrics{}, false
	}
	visualViewport, _ := result["cssVisualViewport"].(map[string]interface{})
	layoutViewport, _ := result["cssLayoutViewport"].(map[string]interface{})
	metrics := CDPViewportMetrics{
		VisualViewportWidth:  numberFromAny(visualViewport["clientWidth"]),
		VisualViewportHeight: numberFromAny(visualViewport["clientHeight"]),
		InnerWidth:           numberFromAny(layoutViewport["clientWidth"]),
		InnerHeight:          numberFromAny(layoutViewport["clientHeight"]),
	}
	if metrics.ViewportWidth() <= 0 || metrics.ViewportHeight() <= 0 {
		return CDPViewportMetrics{}, false
	}
	c.mu.Lock()
	c.viewportMetricsBySession[sessionID] = metrics
	c.mu.Unlock()
	return metrics, true
}

// Connect connects to the Chrome Browser endpoint
func (c *CDPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws != nil {
		return nil
	}

	// 1. Get Browser WebSocket URL
	// GET http://localhost:<port>/json/version
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/json/version", c.debugPort))
	if err != nil {
		return fmt.Errorf("failed to get version: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read version failed: %v", err)
	}

	var versionInfo map[string]interface{}
	if err := json.Unmarshal(body, &versionInfo); err != nil {
		return fmt.Errorf("parse version failed: %v", err)
	}

	wsURL, ok := versionInfo["webSocketDebuggerUrl"].(string)
	if !ok || wsURL == "" {
		return fmt.Errorf("no webSocketDebuggerUrl found (ensure --remote-debugging-port is set)")
	}
	c.wsURL = wsURL

	// 2. Dial WebSocket
	config, err := websocket.NewConfig(wsURL, "http://localhost")
	if err != nil {
		return err
	}
	ws, err := websocket.DialConfig(config)
	if err != nil {
		return err
	}
	c.ws = ws

	// 3. Start Reading Loop (For Events)
	go c.readLoop()

	// 4. Enable Auto-Attach (Flattened)
	// This ensures we get a sessionId for every page/target.
	err = c.sendCommand("Target.setAutoAttach", map[string]interface{}{
		"autoAttach":             true,
		"waitForDebuggerOnStart": false,
		"flatten":                true,
	})
	if err != nil {
		cdpDebugf("[CDP] warning setAutoAttach failed: %v\n", err)
	}

	// 5. Enable Target Discovery (CRITICAL for Master to see new tabs)
	err = c.sendCommand("Target.setDiscoverTargets", map[string]interface{}{
		"discover": true,
	})
	if err != nil {
		cdpDebugf("[CDP] warning setDiscoverTargets failed: %v\n", err)
	}

	// 6. Enable Page domain to receive frameNavigated events
	_ = c.sendCommand("Page.enable", nil)

	c.isConnected = true

	// 7. Set initialized flag after 1 second to ignore initial burst of targets
	go func() {
		time.Sleep(1 * time.Second)
		c.mu.Lock()
		c.isInitialized = true
		c.mu.Unlock()
		c.requestBrowserActiveTab()
		cdpDebugf("[CDP] Initialization complete. Listening for new targets on port %d", c.debugPort)
	}()

	return nil
}

func (c *CDPClient) readLoop() {
	for {
		var msg map[string]interface{}
		if err := websocket.JSON.Receive(c.ws, &msg); err != nil {
			// Connection closed - mark as disconnected
			c.mu.Lock()
			c.isConnected = false
			c.mu.Unlock()
			cdpDebugf("[CDP] readLoop exited: %v\n", err)
			return
		}
		c.handleMessage(msg)
	}
}

const extensionSecureInputBinding = "chromeManagerExtensionSecureInput"
const extensionSecureInputWorld = "ChromeManagerExtensionInput"

const extensionSecureInputScript = `(() => {
  if (globalThis.__chromeManagerSecureInputInstalled) return true;
  globalThis.__chromeManagerSecureInputInstalled = true;

  const passwordTarget = (event) => {
    const path = typeof event.composedPath === "function" ? event.composedPath() : [];
    const target = path.length ? path[0] : event.target;
    if (!target || target.nodeType !== 1 || String(target.tagName).toUpperCase() !== "INPUT") return null;
    return String(target.type || "").toLowerCase() === "password" ? target : null;
  };
  const emit = (kind, target, event) => {
    const binding = globalThis.chromeManagerExtensionSecureInput;
    if (typeof binding !== "function") return;
    const fields = Array.from(document.querySelectorAll('input[type="password"]'));
    binding(JSON.stringify({
      kind,
      targetURL: location.href,
      inputType: event && typeof event.inputType === "string" ? event.inputType : "",
      data: event && typeof event.data === "string" ? event.data : "",
      value: String(target.value || ""),
      selectionStart: typeof target.selectionStart === "number" ? target.selectionStart : -1,
      selectionEnd: typeof target.selectionEnd === "number" ? target.selectionEnd : -1,
      elementID: target.id || "",
      elementName: target.name || "",
      elementTag: target.tagName || "",
      elementType: target.type || "",
      elementIndex: fields.indexOf(target)
    }));
  };

  document.addEventListener("input", (event) => {
    const target = passwordTarget(event);
    if (target) emit("edit", target, event);
  }, true);
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.code !== "Enter" && event.keyCode !== 13) return;
    const target = passwordTarget(event);
    if (target) emit("enter", target, event);
  }, true);
  return true;
})()`

func extensionInputFrameIDs(frameTreeResult map[string]interface{}) []string {
	root, _ := frameTreeResult["frameTree"].(map[string]interface{})
	if root == nil {
		return nil
	}

	frameIDs := make([]string, 0, 2)
	var visit func(map[string]interface{})
	visit = func(tree map[string]interface{}) {
		frame, _ := tree["frame"].(map[string]interface{})
		if frameID, _ := frame["id"].(string); frameID != "" {
			frameIDs = append(frameIDs, frameID)
		}
		children, _ := tree["childFrames"].([]interface{})
		for _, rawChild := range children {
			child, _ := rawChild.(map[string]interface{})
			if child != nil {
				visit(child)
			}
		}
	}
	visit(root)
	return frameIDs
}

func extensionInputEvaluationError(result map[string]interface{}) error {
	if details, ok := result["exceptionDetails"]; ok && details != nil {
		return fmt.Errorf("javascript exception: %v", details)
	}
	return nil
}

const tabVisibilityBinding = "chromeManagerTabVisibility"

const tabVisibilityScript = `(() => {
  const emit = () => {
    if (document.visibilityState !== "visible") return;
    const binding = globalThis.chromeManagerTabVisibility;
    if (typeof binding === "function") binding(location.href);
  };
  if (!globalThis.__chromeManagerTabVisibilityInstalled) {
    globalThis.__chromeManagerTabVisibilityInstalled = true;
    document.addEventListener("visibilitychange", emit, true);
    window.addEventListener("pageshow", emit, true);
  }
  emit();
  return true;
})()`

const domClickBinding = "chromeManagerDOMClick"
const domClickWorld = "ChromeManagerDOMClick"

const domClickCaptureScript = `(() => {
  if (typeof globalThis.__chromeManagerDOMClickHandler === "function") {
    document.removeEventListener("click", globalThis.__chromeManagerDOMClickHandler, true);
		document.removeEventListener("pointerdown", globalThis.__chromeManagerDOMClickHandler, true);
  }

  const escapeCSS = (value) => globalThis.CSS && CSS.escape
    ? CSS.escape(value)
    : String(value).replace(/[^a-zA-Z0-9_-]/g, (ch) => "\\" + ch);
  const stableValue = (value) => value && value.length <= 100 && !/[a-f0-9]{16,}/i.test(value);
	const stableID = (value) => stableValue(value) && !/^(radix-|headlessui-|react-|_?r_)/i.test(value);
	const textOf = (element) => String((element && (element.innerText || element.value || element.textContent)) || "")
		.replace(/\s+/g, " ").trim().slice(0, 160);
  const unique = (root, selector) => {
    try {
      return root.querySelectorAll(selector).length === 1;
    } catch (_) {
      return false;
    }
  };
  const segment = (element, root) => {
    const tag = element.tagName.toLowerCase();
    for (const name of ["data-testid", "data-test-id", "data-qa", "data-cy", "data-action", "aria-label", "title"]) {
      const value = element.getAttribute(name);
      if (!stableValue(value)) continue;
      const selector = tag + "[" + name + "=" + JSON.stringify(value) + "]";
      return {value: selector, anchor: unique(root, selector)};
    }
		if (stableID(element.id)) {
			const selector = "#" + escapeCSS(element.id);
			if (unique(root, selector)) return {value: selector, anchor: true};
		}
    if (/^(input|textarea|select)$/.test(tag) && stableValue(element.getAttribute("name"))) {
      const selector = tag + "[name=" + JSON.stringify(element.getAttribute("name")) + "]";
      return {value: selector, anchor: unique(root, selector)};
    }
    const parent = element.parentElement;
    if (!parent) return {value: tag, anchor: false};
    const siblings = Array.from(parent.children).filter((item) => item.tagName === element.tagName);
    return {
      value: siblings.length > 1 ? tag + ":nth-of-type(" + (siblings.indexOf(element) + 1) + ")" : tag,
      anchor: false
    };
  };
  const selectorFor = (element) => {
    const layers = [];
    let current = element;
    while (current && current.nodeType === 1) {
      const root = current.getRootNode ? current.getRootNode() : document;
      const parts = [];
      let node = current;
      while (node && node.nodeType === 1) {
        const part = segment(node, root);
        parts.unshift(part.value);
        if (part.anchor || unique(root, parts.join(" > "))) break;
        node = node.parentElement;
      }
      layers.unshift(parts.join(" > "));
      current = root && root.host ? root.host : null;
    }
    return layers.filter(Boolean).join(" >>> ");
  };
	const semanticFor = (element) => {
		const root = element && element.getRootNode ? element.getRootNode() : document;
		if (!element || root !== document) return {selector: "", index: -1, text: ""};
		const tag = element.tagName.toLowerCase();
		for (const name of ["data-testid", "data-test-id", "data-qa", "data-cy", "data-action", "aria-label", "name", "title"]) {
			const value = element.getAttribute(name);
			if (!stableValue(value)) continue;
			const selector = tag + "[" + name + "=" + JSON.stringify(value) + "]";
			let matches = [];
			try { matches = Array.from(document.querySelectorAll(selector)); } catch (_) {}
			const index = matches.indexOf(element);
			if (index >= 0) return {selector, index, text: ""};
		}
		const role = element.getAttribute("role");
		const text = textOf(element);
		const selector = stableValue(role) ? tag + "[role=" + JSON.stringify(role) + "]" : tag;
		let matches = [];
		try { matches = Array.from(document.querySelectorAll(selector)); } catch (_) {}
		if (text) matches = matches.filter((candidate) => textOf(candidate) === text);
		const index = matches.indexOf(element);
		return index >= 0 ? {selector, index, text} : {selector: "", index: -1, text: ""};
	};
  const svgInternals = new Set(["path", "circle", "rect", "line", "polygon", "polyline", "use", "g", "defs", "clippath", "ellipse", "text", "tspan"]);
	const explicitInteractive = (element) => {
    if (!element || element.nodeType !== 1) return false;
    const tag = element.tagName.toLowerCase();
    const role = element.getAttribute("role");
    return /^(a|button|input|select|textarea|label|summary)$/.test(tag) ||
      /^(button|link|tab|menuitem|option|checkbox|radio|switch|slider|combobox)$/.test(role || "") ||
      element.hasAttribute("onclick") || element.hasAttribute("data-action") ||
      element.hasAttribute("data-testid") || element.hasAttribute("data-test-id") ||
      element.hasAttribute("data-qa") || element.hasAttribute("data-cy") ||
			element.tabIndex >= 0;
	};
	const interactive = (element) => {
		return explicitInteractive(element) || (element && element.nodeType === 1 && getComputedStyle(element).cursor === "pointer");
  };
  const clickTarget = (event) => {
    const path = event.composedPath ? event.composedPath() : [event.target];
		return path.find(explicitInteractive) || path.find(interactive) || path.find((item) => {
      if (!item || item.nodeType !== 1) return false;
      const tag = item.tagName.toLowerCase();
      return !/^(html|body)$/.test(tag) && !svgInternals.has(tag);
    });
  };
  const handler = (event) => {
		if (!event.isTrusted || event.button !== 0 || event.isPrimary === false || typeof globalThis.chromeManagerDOMClick !== "function") return;
    const path = event.composedPath ? event.composedPath() : [event.target];
    const opensNewTab = event.metaKey || event.ctrlKey || path.some((item) =>
      item && item.tagName === "A" && String(item.target || "").toLowerCase() === "_blank");
    if (opensNewTab) {
      globalThis.chromeManagerDOMClick(JSON.stringify({kind: "new_tab"}));
      return;
    }
    const target = clickTarget(event);
    if (!target) return;
    const selector = selectorFor(target);
		const semantic = semanticFor(target);
    const rect = target.getBoundingClientRect();
    if (!selector || rect.width <= 0 || rect.height <= 0) return;
    globalThis.chromeManagerDOMClick(JSON.stringify({
      kind: "click",
      selector,
		semanticSelector: semantic.selector,
		semanticIndex: semantic.index,
		semanticText: semantic.text,
      elementX: Math.max(0, Math.min(1, (event.clientX - rect.left) / rect.width)),
      elementY: Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height))
    }));
  };
  globalThis.__chromeManagerDOMClickHandler = handler;
	document.addEventListener("pointerdown", handler, true);
  return true;
})()`

func (c *CDPClient) installDOMClickBinding(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	targetURL := c.sessionURLs[sessionID]
	enabled := c.OnDOMClick != nil && supportsDOMClickBinding(c.sessionTypes[sessionID], targetURL)
	if !enabled {
		delete(c.domClickSessions, sessionID)
	}
	c.mu.Unlock()
	if !enabled {
		return
	}
	if isExtensionURL(targetURL) {
		c.installExtensionDOMClickBinding(sessionID, targetURL)
		return
	}
	if _, err := c.callCommand("Runtime.enable", nil, sessionID, 2*time.Second); err != nil {
		return
	}
	if _, err := c.callCommand("Runtime.addBinding", map[string]interface{}{"name": domClickBinding}, sessionID, 2*time.Second); err != nil {
		return
	}
	_, _ = c.callCommand("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": domClickCaptureScript}, sessionID, 2*time.Second)
	result, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    domClickCaptureScript,
		"returnByValue": true,
	}, sessionID, 2*time.Second)
	if err != nil || extensionInputEvaluationError(result) != nil {
		return
	}
	c.mu.Lock()
	c.domClickSessions[sessionID] = true
	c.mu.Unlock()
}

func (c *CDPClient) installExtensionDOMClickBinding(sessionID string, targetURL string) {
	const commandTimeout = 2 * time.Second
	if _, err := c.callCommand("Runtime.enable", nil, sessionID, commandTimeout); err != nil {
		cdpDebugf("[DOMClick] extension Runtime.enable failed url=%s: %v\n", targetURL, err)
		return
	}
	if _, err := c.callCommand("Runtime.addBinding", map[string]interface{}{
		"name":                 domClickBinding,
		"executionContextName": domClickWorld,
	}, sessionID, commandTimeout); err != nil {
		cdpDebugf("[DOMClick] extension Runtime.addBinding failed url=%s: %v\n", targetURL, err)
		return
	}
	if _, err := c.callCommand("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
		"source":    domClickCaptureScript,
		"worldName": domClickWorld,
	}, sessionID, commandTimeout); err != nil {
		cdpDebugf("[DOMClick] extension addScript failed url=%s: %v\n", targetURL, err)
	}
	frameTree, err := c.callCommand("Page.getFrameTree", nil, sessionID, commandTimeout)
	if err != nil {
		cdpDebugf("[DOMClick] extension Page.getFrameTree failed url=%s: %v\n", targetURL, err)
		return
	}

	installed := 0
	for _, frameID := range extensionInputFrameIDs(frameTree) {
		world, err := c.callCommand("Page.createIsolatedWorld", map[string]interface{}{
			"frameId":             frameID,
			"worldName":           domClickWorld,
			"grantUniveralAccess": true,
		}, sessionID, commandTimeout)
		if err != nil {
			cdpDebugf("[DOMClick] extension create isolated world failed url=%s frame=%s: %v\n", targetURL, frameID, err)
			continue
		}
		contextID := world["executionContextId"]
		if contextID == nil {
			continue
		}
		evalResult, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
			"expression": domClickCaptureScript,
			"contextId":  contextID,
		}, sessionID, commandTimeout)
		if err == nil {
			err = extensionInputEvaluationError(evalResult)
		}
		if err != nil {
			cdpDebugf("[DOMClick] extension isolated script failed url=%s frame=%s: %v\n", targetURL, frameID, err)
			continue
		}
		installed++
	}
	if installed == 0 {
		cdpDebugf("[DOMClick] no isolated binding installed url=%s session=%s\n", targetURL, sessionID)
		return
	}
	c.mu.Lock()
	c.domClickSessions[sessionID] = true
	c.mu.Unlock()
}

func (c *CDPClient) domClickReady(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.domClickSessions[sessionID]
}

func (c *CDPClient) installExtensionSecureInputBinding(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	enabled := c.OnExtensionInput != nil
	targetURL := c.sessionURLs[sessionID]
	c.mu.Unlock()
	if !enabled {
		return
	}

	const commandTimeout = 2 * time.Second
	if _, err := c.callCommand("Runtime.enable", nil, sessionID, commandTimeout); err != nil {
		cdpDebugf("[ExtensionInput] Runtime.enable failed url=%s: %v\n", targetURL, err)
		return
	}
	if _, err := c.callCommand("Runtime.addBinding", map[string]interface{}{
		"name":                 extensionSecureInputBinding,
		"executionContextName": extensionSecureInputWorld,
	}, sessionID, commandTimeout); err != nil {
		cdpDebugf("[ExtensionInput] Runtime.addBinding failed url=%s: %v\n", targetURL, err)
		return
	}
	if _, err := c.callCommand("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
		"source":    extensionSecureInputScript,
		"worldName": extensionSecureInputWorld,
	}, sessionID, commandTimeout); err != nil {
		cdpDebugf("[ExtensionInput] addScript failed url=%s: %v\n", targetURL, err)
	}
	frameTree, err := c.callCommand("Page.getFrameTree", nil, sessionID, commandTimeout)
	if err != nil {
		cdpDebugf("[ExtensionInput] Page.getFrameTree failed url=%s: %v\n", targetURL, err)
		return
	}

	installed := 0
	for _, frameID := range extensionInputFrameIDs(frameTree) {
		world, err := c.callCommand("Page.createIsolatedWorld", map[string]interface{}{
			"frameId":             frameID,
			"worldName":           extensionSecureInputWorld,
			"grantUniveralAccess": true,
		}, sessionID, commandTimeout)
		if err != nil {
			cdpDebugf("[ExtensionInput] create isolated world failed url=%s frame=%s: %v\n", targetURL, frameID, err)
			continue
		}
		contextID := world["executionContextId"]
		if contextID == nil {
			cdpDebugf("[ExtensionInput] isolated world missing context url=%s frame=%s\n", targetURL, frameID)
			continue
		}
		evalResult, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
			"expression": extensionSecureInputScript,
			"contextId":  contextID,
		}, sessionID, commandTimeout)
		if err != nil {
			cdpDebugf("[ExtensionInput] isolated Runtime.evaluate failed url=%s frame=%s: %v\n", targetURL, frameID, err)
			continue
		}
		if err := extensionInputEvaluationError(evalResult); err != nil {
			cdpDebugf("[ExtensionInput] isolated script rejected url=%s frame=%s: %v\n", targetURL, frameID, err)
			continue
		}
		installed++
	}
	if installed == 0 {
		cdpDebugf("[ExtensionInput] no isolated frame binding installed url=%s session=%s\n", targetURL, sessionID)
		return
	}
	cdpDebugf("[ExtensionInput] isolated binding installed url=%s session=%s frames=%d\n", targetURL, sessionID, installed)
}

func (c *CDPClient) enableExtensionChildTargetAutoAttach(sessionID string) {
	if sessionID == "" {
		return
	}
	if _, err := c.callCommand("Target.setAutoAttach", map[string]interface{}{
		"autoAttach":             true,
		"waitForDebuggerOnStart": false,
		"flatten":                true,
	}, sessionID, time.Second); err != nil {
		cdpDebugf("[CDP] extension child auto-attach failed on port %d: %v\n", c.debugPort, err)
	}
}

func (c *CDPClient) attachDiscoveredExtensionTarget(targetID string, targetType string, targetURL string) {
	if targetID == "" || !isExtensionFocusTarget(targetType, targetURL) {
		return
	}
	go func() {
		if _, err := c.ensureTargetAttached(targetID, targetType, targetURL); err != nil {
			cdpDebugf("[ExtensionInput] target attach failed target=%s url=%s: %v\n", targetID, targetURL, err)
			return
		}
		if isExtensionChildTargetHost(targetType, targetURL) {
			c.ensureExtensionInputFrameTargetsAttached(extensionIDFromURL(targetURL))
		}
	}()
}

func (c *CDPClient) installTabVisibilityBinding(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	alreadyInstalled := c.tabVisibilitySessions[sessionID]
	if !alreadyInstalled {
		c.tabVisibilitySessions[sessionID] = true
	}
	c.mu.Unlock()

	if _, err := c.callCommand("Runtime.enable", nil, sessionID, 2*time.Second); err != nil {
		if !alreadyInstalled {
			c.mu.Lock()
			delete(c.tabVisibilitySessions, sessionID)
			c.mu.Unlock()
		}
		cdpDebugf("[TabVisibility] Runtime.enable failed on port %d: %v\n", c.debugPort, err)
		return
	}
	if !alreadyInstalled {
		if _, err := c.callCommand("Runtime.addBinding", map[string]interface{}{
			"name": tabVisibilityBinding,
		}, sessionID, 2*time.Second); err != nil {
			c.mu.Lock()
			delete(c.tabVisibilitySessions, sessionID)
			c.mu.Unlock()
			cdpDebugf("[TabVisibility] addBinding failed on port %d: %v\n", c.debugPort, err)
			return
		}
		_, _ = c.callCommand("Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{
			"source": tabVisibilityScript,
		}, sessionID, 2*time.Second)
	}
	if _, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    tabVisibilityScript,
		"returnByValue": true,
	}, sessionID, 2*time.Second); err != nil {
		cdpDebugf("[TabVisibility] install failed on port %d: %v\n", c.debugPort, err)
	}
}

type browserPageCandidate struct {
	sessionID string
	targetID  string
	targetURL string
}

func (c *CDPClient) browserTabTargetLocked(tab browserTabDescriptor) browserPageCandidate {
	if tab.TargetID == "" {
		return browserPageCandidate{}
	}
	sessionID := c.targetSessions[tab.TargetID]
	targetURL := c.sessionURLs[sessionID]
	if sessionID == "" || !isActivePageTarget(c.sessionTypes[sessionID], targetURL) {
		return browserPageCandidate{}
	}
	return browserPageCandidate{
		sessionID: sessionID,
		targetID:  tab.TargetID,
		targetURL: targetURL,
	}
}

func (c *CDPClient) applyBrowserTab(tab browserTabDescriptor) {
	c.mu.Lock()
	selected := c.browserTabTargetLocked(tab)
	if selected.sessionID == "" || selected.targetID == "" {
		pending := tab
		c.pendingBrowserTab = &pending
		c.mu.Unlock()
		return
	}
	c.pendingBrowserTab = nil
	changed := c.activeSessionID != selected.sessionID
	c.setActiveSessionLocked(selected.sessionID, selected.targetURL)
	callback := c.OnTabActivated
	c.mu.Unlock()
	go c.refreshViewportMetrics(selected.sessionID)

	if changed && callback != nil {
		go callback(selected.targetID, selected.targetURL)
	}
}

func (c *CDPClient) resolvePendingBrowserTab() {
	c.mu.Lock()
	if c.pendingBrowserTab == nil {
		c.mu.Unlock()
		return
	}
	pending := *c.pendingBrowserTab
	c.mu.Unlock()
	c.applyBrowserTab(pending)
}

func (c *CDPClient) requestBrowserActiveTab() {
	tabs, err := c.browserTabsSnapshot()
	if err != nil {
		cdpDebugf("[TabVisibility] snapshot unavailable on port %d: %v\n", c.debugPort, err)
		return
	}
	for _, tab := range tabs {
		if tab.Active {
			c.applyBrowserTab(tab)
			return
		}
	}
}

func (c *CDPClient) browserTabsSnapshot() ([]browserTabDescriptor, error) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", c.debugPort))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DevTools target list returned HTTP %d", resp.StatusCode)
	}
	var rawTargets []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rawTargets); err != nil {
		return nil, err
	}

	tabs := make([]browserTabDescriptor, 0, len(rawTargets))
	for _, target := range rawTargets {
		targetID := target.ID
		targetType := target.Type
		targetURL := target.URL
		if targetID == "" || !isActivePageTarget(targetType, targetURL) {
			continue
		}
		sessionID := c.getSessionIDByTargetID(targetID)
		if sessionID == "" {
			var attachErr error
			sessionID, attachErr = c.ensureTargetAttached(targetID, targetType, targetURL)
			if attachErr != nil {
				continue
			}
		}
		if sessionID == "" {
			continue
		}
		go c.installTabVisibilityBinding(sessionID)

		active := false
		visibility, visibilityErr := c.callCommand("Runtime.evaluate", map[string]interface{}{
			"expression":    `document.visibilityState === "visible"`,
			"returnByValue": true,
		}, sessionID, 2*time.Second)
		if visibilityErr == nil {
			if remoteObject, ok := visibility["result"].(map[string]interface{}); ok {
				active, _ = remoteObject["value"].(bool)
			}
		}

		tab := browserTabDescriptor{
			Index:    len(tabs),
			URL:      targetURL,
			TargetID: targetID,
			Active:   active,
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

func (c *CDPClient) handleMessage(msg map[string]interface{}) {
	// 1. Check if it's a Response to a Request
	if idVal, ok := msg["id"].(float64); ok { // JSON numbers are floats
		id := int(idVal)
		c.mu.Lock()
		if ch, exists := c.pendingRequests[id]; exists {
			ch <- msg
			delete(c.pendingRequests, id)
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
	}

	// 2. Handle Events. Target lifecycle callbacks stay in wire order; their
	// handlers fan out any slower work themselves.
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]interface{})

	if method == "Target.attachedToTarget" {
		if sessID, ok := params["sessionId"].(string); ok {
			// Chrome can leave a WebUI-created page paused even when the event's
			// waitingForDebugger flag is false. This command is a no-op for running
			// targets and must precede all target setup so window.open can return.
			_ = c.sendCommandNoWait("Runtime.runIfWaitingForDebugger", nil, sessID)
			// Check target type and URL
			isPage := false
			targetURL := ""
			targetId := ""
			targetType := ""

			if targetInfo, ok := params["targetInfo"].(map[string]interface{}); ok {
				if typeVal, ok := targetInfo["type"].(string); ok {
					targetType = typeVal
					if typeVal == "page" {
						isPage = true
					}
				}
				if id, ok := targetInfo["targetId"].(string); ok {
					targetId = id
				}
				// Check URL to determine if it's a main frame or iframe
				if url, ok := targetInfo["url"].(string); ok {
					targetURL = url
				}
			}
			openerTargetID := ""
			if targetInfo, ok := params["targetInfo"].(map[string]interface{}); ok {
				openerTargetID, _ = targetInfo["openerId"].(string)
			}

			if targetId != "" {
				c.mu.Lock()
				if _, destroyed := c.destroyedTargets[targetId]; destroyed {
					c.mu.Unlock()
					return
				}
				c.sessionURLs[sessID] = targetURL
				c.sessionCommittedURLs[sessID] = targetURL
				c.sessionTargets[sessID] = targetId
				c.sessionTypes[sessID] = targetType
				c.targetSessions[targetId] = sessID
				if isExtensionFocusTarget(targetType, targetURL) && !containsString(c.sessionOrder, sessID) {
					c.sessionOrder = append(c.sessionOrder, sessID)
				}
				c.mu.Unlock()
			}

			if (targetType == "service_worker" && isChromeManagerZoomExtensionURL(targetURL)) || (targetType == "page" && isChromeManagerZoomExtensionControlURL(targetURL)) {
				c.mu.Lock()
				c.extensionSessionID = sessID
				c.extensionTargetID = targetId
				c.extensionURL = targetURL
				c.mu.Unlock()
				cdpDebugf("[CDP] ChromeManager zoom extension attached on port %d\n", c.debugPort)
			}

			if isExtensionChildTargetHost(targetType, targetURL) {
				go c.enableExtensionChildTargetAutoAttach(sessID)
			}
			if isExtensionFocusTarget(targetType, targetURL) {
				go c.installExtensionSecureInputBinding(sessID)
				go c.installDOMClickBinding(sessID)
			}

			if isExtensionURL(targetURL) {
				if isVisibleExtensionTarget(targetType, targetURL) {
					c.mu.Lock()
					c.sessions[sessID] = true
					if targetId != "" {
						c.knownPageTargets[targetId] = true
						c.lastVisibleExtensionTargetID = targetId
					}
					c.lastVisibleExtensionURL = targetURL
					callback := c.OnTargetCreated
					c.mu.Unlock()
					c.signalTargetEvent()
					if callback != nil && shouldNotifyTargetCreated(targetType, targetURL) {
						callback(targetId, openerTargetID, targetType, targetURL)
					}
					cdpDebugf("CDP Extension Target Attached: %s (Type: %s URL: %s)\n", sessID[:8], targetType, targetURL)
					return
				}
				if targetType != "page" {
					cdpDebugf("CDP Extension Target Attached: %s (Type: %s URL: %s) -> IGNORED\n", sessID[:8], targetType, targetURL)
					return
				}
			}

			if isPage {
				internalPage := isInternalPageURL(targetURL)

				c.mu.Lock()
				c.sessionURLs[sessID] = targetURL
				c.sessionTargets[sessID] = targetId
				c.sessionTypes[sessID] = targetType
				if targetId != "" {
					c.targetSessions[targetId] = sessID
				}
				if !internalPage && !containsString(c.sessionOrder, sessID) {
					c.sessionOrder = append(c.sessionOrder, sessID)
				}
				c.mu.Unlock()

				if !internalPage {
					_ = c.sendCommandNoWait("Page.enable", nil, sessID)
					go c.installDOMClickBinding(sessID)
				}
				if isActivePageTarget(targetType, targetURL) {
					go c.installTabVisibilityBinding(sessID)
				}

				// Ignore internal UI popups entirely
				if internalPage {
					cdpDebugf("CDP Session Attached: %s (URL: %s) -> IGNORED (Internal)\n", sessID[:8], targetURL)
				} else {
					c.mu.Lock()
					c.sessions[sessID] = true
					if targetId != "" && !isBlankTargetURL(targetURL) {
						c.knownPageTargets[targetId] = true // Register as a genuine page target
					}

					// Chrome owns visual activation. The visibility binding reports the
					// exact active target, including chrome:// pages.
					cdpDebugf("CDP Session Attached: %s (URL: %s) -> Stored\n", sessID[:8], targetURL)
					c.mu.Unlock()
					c.signalTargetEvent()
					go c.resolvePendingBrowserTab()
				}
			}
		}
	} else if method == "Target.targetCreated" {
		if targetInfo, ok := params["targetInfo"].(map[string]interface{}); ok {
			typeVal, _ := targetInfo["type"].(string)
			url, _ := targetInfo["url"].(string)
			openerTargetID, _ := targetInfo["openerId"].(string)
			if (typeVal == "service_worker" && isChromeManagerZoomExtensionURL(url)) || (typeVal == "page" && isChromeManagerZoomExtensionControlURL(url)) {
				if id, ok := targetInfo["targetId"].(string); ok {
					c.mu.Lock()
					c.extensionTargetID = id
					c.extensionURL = url
					c.mu.Unlock()
				}
			}
			id, _ := targetInfo["targetId"].(string)
			if id == "" {
				return
			}
			c.mu.Lock()
			delete(c.destroyedTargets, id)
			c.targets[id] = url
			c.mu.Unlock()

			if !isSupportedTarget(typeVal, url) || isIgnoredCDPTargetURL(url) {
				return
			}
			c.attachDiscoveredExtensionTarget(id, typeVal, url)

			c.mu.Lock()
			if !isBlankTargetURL(url) {
				c.knownPageTargets[id] = true
			}
			if isVisibleExtensionTarget(typeVal, url) {
				c.lastVisibleExtensionTargetID = id
				c.lastVisibleExtensionURL = url
			}
			initStatus := c.isInitialized
			callback := c.OnTargetCreated
			c.mu.Unlock()
			c.signalTargetEvent()

			_, isExtensionPopup := extensionPopupID(url)
			isVisibleExtension := isVisibleExtensionTarget(typeVal, url)
			isExtensionFullPage := isExtensionFullPageTarget(typeVal, url)
			if callback != nil && shouldNotifyTargetCreated(typeVal, url) && (initStatus || isExtensionPopup || isVisibleExtension || isExtensionFullPage) {
				callback(id, openerTargetID, typeVal, url)
			} else if !initStatus {
				cdpDebugf("[CDP] Ignored TargetCreated (burst during init): %s\n", url)
			}
		}
	} else if method == "Target.targetInfoChanged" {
		if targetInfo, ok := params["targetInfo"].(map[string]interface{}); ok {
			id, _ := targetInfo["targetId"].(string)
			targetType, _ := targetInfo["type"].(string)
			targetURL, _ := targetInfo["url"].(string)
			openerTargetID, _ := targetInfo["openerId"].(string)
			if id == "" {
				return
			}

			c.mu.Lock()
			if _, destroyed := c.destroyedTargets[id]; destroyed {
				c.mu.Unlock()
				return
			}
			wasKnown := c.knownPageTargets[id]
			sessionID := c.targetSessions[id]
			previousURL := ""
			if sessionID != "" {
				previousURL = c.sessionURLs[sessionID]
				c.sessionURLs[sessionID] = targetURL
				c.sessionTypes[sessionID] = targetType
				if previousURL != targetURL {
					delete(c.domClickSessions, sessionID)
				}
			}
			c.targets[id] = targetURL
			c.mu.Unlock()

			if !isSupportedTarget(targetType, targetURL) || isIgnoredCDPTargetURL(targetURL) {
				return
			}
			c.attachDiscoveredExtensionTarget(id, targetType, targetURL)

			c.mu.Lock()
			if !isBlankTargetURL(targetURL) {
				c.knownPageTargets[id] = true
			}
			if isVisibleExtensionTarget(targetType, targetURL) {
				c.lastVisibleExtensionTargetID = id
				c.lastVisibleExtensionURL = targetURL
			}
			initStatus := c.isInitialized
			callback := c.OnTargetCreated
			c.mu.Unlock()
			c.signalTargetEvent()

			isVisibleExtension := isVisibleExtensionTarget(targetType, targetURL)
			notifyCreated := shouldNotifyTargetInfoChanged(wasKnown, targetType, previousURL, targetURL)

			_, isExtensionPopup := extensionPopupID(targetURL)
			isExtensionFullPage := isExtensionFullPageTarget(targetType, targetURL)
			if notifyCreated && callback != nil && shouldNotifyTargetCreated(targetType, targetURL) && (initStatus || isExtensionPopup || isVisibleExtension || isExtensionFullPage) {
				callback(id, openerTargetID, targetType, targetURL)
			}
			go c.resolvePendingBrowserTab()
		}
	} else if method == "Target.targetDestroyed" {
		if targetId, ok := params["targetId"].(string); ok {
			c.mu.Lock()
			now := time.Now()
			c.destroyedTargets[targetId] = now
			if len(c.destroyedTargets) > 256 {
				for id, destroyedAt := range c.destroyedTargets {
					if now.Sub(destroyedAt) > 30*time.Second {
						delete(c.destroyedTargets, id)
					}
				}
			}
			targetURL := c.targets[targetId]
			if targetURL == "" {
				if sessionID := c.targetSessions[targetId]; sessionID != "" {
					targetURL = c.sessionURLs[sessionID]
				}
			}
			delete(c.targets, targetId)
			// Only emit the callback for known genuine pages, but always remove the
			// session. Extension OOPIF targets are not genuine pages and otherwise
			// leave stale sessions that can receive input after a popup is reopened.
			known := c.knownPageTargets[targetId]
			if known {
				delete(c.knownPageTargets, targetId)
			}
			if c.lastVisibleExtensionTargetID == targetId {
				c.lastVisibleExtensionTargetID = ""
				c.lastVisibleExtensionURL = ""
			}
			if sessionID := c.targetSessions[targetId]; sessionID != "" {
				wasActive := c.activeSessionID == sessionID
				if c.lastActivePageSessionID == sessionID {
					c.lastActivePageSessionID = ""
				}
				delete(c.sessions, sessionID)
				delete(c.sessionURLs, sessionID)
				delete(c.sessionCommittedURLs, sessionID)
				delete(c.sessionTargets, sessionID)
				delete(c.sessionTypes, sessionID)
				delete(c.sessionLoaderIDs, sessionID)
				delete(c.viewportMetricsBySession, sessionID)
				delete(c.domClickSessions, sessionID)
				delete(c.tabVisibilitySessions, sessionID)
				delete(c.targetSessions, targetId)
				c.sessionOrder = removeString(c.sessionOrder, sessionID)
				if wasActive {
					c.restoreLastActivePageSessionLocked()
				}
			}
			callback := c.OnTargetDestroyed
			c.mu.Unlock()
			c.signalTargetEvent()
			if known && callback != nil {
				callback(targetId, targetURL)
			}
		}
	} else if method == "Target.detachedFromTarget" {
		if sessID, ok := params["sessionId"].(string); ok {
			c.mu.Lock()
			delete(c.sessions, sessID)
			delete(c.sessionURLs, sessID)
			delete(c.sessionCommittedURLs, sessID)
			targetID := c.sessionTargets[sessID]
			delete(c.sessionTargets, sessID)
			delete(c.sessionTypes, sessID)
			delete(c.sessionLoaderIDs, sessID)
			delete(c.viewportMetricsBySession, sessID)
			delete(c.domClickSessions, sessID)
			delete(c.tabVisibilitySessions, sessID)
			if targetID != "" {
				delete(c.targetSessions, targetID)
			}
			if c.extensionSessionID == sessID {
				c.extensionSessionID = ""
				c.extensionTargetID = ""
			}
			// Remove from order tracking
			c.sessionOrder = removeString(c.sessionOrder, sessID)
			wasActive := c.activeSessionID == sessID
			if c.lastActivePageSessionID == sessID {
				c.lastActivePageSessionID = ""
			}
			if wasActive {
				c.restoreLastActivePageSessionLocked()
				cdpDebugf("CDP Session Detached: %s. Restored active page session %s\n", sessID, c.activeSessionID)
			}
			c.mu.Unlock()
			c.signalTargetEvent()
		}
	} else if method == "Page.frameResized" {
		if sessID, ok := msg["sessionId"].(string); ok {
			c.mu.Lock()
			targetURL := c.sessionURLs[sessID]
			targetType := c.sessionTypes[sessID]
			c.mu.Unlock()
			if isActivePageTarget(targetType, targetURL) || isExtensionFocusTarget(targetType, targetURL) {
				go c.refreshViewportMetrics(sessID)
			}
		}
	} else if method == "Page.loadEventFired" {
		if sessID, ok := msg["sessionId"].(string); ok {
			c.mu.Lock()
			targetID := c.sessionTargets[sessID]
			targetURL := c.sessionURLs[sessID]
			targetType := c.sessionTypes[sessID]
			domClickReady := c.domClickSessions[sessID]
			loadedCallback := c.OnTargetLoaded
			c.mu.Unlock()
			if !domClickReady && supportsDOMClickBinding("page", targetURL) {
				go c.installDOMClickBinding(sessID)
			}
			if isActivePageTarget(targetType, targetURL) {
				go c.installTabVisibilityBinding(sessID)
			}
			if loadedCallback != nil {
				loadedCallback(targetID, targetURL)
			}
		}
	} else if method == "Page.frameNavigated" {
		if frame, ok := params["frame"].(map[string]interface{}); ok {
			// Only trigger for top-level frame (no parentId)
			if _, hasParent := frame["parentId"]; !hasParent {
				if url, ok := frame["url"].(string); ok {
					if sessID, ok := msg["sessionId"].(string); ok {
						loaderID, _ := frame["loaderId"].(string)
						c.mu.Lock()
						previousURL := c.sessionCommittedURLs[sessID]
						if previousURL == "" {
							previousURL = c.sessionURLs[sessID]
						}
						previousLoaderID := c.sessionLoaderIDs[sessID]
						initialized := c.isInitialized
						c.sessionURLs[sessID] = url
						c.sessionCommittedURLs[sessID] = url
						delete(c.domClickSessions, sessID)
						delete(c.viewportMetricsBySession, sessID)
						if loaderID != "" {
							c.sessionLoaderIDs[sessID] = loaderID
						}
						if targetID := c.sessionTargets[sessID]; targetID != "" {
							c.targets[targetID] = url
						}
						if sessID == c.activeSessionID {
							c.activeSessionURL = url
						}
						reloadCallback := c.OnURLReloaded
						c.mu.Unlock()
						if !isBlankTargetURL(url) && !isIgnoredCDPTargetURL(url) {
							go c.installDOMClickBinding(sessID)
						}
						if isActivePageTarget("page", url) {
							go c.installTabVisibilityBinding(sessID)
							go c.refreshViewportMetrics(sessID)
						}
						if reloadCallback != nil &&
							initialized &&
							previousURL == url &&
							!isBlankTargetURL(url) &&
							!isIgnoredCDPTargetURL(url) &&
							loaderID != "" &&
							previousLoaderID != "" &&
							loaderID != previousLoaderID {
							go reloadCallback(sessID, url)
						}
						_, isExtensionPopup := extensionPopupID(url)
						if c.OnNavigate != nil && !isExtensionPopup {
							c.OnNavigate(sessID, url)
						}
					}
				}
			}
		}
	} else if method == "Runtime.bindingCalled" {
		name, _ := params["name"].(string)
		if name == tabVisibilityBinding {
			sessionID, _ := msg["sessionId"].(string)
			c.mu.Lock()
			targetID := c.sessionTargets[sessionID]
			targetURL := c.sessionURLs[sessionID]
			targetType := c.sessionTypes[sessionID]
			c.mu.Unlock()
			if sessionID != "" && targetID != "" && isActivePageTarget(targetType, targetURL) {
				c.applyBrowserTab(browserTabDescriptor{Index: -1, TargetID: targetID, URL: targetURL, Active: true})
			}
		} else if name == extensionSecureInputBinding {
			payload, _ := params["payload"].(string)
			state := extensionEditableState{}
			if payload == "" || json.Unmarshal([]byte(payload), &state) != nil {
				return
			}
			if state.TargetURL == "" {
				if sessID, ok := msg["sessionId"].(string); ok {
					c.mu.Lock()
					state.TargetURL = c.sessionURLs[sessID]
					c.mu.Unlock()
				}
			}
			c.mu.Lock()
			callback := c.OnExtensionInput
			c.mu.Unlock()
			if callback != nil && state.TargetURL != "" {
				go callback(state)
			}
		} else if name == domClickBinding {
			payload, _ := params["payload"].(string)
			action := DOMClickAction{}
			sessionID, _ := msg["sessionId"].(string)
			if payload == "" || sessionID == "" || json.Unmarshal([]byte(payload), &action) != nil {
				return
			}
			c.mu.Lock()
			action.SourceURL = c.sessionURLs[sessionID]
			callback := c.OnDOMClick
			c.mu.Unlock()
			if callback != nil {
				callback(sessionID, action)
			}
		}
	}
}

// sendCommand sends a raw command (Synchronous used by Connect)
func (c *CDPClient) sendCommand(method string, params map[string]interface{}) error {
	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":     id,
		"method": method,
		"params": params,
	}
	return c.sendJSON(c.ws, cmd)
}

func (c *CDPClient) sendCommandNoWait(method string, params map[string]interface{}, sessionID string) error {
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return fmt.Errorf("not connected")
	}
	c.msgID++
	id := c.msgID
	ws := c.ws
	c.mu.Unlock()

	cmd := map[string]interface{}{
		"id":     id,
		"method": method,
	}
	if params != nil {
		cmd["params"] = params
	}
	if sessionID != "" {
		cmd["sessionId"] = sessionID
	}
	return c.sendJSON(ws, cmd)
}

func (c *CDPClient) callCommand(method string, params map[string]interface{}, sessionID string, timeout time.Duration) (map[string]interface{}, error) {
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}

	c.msgID++
	id := c.msgID
	ch := make(chan map[string]interface{}, 1)
	c.pendingRequests[id] = ch
	ws := c.ws
	c.mu.Unlock()

	cmd := map[string]interface{}{
		"id":     id,
		"method": method,
	}
	if params != nil {
		cmd["params"] = params
	}
	if sessionID != "" {
		cmd["sessionId"] = sessionID
	}

	if err := c.sendJSON(ws, cmd); err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, err
	}

	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	select {
	case response := <-ch:
		if errObj, ok := response["error"].(map[string]interface{}); ok {
			if msg, ok := errObj["message"].(string); ok {
				return nil, fmt.Errorf("%s failed: %s", method, msg)
			}
			return nil, fmt.Errorf("%s failed: %v", method, errObj)
		}
		result, _ := response["result"].(map[string]interface{})
		return result, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s timeout", method)
	}
}

// Close closes the connection
func (c *CDPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws != nil {
		c.ws.Close()
		c.ws = nil
	}
	c.isConnected = false
}

// Navigate sends a Page.navigate command to the active session.
func (c *CDPClient) Navigate(url string) error {
	_, sessionID, _ := c.activeRouteSnapshot()
	if sessionID == "" {
		return nil
	}
	return c.NavigateSession(sessionID, url)
}

func (c *CDPClient) NavigateSession(sessionID, url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}
	if sessionID == "" || c.sessionTargets[sessionID] == "" {
		return fmt.Errorf("page session is unavailable")
	}

	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Page.navigate",
		"params":    map[string]interface{}{"url": url},
		"sessionId": sessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

// CreateTarget sends Target.createTarget
func (c *CDPClient) CreateTarget(url string) (string, error) {
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return "", fmt.Errorf("not connected")
	}

	c.msgID++
	id := c.msgID
	// If URL is empty, open a blank page
	if url == "" {
		url = "chrome://newtab/"
	}
	ch := make(chan map[string]interface{}, 1)
	c.pendingRequests[id] = ch

	cmd := map[string]interface{}{
		"id":     id,
		"method": "Target.createTarget",
		"params": map[string]interface{}{
			"url": url,
		},
	}

	ws := c.ws
	c.mu.Unlock()

	if err := c.sendJSON(ws, cmd); err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return "", err
	}

	// Wait for response via channel
	select {
	case response := <-ch:
		if resultParams, ok := response["result"].(map[string]interface{}); ok {
			if targetId, ok := resultParams["targetId"].(string); ok {
				return targetId, nil
			}
		}
		return "", fmt.Errorf("invalid response structure: %v", response)
	case <-time.After(2 * time.Second):
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return "", fmt.Errorf("timeout")
	}
}

// ActivateTargetByTargetId activates the tab given a specific targetId
func (c *CDPClient) ActivateTargetByTargetId(targetId string) error {
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return fmt.Errorf("not connected")
	}

	id := c.msgID + 1
	c.msgID = id
	ws := c.ws
	c.mu.Unlock()

	cmd := map[string]interface{}{
		"id":     id,
		"method": "Target.activateTarget",
		"params": map[string]interface{}{"targetId": targetId},
	}
	if err := c.sendJSON(ws, cmd); err != nil {
		return err
	}
	c.setActiveTarget(targetId)
	return nil
}

func (c *CDPClient) backgroundTabActivationState(targetID string) (exists, alreadyActive bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sessionID := c.targetSessions[targetID]
	return sessionID != "", sessionID != "" && sessionID == c.activeSessionID
}

// ActivateTabByTargetIDInBackground changes the active Chrome tab without
// activating the Chrome application or raising its native window on macOS.
func (c *CDPClient) ActivateTabByTargetIDInBackground(targetID string) error {
	return c.activateTabByTargetIDInBackground(targetID, false)
}

func (c *CDPClient) reactivateTabByTargetIDInBackground(targetID string) error {
	return c.activateTabByTargetIDInBackground(targetID, true)
}

func (c *CDPClient) activateTabByTargetIDInBackground(targetID string, force bool) error {
	exists, alreadyActive := c.backgroundTabActivationState(targetID)
	if !exists {
		return fmt.Errorf("target not found for %s", targetID)
	}
	if alreadyActive && !force {
		return nil
	}

	_, err := c.callCommand("Target.activateTarget", map[string]interface{}{
		"targetId": targetID,
	}, "", 2*time.Second)
	if err != nil {
		return err
	}

	c.setActiveTarget(targetID)
	return nil
}

// CloseCurrentTarget sends Target.closeTarget for the active session
func (c *CDPClient) CloseCurrentTarget() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil || c.activeSessionID == "" {
		return fmt.Errorf("not connected or no active session")
	}

	c.msgID++
	id := c.msgID

	// NOTE: CloseTarget usually requires the targetId, which we need to track.
	// For simplicity, we can just send Page.close to the active session.
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Page.close",
		"params":    map[string]interface{}{},
		"sessionId": c.activeSessionID,
	}
	return c.sendJSON(c.ws, cmd)
}

// CloseTargetById sends Target.closeTarget for a specific targetId
func (c *CDPClient) CloseTargetById(targetId string) error {
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return fmt.Errorf("not connected")
	}

	id := c.msgID + 1
	c.msgID = id
	ws := c.ws
	c.mu.Unlock()

	cmd := map[string]interface{}{
		"id":     id,
		"method": "Target.closeTarget",
		"params": map[string]interface{}{"targetId": targetId},
	}
	return c.sendJSON(ws, cmd)
}

func (c *CDPClient) getSessionIDByTargetID(targetID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.targetSessions[targetID]
}

func (c *CDPClient) getTargetIDBySessionID(sessionID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionTargets[sessionID]
}

func (c *CDPClient) getURLForTarget(targetID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if targetID == "" {
		return ""
	}
	if targetURL := c.targets[targetID]; targetURL != "" {
		return targetURL
	}
	return c.sessionURLs[c.targetSessions[targetID]]
}

func (c *CDPClient) debugSessionURL(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionURLs[sessionID]
}

func (c *CDPClient) exactExtensionSessionID(targetURL string) string {
	extensionID := extensionIDFromURL(targetURL)
	if extensionID == "" {
		return ""
	}
	targetKey := extensionPageMatchKey(targetURL)

	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		currentURL := c.sessionURLs[sessionID]
		if currentURL == targetURL && isExtensionFocusTarget(c.sessionTypes[sessionID], currentURL) {
			return sessionID
		}
	}
	if targetKey == "" {
		return ""
	}
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		currentURL := c.sessionURLs[sessionID]
		if extensionIDFromURL(currentURL) == extensionID &&
			extensionPageMatchKey(currentURL) == targetKey &&
			isExtensionFocusTarget(c.sessionTypes[sessionID], currentURL) {
			return sessionID
		}
	}
	return ""
}

func (c *CDPClient) extensionRouteSnapshot(targetURL string) (targetID, sessionID string) {
	extensionID := extensionIDFromURL(targetURL)
	if extensionID == "" {
		return "", ""
	}
	targetKey := extensionPageMatchKey(targetURL)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeSessionID != "" {
		activeURL := c.sessionURLs[c.activeSessionID]
		if extensionIDFromURL(activeURL) == extensionID &&
			targetKey != "" && extensionPageMatchKey(activeURL) == targetKey &&
			isExtensionFullPageTarget(c.sessionTypes[c.activeSessionID], activeURL) {
			return c.sessionTargets[c.activeSessionID], c.activeSessionID
		}
	}
	if c.lastVisibleExtensionTargetID != "" && extensionIDFromURL(c.lastVisibleExtensionURL) == extensionID {
		sessionID = c.targetSessions[c.lastVisibleExtensionTargetID]
		if sessionID != "" {
			return c.lastVisibleExtensionTargetID, sessionID
		}
	}
	if c.activeSessionID != "" {
		activeURL := c.sessionURLs[c.activeSessionID]
		if extensionIDFromURL(activeURL) == extensionID && isExtensionFullPageTarget(c.sessionTypes[c.activeSessionID], activeURL) {
			return c.sessionTargets[c.activeSessionID], c.activeSessionID
		}
	}
	return "", ""
}

func (c *CDPClient) ensureExtensionInputFrameTargetsAttached(extensionID string) {
	if extensionID == "" {
		return
	}

	result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
	if err != nil {
		return
	}

	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || extensionIDFromURL(targetURL) != extensionID {
			continue
		}
		if !isExtensionInputFrameTarget(targetType, targetURL) {
			continue
		}
		_, _ = c.ensureTargetAttached(targetID, targetType, targetURL)
	}
}

type extensionEditableState struct {
	Kind           string `json:"kind"`
	TargetURL      string `json:"targetURL"`
	InputType      string `json:"inputType"`
	InputData      string `json:"data"`
	Value          string `json:"value"`
	SelectionStart int    `json:"selectionStart"`
	SelectionEnd   int    `json:"selectionEnd"`
	EnterSeq       int    `json:"enterSeq"`
	ElementID      string `json:"elementID"`
	ElementName    string `json:"elementName"`
	ElementTag     string `json:"elementTag"`
	ElementType    string `json:"elementType"`
	ElementIndex   int    `json:"elementIndex"`
}

func (c *CDPClient) setActiveSession(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	targetURL := c.sessionURLs[sessionID]
	if !isActivePageTarget(c.sessionTypes[sessionID], targetURL) {
		return
	}
	c.setActiveSessionLocked(sessionID, targetURL)
}

func (c *CDPClient) setActiveSessionLocked(sessionID string, targetURL string) {
	c.activeSessionID = sessionID
	if targetURL != "" {
		c.activeSessionURL = targetURL
	}
	if !isExtensionURL(targetURL) && isActivePageTarget(c.sessionTypes[sessionID], targetURL) {
		c.lastActivePageSessionID = sessionID
	}
}

func (c *CDPClient) restoreLastActivePageSessionLocked() {
	sessionID := c.lastActivePageSessionID
	targetURL := c.sessionURLs[sessionID]
	if sessionID != "" && !isExtensionURL(targetURL) && isActivePageTarget(c.sessionTypes[sessionID], targetURL) {
		c.activeSessionID = sessionID
		c.activeSessionURL = targetURL
		return
	}
	c.lastActivePageSessionID = ""
	c.activeSessionID = ""
	c.activeSessionURL = ""
}

func (c *CDPClient) setActiveTarget(targetID string) {
	if targetID == "" {
		return
	}
	c.mu.Lock()
	sessionID := c.targetSessions[targetID]
	c.mu.Unlock()
	c.setActiveSession(sessionID)
}

func isExtensionEditInputType(inputType string) bool {
	inputType = strings.TrimSpace(inputType)
	return strings.HasPrefix(inputType, "insert") || strings.HasPrefix(inputType, "delete")
}

func (c *CDPClient) ApplyExtensionEditToSession(sessionID string, state extensionEditableState) error {
	if sessionID == "" {
		return nil
	}
	if !isExtensionEditInputType(state.InputType) {
		return fmt.Errorf("unsupported extension input type: %s", strings.TrimSpace(state.InputType))
	}

	encodedState, _ := json.Marshal(state)
	expression := fmt.Sprintf(`((state) => {
  const isEditable = (element) => {
    if (!element || element.nodeType !== 1) return false;
    const tag = String(element.tagName || "").toUpperCase();
    return element.isContentEditable || tag === "INPUT" || tag === "TEXTAREA";
  };
  const fields = Array.from(document.querySelectorAll('input[type="password"],textarea,[contenteditable="true"]'));
  const wantedID = state.elementID || "";
  const wantedName = state.elementName || "";
  const wantedTag = String(state.elementTag || "").toUpperCase();
  const wantedType = String(state.elementType || "").toLowerCase();
  const wantedIndex = Number(state.elementIndex);
  let target = null;

  if (wantedID) {
    const byID = document.getElementById(wantedID);
    if (isEditable(byID)) target = byID;
  }
  if (!target && wantedIndex >= 0 && wantedIndex < fields.length) {
    const byIndex = fields[wantedIndex];
    const tagMatches = !wantedTag || String(byIndex.tagName || "").toUpperCase() === wantedTag;
    const typeMatches = !wantedType || String(byIndex.type || "").toLowerCase() === wantedType;
    if (tagMatches && typeMatches) target = byIndex;
  }
  if (!target && wantedName) {
    target = fields.find((element) => isEditable(element) && String(element.name || "") === wantedName) || null;
  }
  if (!target && wantedType) {
    target = fields.find((element) => isEditable(element) && String(element.type || "").toLowerCase() === wantedType) || null;
  }
  if (!target && isEditable(document.activeElement)) target = document.activeElement;
  if (!target) return {ok: false, reason: "target-not-found"};

  try { target.focus({preventScroll: true}); } catch (_) { try { target.focus(); } catch (_) {} }
  const nextValue = String(state.value || "");
  const currentValue = target.isContentEditable ? String(target.textContent || "") : String(target.value || "");
  const changed = currentValue !== nextValue;
  if (changed) {
    if (target.isContentEditable) {
      target.textContent = nextValue;
    } else {
      const prototype = Object.getPrototypeOf(target);
      const descriptor = prototype && Object.getOwnPropertyDescriptor(prototype, "value");
      if (descriptor && typeof descriptor.set === "function") descriptor.set.call(target, nextValue);
      else target.value = nextValue;
    }
  }

  const selectionStart = Number(state.selectionStart);
  const selectionEnd = Number(state.selectionEnd);
  if (!target.isContentEditable && target.setSelectionRange && selectionStart >= 0 && selectionEnd >= 0) {
    target.setSelectionRange(selectionStart, selectionEnd);
  }
  if (changed) {
    let inputEvent;
    try {
      inputEvent = new InputEvent("input", {
        bubbles: true,
        composed: true,
        inputType: state.inputType || "",
        data: typeof state.data === "string" ? state.data : null
      });
    } catch (_) {
      inputEvent = new Event("input", {bubbles: true, composed: true});
    }
    target.dispatchEvent(inputEvent);
  }
  return {ok: true, changed};
})(%s)`, string(encodedState))

	result, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
	}, sessionID, 800*time.Millisecond)
	if err != nil {
		return err
	}
	if err := extensionInputEvaluationError(result); err != nil {
		return err
	}
	evalResult, _ := result["result"].(map[string]interface{})
	value, _ := evalResult["value"].(map[string]interface{})
	if ok, _ := value["ok"].(bool); !ok {
		reason, _ := value["reason"].(string)
		return fmt.Errorf("extension input target unavailable: %s", reason)
	}
	return nil
}

func (c *CDPClient) ensureTargetAttached(targetID string, targetType string, targetURL string) (string, error) {
	if targetID == "" {
		return "", fmt.Errorf("empty target id")
	}

	if sessionID := c.getSessionIDByTargetID(targetID); sessionID != "" {
		c.mu.Lock()
		c.sessionURLs[sessionID] = targetURL
		c.sessionTypes[sessionID] = targetType
		c.knownPageTargets[targetID] = true
		if isExtensionFocusTarget(targetType, targetURL) {
			c.sessionOrder = removeString(c.sessionOrder, sessionID)
			c.sessionOrder = append(c.sessionOrder, sessionID)
		}
		if isVisibleExtensionTarget(targetType, targetURL) {
			c.lastVisibleExtensionTargetID = targetID
			c.lastVisibleExtensionURL = targetURL
		}
		c.mu.Unlock()
		if isExtensionChildTargetHost(targetType, targetURL) {
			go c.enableExtensionChildTargetAutoAttach(sessionID)
		}
		if isExtensionFocusTarget(targetType, targetURL) {
			go c.installExtensionSecureInputBinding(sessionID)
			go c.refreshViewportMetrics(sessionID)
			go c.installDOMClickBinding(sessionID)
		} else if targetType == "page" && !isIgnoredCDPTargetURL(targetURL) {
			go c.installDOMClickBinding(sessionID)
		}
		if isActivePageTarget(targetType, targetURL) {
			go c.installTabVisibilityBinding(sessionID)
			go c.refreshViewportMetrics(sessionID)
		}
		return sessionID, nil
	}

	attachResult, err := c.callCommand("Target.attachToTarget", map[string]interface{}{
		"targetId": targetID,
		"flatten":  true,
	}, "", 2*time.Second)
	if err != nil {
		if sessionID := c.getSessionIDByTargetID(targetID); sessionID != "" {
			return sessionID, nil
		}
		return "", err
	}

	sessionID, _ := attachResult["sessionId"].(string)
	if sessionID == "" {
		return "", fmt.Errorf("Target.attachToTarget returned empty session for %s", targetID)
	}

	c.mu.Lock()
	c.sessionTargets[sessionID] = targetID
	c.sessionURLs[sessionID] = targetURL
	c.sessionTypes[sessionID] = targetType
	c.targetSessions[targetID] = sessionID
	c.sessions[sessionID] = true
	c.knownPageTargets[targetID] = true
	_, isExtensionPopup := extensionPopupID(targetURL)
	if (isExtensionFocusTarget(targetType, targetURL) || (targetType == "page" && !isExtensionPopup)) && !containsString(c.sessionOrder, sessionID) {
		c.sessionOrder = append(c.sessionOrder, sessionID)
	}
	if isVisibleExtensionTarget(targetType, targetURL) {
		c.lastVisibleExtensionTargetID = targetID
		c.lastVisibleExtensionURL = targetURL
	}
	if targetType == "page" && c.activeSessionID == "" && !isExtensionPopup {
		c.setActiveSessionLocked(sessionID, targetURL)
	}
	c.mu.Unlock()

	_, _ = c.callCommand("Runtime.enable", nil, sessionID, 2*time.Second)
	if isExtensionChildTargetHost(targetType, targetURL) {
		go c.enableExtensionChildTargetAutoAttach(sessionID)
	}
	if isExtensionFocusTarget(targetType, targetURL) {
		go c.installExtensionSecureInputBinding(sessionID)
		go c.refreshViewportMetrics(sessionID)
		go c.installDOMClickBinding(sessionID)
	} else if targetType == "page" && !isIgnoredCDPTargetURL(targetURL) {
		go c.installDOMClickBinding(sessionID)
	}
	if isActivePageTarget(targetType, targetURL) {
		go c.installTabVisibilityBinding(sessionID)
		go c.refreshViewportMetrics(sessionID)
	}
	return sessionID, nil
}

func (c *CDPClient) ensureExtensionTargetsAttached(extensionID string) {
	if extensionID == "" {
		return
	}

	result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
	if err != nil {
		return
	}

	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || extensionIDFromURL(targetURL) != extensionID {
			continue
		}
		if !isVisibleExtensionTarget(targetType, targetURL) {
			continue
		}
		_, _ = c.ensureTargetAttached(targetID, targetType, targetURL)
	}
}

func (c *CDPClient) ensureVisibleExtensionTargetsAttached() bool {
	result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
	if err != nil {
		return false
	}

	found := false
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || !isVisibleExtensionTarget(targetType, targetURL) {
			continue
		}
		if _, err := c.ensureTargetAttached(targetID, targetType, targetURL); err == nil {
			found = true
		}
	}
	return found
}

func (c *CDPClient) newestVisibleExtensionURL() string {
	c.mu.Lock()
	targetID := c.lastVisibleExtensionTargetID
	targetURL := c.lastVisibleExtensionURL
	if targetID != "" && targetURL != "" && c.knownPageTargets[targetID] {
		c.mu.Unlock()
		return targetURL
	}
	c.mu.Unlock()

	if !c.ensureVisibleExtensionTargetsAttached() {
		return ""
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastVisibleExtensionTargetID != "" && c.lastVisibleExtensionURL != "" {
		return c.lastVisibleExtensionURL
	}
	return ""
}

func (c *CDPClient) cachedVisibleExtensionURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastVisibleExtensionTargetID != "" && c.lastVisibleExtensionURL != "" && c.knownPageTargets[c.lastVisibleExtensionTargetID] {
		return c.lastVisibleExtensionURL
	}
	return ""
}

func (c *CDPClient) cachedActiveExtensionURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeSessionID == "" {
		return ""
	}
	targetURL := c.sessionURLs[c.activeSessionID]
	if !isExtensionFullPageTarget(c.sessionTypes[c.activeSessionID], targetURL) {
		return ""
	}
	return targetURL
}

func (c *CDPClient) clearCachedVisibleExtension(extensionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if extensionID == "" || extensionIDFromURL(c.lastVisibleExtensionURL) == extensionID {
		c.lastVisibleExtensionTargetID = ""
		c.lastVisibleExtensionURL = ""
	}
}

type extensionRuntimeTarget struct {
	extensionID string
	targetID    string
	targetType  string
	targetURL   string
}

func isExtensionRuntimeTargetType(targetType, targetURL string) bool {
	return targetType == "service_worker" ||
		(targetType == "background_page" && !strings.Contains(targetURL, "offscreen"))
}

func preferExtensionRuntimeTarget(current, candidate extensionRuntimeTarget) extensionRuntimeTarget {
	if candidate.targetID == "" {
		return current
	}
	if current.targetID == "" || candidate.targetType == "service_worker" {
		return candidate
	}
	return current
}

func (c *CDPClient) getExtensionRuntimeTargets() ([]extensionRuntimeTarget, error) {
	result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("Target.getTargets failed: %w", err)
	}

	targetInfos, _ := result["targetInfos"].([]interface{})
	byExtension := make(map[string]extensionRuntimeTarget)
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		extensionID := extensionIDFromURL(targetURL)
		if targetID == "" || extensionID == "" || !isExtensionRuntimeTargetType(targetType, targetURL) {
			continue
		}
		byExtension[extensionID] = preferExtensionRuntimeTarget(byExtension[extensionID], extensionRuntimeTarget{
			extensionID: extensionID,
			targetID:    targetID,
			targetType:  targetType,
			targetURL:   targetURL,
		})
	}

	c.mu.Lock()
	for sessionID, targetURL := range c.sessionURLs {
		extensionID := extensionIDFromURL(targetURL)
		targetID := c.sessionTargets[sessionID]
		targetType := c.sessionTypes[sessionID]
		if extensionID == "" || targetID == "" || !isExtensionRuntimeTargetType(targetType, targetURL) {
			continue
		}
		byExtension[extensionID] = preferExtensionRuntimeTarget(byExtension[extensionID], extensionRuntimeTarget{
			extensionID: extensionID,
			targetID:    targetID,
			targetType:  targetType,
			targetURL:   targetURL,
		})
	}
	c.mu.Unlock()

	targets := make([]extensionRuntimeTarget, 0, len(byExtension))
	for _, target := range byExtension {
		targets = append(targets, target)
	}
	return targets, nil
}

func (c *CDPClient) hasExtensionTarget(extensionID string) bool {
	if extensionID == "" {
		return false
	}

	c.mu.Lock()
	for _, targetURL := range c.sessionURLs {
		if extensionIDFromURL(targetURL) == extensionID {
			c.mu.Unlock()
			return true
		}
	}
	c.mu.Unlock()

	result, err := c.callCommand("Target.getTargets", nil, "", 800*time.Millisecond)
	if err != nil {
		return false
	}

	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetURL, _ := info["url"].(string)
		if extensionIDFromURL(targetURL) == extensionID {
			return true
		}
	}

	return false
}

func (c *CDPClient) ensureExtensionAvailableForAction(extensionID string) error {
	if extensionID == "" {
		return fmt.Errorf("empty extension id")
	}
	if c.hasExtensionTarget(extensionID) {
		return nil
	}
	if c.userDataDir != "" {
		if c.hasInstalledExtensionOnDisk(extensionID) {
			return nil
		}
		return fmt.Errorf("extension %s is not installed in %s", extensionID, c.userDataDir)
	}

	// A dormant MV3 extension may have no runtime target. Probe one impossible
	// storage key so missing extensions are rejected before triggerAction, whose
	// current Chromium implementation crashes when the action ID is unknown.
	_, err := c.callCommand("Extensions.getStorageItems", map[string]interface{}{
		"id":          extensionID,
		"storageArea": "local",
		"keys":        []string{"__chromemanager_extension_presence_probe__"},
	}, "", 2*time.Second)
	if err != nil {
		return fmt.Errorf("extension %s is not installed or enabled: %w", extensionID, err)
	}
	return nil
}

func (c *CDPClient) hasInstalledExtensionOnDisk(extensionID string) bool {
	if extensionID == "" || c.userDataDir == "" {
		return false
	}
	for _, extensionDir := range []string{
		filepath.Join(c.userDataDir, "Default", "Extensions", extensionID),
		filepath.Join(c.userDataDir, "Extensions", extensionID),
	} {
		info, err := os.Stat(extensionDir)
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func (c *CDPClient) hasKnownExtensionTarget(extensionID string) bool {
	if extensionID == "" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, targetURL := range c.sessionURLs {
		if extensionIDFromURL(targetURL) == extensionID {
			return true
		}
	}
	for _, targetURL := range c.targets {
		if extensionIDFromURL(targetURL) == extensionID {
			return true
		}
	}
	return false
}

func (c *CDPClient) getExtensionRuntimeTarget(extensionID string) (extensionRuntimeTarget, error) {
	targets, err := c.getExtensionRuntimeTargets()
	if err != nil {
		return extensionRuntimeTarget{}, err
	}
	for _, target := range targets {
		if target.extensionID == extensionID {
			return target, nil
		}
	}
	return extensionRuntimeTarget{}, fmt.Errorf("extension runtime target not found for %s", extensionID)
}

func (c *CDPClient) findPageSessionForActivation() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeSessionID != "" && !isExtensionURL(c.sessionURLs[c.activeSessionID]) {
		return c.activeSessionID
	}
	if sessionID := c.lastActivePageSessionID; sessionID != "" && isActivePageTarget(c.sessionTypes[sessionID], c.sessionURLs[sessionID]) {
		return sessionID
	}
	for _, sessionID := range c.sessionOrder {
		if isActivePageTarget(c.sessionTypes[sessionID], c.sessionURLs[sessionID]) {
			return sessionID
		}
	}
	return ""
}

func (c *CDPClient) activatePageForPopupDismissal() bool {
	sessionID := c.findPageSessionForActivation()
	if sessionID == "" {
		return false
	}
	c.mu.Lock()
	targetType := c.sessionTypes[sessionID]
	targetURL := c.sessionURLs[sessionID]
	targetID := c.sessionTargets[sessionID]
	c.mu.Unlock()
	if sessionID == "" || targetID == "" || !isActivePageTarget(targetType, targetURL) {
		return false
	}

	return c.reactivateTabByTargetIDInBackground(targetID) == nil
}

func (c *CDPClient) findTabTargetForActivation() string {
	activeURL := ""
	if sessionID := c.findPageSessionForActivation(); sessionID != "" {
		c.mu.Lock()
		activeURL = c.sessionURLs[sessionID]
		c.mu.Unlock()
	}

	result, err := c.callCommand("Target.getTargets", map[string]interface{}{
		"filter": []map[string]interface{}{
			{"type": "browser", "exclude": true},
			{},
		},
	}, "", 2*time.Second)
	if err != nil {
		return ""
	}
	return selectTabTargetForActivation(result["targetInfos"], activeURL)
}

func selectTabTargetForActivation(rawTargetInfos interface{}, activeURL string) string {
	targetInfos, _ := rawTargetInfos.([]interface{})
	fallback := ""
	urlMatch := ""
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetType != "tab" || targetID == "" || targetURL == "" || isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
			continue
		}
		embedderData, _ := info["embedderData"].(map[string]interface{})
		tabActive, _ := embedderData["tabActive"].(bool)
		if tabActive && (activeURL == "" || targetURL == activeURL) {
			return targetID
		}
		if urlMatch == "" && activeURL != "" && targetURL == activeURL {
			urlMatch = targetID
		}
		if fallback == "" {
			fallback = targetID
		}
	}
	if urlMatch != "" {
		return urlMatch
	}
	return fallback
}

func (c *CDPClient) triggerExtensionAction(extensionID string) error {
	targetID := c.findTabTargetForActivation()
	if targetID == "" {
		return fmt.Errorf("no active tab target")
	}
	_, err := c.callCommand("Extensions.triggerAction", map[string]interface{}{
		"id":       extensionID,
		"targetId": targetID,
	}, "", 2*time.Second)
	return err
}

func (c *CDPClient) extensionPopupTargets(extensionID string) []extensionRuntimeTarget {
	result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
	if err != nil {
		return nil
	}

	targetInfos, _ := result["targetInfos"].([]interface{})
	targets := make([]extensionRuntimeTarget, 0, 2)
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || targetURL == "" {
			continue
		}
		foundExtensionID := extensionIDFromURL(targetURL)
		if foundExtensionID != extensionID || !isVisibleExtensionTarget(targetType, targetURL) {
			continue
		}
		targets = append(targets, extensionRuntimeTarget{
			extensionID: extensionID,
			targetID:    targetID,
			targetType:  targetType,
			targetURL:   targetURL,
		})
	}
	return targets
}

func (c *CDPClient) existingTargetIDs(targetIDs map[string]bool) (map[string]bool, bool) {
	result, err := c.callCommand("Target.getTargets", nil, "", 800*time.Millisecond)
	if err != nil {
		return nil, false
	}

	existing := make(map[string]bool, len(targetIDs))
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, rawInfo := range targetInfos {
		info, _ := rawInfo.(map[string]interface{})
		if info == nil {
			continue
		}
		targetID, _ := info["targetId"].(string)
		if targetIDs[targetID] {
			existing[targetID] = true
		}
	}
	return existing, true
}

func (c *CDPClient) waitForTargetsDestroyed(targetIDs map[string]bool, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		remaining, ok := c.existingTargetIDs(targetIDs)
		if ok && len(remaining) == 0 {
			return true
		}
		select {
		case <-c.targetEvents:
			continue
		case <-timer.C:
			return false
		}
	}
}

func (c *CDPClient) closeVisibleExtensionTargets(extensionID string) int {
	if extensionID == "" {
		return 0
	}

	targets := c.extensionPopupTargets(extensionID)
	if len(targets) == 0 {
		c.mu.Lock()
		if extensionIDFromURL(c.lastVisibleExtensionURL) == extensionID {
			c.lastVisibleExtensionTargetID = ""
			c.lastVisibleExtensionURL = ""
		}
		c.mu.Unlock()
		return 0
	}
	trackedTargetIDs := make(map[string]bool, len(targets))
	for _, target := range targets {
		trackedTargetIDs[target.targetID] = true
	}

	// Browser action popups are owned by Chrome's native UI. Activating the
	// underlying page dismisses that UI; closing only its renderer target can
	// make Chrome recreate the popup with a new target ID.
	c.activatePageForPopupDismissal()
	if c.waitForTargetsDestroyed(trackedTargetIDs, 350*time.Millisecond) {
		c.mu.Lock()
		if extensionIDFromURL(c.lastVisibleExtensionURL) == extensionID {
			c.lastVisibleExtensionTargetID = ""
			c.lastVisibleExtensionURL = ""
		}
		c.mu.Unlock()
		return len(targets)
	}

	// A native popup may survive with the same target ID but an empty URL.
	// Keep closing by the original IDs instead of losing it from the
	// chrome-extension:// URL filter.
	for _, target := range c.extensionPopupTargets(extensionID) {
		trackedTargetIDs[target.targetID] = true
	}
	closed := 0
	for targetID := range trackedTargetIDs {
		if err := c.CloseTargetById(targetID); err != nil {
			cdpDebugf("[ExtensionPopup] close failed extension=%s targetID=%s: %v\n", extensionID, targetID, err)
			continue
		}
		closed++
	}

	if closed > 0 {
		c.mu.Lock()
		if extensionIDFromURL(c.lastVisibleExtensionURL) == extensionID {
			c.lastVisibleExtensionTargetID = ""
			c.lastVisibleExtensionURL = ""
		}
		c.mu.Unlock()
	}
	return closed
}

func (c *CDPClient) visibleExtensionTarget(extensionID string) (extensionRuntimeTarget, bool) {
	targets := c.extensionPopupTargets(extensionID)
	if len(targets) == 0 {
		return extensionRuntimeTarget{}, false
	}
	c.mu.Lock()
	lastTargetID := c.lastVisibleExtensionTargetID
	c.mu.Unlock()
	for _, target := range targets {
		if target.targetID == lastTargetID {
			return target, true
		}
	}
	return targets[0], true
}

func (c *CDPClient) waitForVisibleExtensionTarget(extensionID string, timeout time.Duration) (extensionRuntimeTarget, bool) {
	if extensionID == "" {
		return extensionRuntimeTarget{}, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if target, ok := c.visibleExtensionTarget(extensionID); ok {
			_, _ = c.ensureTargetAttached(target.targetID, target.targetType, target.targetURL)
			return target, true
		}
		select {
		case <-c.targetEvents:
			continue
		case <-timer.C:
			return extensionRuntimeTarget{}, false
		}
	}
}

func (c *CDPClient) openDeclaredExtensionPopup(extensionID string) (string, error) {
	tabTargetID := c.findTabTargetForActivation()
	if tabTargetID == "" {
		return "", fmt.Errorf("no active tab target for extension popup")
	}
	if _, err := c.callCommand("Target.activateTarget", map[string]interface{}{
		"targetId": tabTargetID,
	}, "", 2*time.Second); err != nil {
		return "", fmt.Errorf("activate extension tab target: %w", err)
	}

	runtimeTarget, err := c.getExtensionRuntimeTarget(extensionID)
	if err != nil {
		return "", err
	}
	sessionID, err := c.ensureTargetAttached(runtimeTarget.targetID, runtimeTarget.targetType, runtimeTarget.targetURL)
	if err != nil {
		return "", err
	}
	_, _ = c.callCommand("Runtime.enable", nil, sessionID, 2*time.Second)

	activeURL := ""
	if pageSessionID := c.findPageSessionForActivation(); pageSessionID != "" {
		c.mu.Lock()
		activeURL = c.sessionURLs[pageSessionID]
		c.mu.Unlock()
	}
	activeURLJSON, _ := json.Marshal(activeURL)
	evalResp, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    fmt.Sprintf(declaredExtensionPopupScript, string(activeURLJSON)),
		"awaitPromise":  true,
		"returnByValue": true,
		"userGesture":   true,
	}, sessionID, 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("open declared extension popup: %w", err)
	}
	if exception, ok := evalResp["exceptionDetails"]; ok {
		return "", fmt.Errorf("open declared extension popup exception: %v", exception)
	}

	visibleTarget, ok := c.waitForVisibleExtensionTarget(extensionID, 1500*time.Millisecond)
	if !ok {
		return "", fmt.Errorf("declared extension popup did not become visible")
	}
	return visibleTarget.targetID, nil
}

func (c *CDPClient) openExtensionActionPopup(extensionID string) (string, error) {
	if extensionID == "" {
		return "", fmt.Errorf("empty extension id")
	}
	if err := c.ensureExtensionAvailableForAction(extensionID); err != nil {
		return "", err
	}

	if target, ok := c.visibleExtensionTarget(extensionID); ok {
		c.ensureExtensionTargetsAttached(extensionID)
		return target.targetID, nil
	}

	if err := c.triggerExtensionAction(extensionID); err != nil {
		return "", fmt.Errorf("Extensions.triggerAction failed: %w", err)
	}
	if visibleTarget, ok := c.waitForVisibleExtensionTarget(extensionID, 750*time.Millisecond); ok {
		return visibleTarget.targetID, nil
	}
	return c.openDeclaredExtensionPopup(extensionID)
}

func cdpModifiersFromEventFlags(modifiers int) int {
	if modifiers >= 0 && modifiers <= 15 {
		return modifiers
	}

	cdpModifiers := 0
	if (modifiers & 0x00020000) != 0 { // Shift
		cdpModifiers |= 8
	}
	if (modifiers & 0x00040000) != 0 { // Control
		cdpModifiers |= 2
	}
	if (modifiers & 0x00080000) != 0 { // Alt/Option
		cdpModifiers |= 1
	}
	if (modifiers & 0x00100000) != 0 { // Command
		cdpModifiers |= 4
	}
	return cdpModifiers
}

// DispatchMouseEvent sends mouse event to ACTIVE session
func (c *CDPClient) DispatchMouseEvent(eventType string, x, y float64, button string, clickCount int, deltaX, deltaY float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}

	// Calculate buttons bitmask (Active buttons)
	// 1=Left, 2=Right, 4=Middle
	buttonsBitmask := 0
	if eventType == "mousePressed" || eventType == "mouseMoved" {
		switch button {
		case "left":
			buttonsBitmask |= 1
		case "right":
			buttonsBitmask |= 2
		}
	}
	// For mouseReleased, buttons should be 0 (if only that button was pressed), so default 0 works.
	// However, if we support multi-button, we'd need state. For now, simple mapping is better than 0.

	params := map[string]interface{}{
		"type":       eventType,
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
		"buttons":    buttonsBitmask,
	}

	if eventType == "mouseWheel" {
		params["deltaX"] = deltaX
		params["deltaY"] = deltaY
	}

	if c.activeSessionID == "" {
		return nil
	}

	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Input.dispatchMouseEvent",
		"params":    params,
		"sessionId": c.activeSessionID, // Routing to active session
	}

	return c.sendJSON(c.ws, cmd)
}

func (c *CDPClient) DispatchMouseEventToSession(sessionID string, eventType string, x, y float64, button string, clickCount int, deltaX, deltaY float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}
	if sessionID == "" {
		return nil
	}

	buttonsBitmask := 0
	if eventType == "mousePressed" || eventType == "mouseMoved" {
		switch button {
		case "left":
			buttonsBitmask |= 1
		case "right":
			buttonsBitmask |= 2
		}
	}

	params := map[string]interface{}{
		"type":       eventType,
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
		"buttons":    buttonsBitmask,
	}
	if eventType == "mouseWheel" {
		params["deltaX"] = deltaX
		params["deltaY"] = deltaY
	}

	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Input.dispatchMouseEvent",
		"params":    params,
		"sessionId": sessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

func (c *CDPClient) DispatchDOMClickToSession(sessionID string, action DOMClickAction) error {
	if sessionID == "" || action.Selector == "" {
		return fmt.Errorf("DOM click route is incomplete")
	}
	actionJSON, _ := json.Marshal(action)
	expression := fmt.Sprintf(`(() => {
	const action = %s;
	const textOf = (element) => String((element && (element.innerText || element.value || element.textContent)) || "")
		.replace(/\s+/g, " ").trim().slice(0, 160);
	const queryShadow = (value, all) => {
		const parts = value.split(" >>> ");
		let root = document;
		for (let index = 0; index < parts.length; index++) {
			if (all && index === parts.length - 1) return Array.from(root.querySelectorAll(parts[index]));
			const element = root.querySelector(parts[index]);
			if (!element) return null;
			if (index === parts.length - 1) return element;
			if (!element.shadowRoot) return null;
			root = element.shadowRoot;
		}
		return null;
	};
	const semanticElement = () => {
		if (!action.semanticSelector) return null;
		let candidates;
		try { candidates = queryShadow(action.semanticSelector, true) || []; } catch (_) { return null; }
		if (action.semanticText) candidates = candidates.filter((candidate) => textOf(candidate) === action.semanticText);
		const index = Number.isInteger(action.semanticIndex) && action.semanticIndex >= 0 ? action.semanticIndex : 0;
		return candidates[index] || null;
	};
	let element = semanticElement();
	if (!element) element = queryShadow(action.selector, false);
	if (!element) return {ok: false, reason: "target-not-found"};
	const hittable = (candidate) => {
		if (!candidate || !candidate.isConnected) return false;
		const rect = candidate.getBoundingClientRect();
		if (rect.width <= 0 || rect.height <= 0) return false;
		const x = rect.left + rect.width * 0.5;
		const y = rect.top + rect.height * 0.5;
		if (x < 0 || y < 0 || x >= window.innerWidth || y >= window.innerHeight) return false;
		const top = document.elementFromPoint(x, y);
		return top === candidate || candidate.contains(top);
	};
	if (!element.isConnected) element = semanticElement();
	if (!element) return {ok: false, reason: "target-detached"};
	const rect = element.getBoundingClientRect();
	if (rect.width <= 0 || rect.height <= 0) return {ok: false, reason: "target-hidden"};
	if (!hittable(element)) {
		try { element.focus({preventScroll: true}); } catch (_) {}
		element.click();
		return {ok: true, direct: true};
	}
	return {
		ok: true,
		x: rect.left + rect.width * %g,
		y: rect.top + rect.height * %g
	};
})()`, string(actionJSON), action.ElementX, action.ElementY)
	result, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
		"userGesture":   true,
	}, sessionID, 800*time.Millisecond)
	if err != nil {
		return err
	}
	evalResult, _ := result["result"].(map[string]interface{})
	value, _ := evalResult["value"].(map[string]interface{})
	if value == nil {
		return fmt.Errorf("DOM click selector not found: %s", action.Selector)
	}
	if ok, _ := value["ok"].(bool); !ok {
		reason, _ := value["reason"].(string)
		return fmt.Errorf("DOM click target unavailable: %s", reason)
	}
	if direct, _ := value["direct"].(bool); direct {
		return nil
	}
	x := numberFromAny(value["x"])
	y := numberFromAny(value["y"])
	if err := c.dispatchMouseEventToSessionAndWait(sessionID, "mouseMoved", x, y, "none", 0); err != nil {
		return err
	}
	if err := c.dispatchMouseEventToSessionAndWait(sessionID, "mousePressed", x, y, "left", 1); err != nil {
		return err
	}
	return c.dispatchMouseEventToSessionAndWait(sessionID, "mouseReleased", x, y, "left", 1)
}

func (c *CDPClient) dispatchMouseEventToSessionAndWait(sessionID, eventType string, x, y float64, button string, clickCount int) error {
	buttons := 0
	if eventType == "mousePressed" {
		switch button {
		case "left":
			buttons = 1
		case "right":
			buttons = 2
		}
	}
	_, err := c.callCommand("Input.dispatchMouseEvent", map[string]interface{}{
		"type":       eventType,
		"x":          x,
		"y":          y,
		"button":     button,
		"clickCount": clickCount,
		"buttons":    buttons,
	}, sessionID, 800*time.Millisecond)
	return err
}

// DispatchKeyEvent sends key event to ACTIVE session
func (c *CDPClient) DispatchKeyEvent(eventType string, modifiers int, key, code string, windowsVirtualKeyCode, nativeVirtualKeyCode int, text, unmodifiedText string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}

	cdpModifiers := modifiers

	params := map[string]interface{}{
		"type":                  eventType,
		"modifiers":             cdpModifiers,
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": windowsVirtualKeyCode,
		"nativeVirtualKeyCode":  nativeVirtualKeyCode,
	}
	if text != "" {
		params["text"] = text
	}
	if unmodifiedText != "" {
		params["unmodifiedText"] = unmodifiedText
	}

	// Map Mac shortcuts to CDP commands
	if (cdpModifiers & 4) != 0 { // Command key is pressed
		var commands []string
		lowerKey := strings.ToLower(key)
		switch lowerKey {
		case "c":
			commands = append(commands, "copy")
		case "v":
			commands = append(commands, "paste")
		case "x":
			commands = append(commands, "cut")
		case "a":
			commands = append(commands, "selectAll")
		case "z":
			if (cdpModifiers & 8) != 0 { // Shift+Cmd+Z
				commands = append(commands, "redo")
			} else {
				commands = append(commands, "undo")
			}
		case "t":
			// Handled by our backend to open tabs, but just in case
		}

		if len(commands) > 0 {
			params["commands"] = commands
			if eventType == "keyDown" {
				// Chrome expects 'rawKeyDown' for command shortcuts to prevent typing char
				params["type"] = "rawKeyDown"
				delete(params, "text")
				delete(params, "unmodifiedText")
			}
		}
	}

	if c.activeSessionID == "" {
		return nil
	}

	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Input.dispatchKeyEvent",
		"params":    params,
		"sessionId": c.activeSessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

func (c *CDPClient) DispatchKeyEventToSession(sessionID string, eventType string, modifiers int, key, code string, windowsVirtualKeyCode, nativeVirtualKeyCode int, text, unmodifiedText string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}
	if sessionID == "" {
		return nil
	}

	cdpModifiers := modifiers
	params := map[string]interface{}{
		"type":                  eventType,
		"modifiers":             cdpModifiers,
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": windowsVirtualKeyCode,
		"nativeVirtualKeyCode":  nativeVirtualKeyCode,
	}
	if text != "" {
		params["text"] = text
	}
	if unmodifiedText != "" {
		params["unmodifiedText"] = unmodifiedText
	}

	if (cdpModifiers & 4) != 0 {
		var commands []string
		lowerKey := strings.ToLower(key)
		switch lowerKey {
		case "c":
			commands = append(commands, "copy")
		case "v":
			commands = append(commands, "paste")
		case "x":
			commands = append(commands, "cut")
		case "a":
			commands = append(commands, "selectAll")
		case "z":
			if (cdpModifiers & 8) != 0 {
				commands = append(commands, "redo")
			} else {
				commands = append(commands, "undo")
			}
		}

		if len(commands) > 0 {
			params["commands"] = commands
			if eventType == "keyDown" {
				params["type"] = "rawKeyDown"
				delete(params, "text")
				delete(params, "unmodifiedText")
			}
		}
	}

	c.msgID++
	id := c.msgID
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Input.dispatchKeyEvent",
		"params":    params,
		"sessionId": sessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

// InsertText directly inserts text at cursor position (simpler than DispatchKeyEvent for typing)
func (c *CDPClient) InsertText(text string, overwrite bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}

	if c.activeSessionID == "" {
		return nil
	}

	c.msgID++
	id := c.msgID

	// We use Runtime.evaluate to inject text into the active element or document selection
	// This approach is significantly more robust for windows in the background
	script := fmt.Sprintf(`
		(function() {
			var text = %q;
			var overwrite = %t;
			var el = document.activeElement;
			if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) {
				if (overwrite) {
					el.value = text;
					el.selectionStart = el.selectionEnd = text.length;
				} else {
					var start = el.selectionStart;
					var end = el.selectionEnd;
					el.value = el.value.substring(0, start) + text + el.value.substring(end);
					el.selectionStart = el.selectionEnd = start + text.length;
				}
				el.dispatchEvent(new Event('input', { bubbles: true }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
			} else {
				if (overwrite) {
					document.execCommand('selectAll', false, null);
				}
				document.execCommand('insertText', false, text);
			}
			return true;
		})();
	`, text, overwrite)

	cmd := map[string]interface{}{
		"id":     id,
		"method": "Runtime.evaluate",
		"params": map[string]interface{}{
			"expression":    script,
			"returnByValue": true,
		},
		"sessionId": c.activeSessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

func (c *CDPClient) InsertTextToSession(sessionID string, text string, overwrite bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ws == nil {
		return fmt.Errorf("not connected")
	}
	if sessionID == "" {
		return nil
	}

	c.msgID++
	id := c.msgID
	script := fmt.Sprintf(`
		(function() {
			var text = %q;
			var overwrite = %t;
			var el = document.activeElement;
			if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA')) {
				if (overwrite) {
					el.value = text;
					el.selectionStart = el.selectionEnd = text.length;
				} else {
					var start = el.selectionStart;
					var end = el.selectionEnd;
					el.value = el.value.substring(0, start) + text + el.value.substring(end);
					el.selectionStart = el.selectionEnd = start + text.length;
				}
				el.dispatchEvent(new Event('input', { bubbles: true }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
			} else {
				if (overwrite) {
					document.execCommand('selectAll', false, null);
				}
				document.execCommand('insertText', false, text);
			}
			return true;
		})();
	`, text, overwrite)

	cmd := map[string]interface{}{
		"id":     id,
		"method": "Runtime.evaluate",
		"params": map[string]interface{}{
			"expression":    script,
			"returnByValue": true,
		},
		"sessionId": sessionID,
	}

	return c.sendJSON(c.ws, cmd)
}

// FocusInputElement finds an input element and focuses it using DOM.focus
// This is more reliable than mouse click for background windows
func (c *CDPClient) FocusInputElement() error {
	// Use JavaScript to find input element and focus it
	// document.activeElement.focus() or find first input
	focusScript := `(function() {
		// First check if there's already a focusable element selected
		var el = document.activeElement;
		if (el && el !== document.body && el !== document.documentElement) {
			if (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || 
			    el.contentEditable === 'true' || el.getAttribute('role') === 'textbox') {
				el.focus();
				return true;
			}
		}
		// Otherwise find first input and focus it
		var input = document.querySelector('input:not([type=hidden]):not([disabled]), textarea:not([disabled]), [contenteditable=true], [role=textbox]');
		if (input) {
			input.focus();
			return true;
		}
		return false;
	})()`

	result, err := c.Evaluate(focusScript)
	if err != nil {
		return fmt.Errorf("focus script failed: %v", err)
	}

	if result == false {
		return fmt.Errorf("no input element found to focus")
	}

	return nil
}

// GetAllSessions returns all tracked session IDs
func (c *CDPClient) GetAllSessions() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	sessions := make([]string, 0, len(c.sessions))
	// Put active session first
	if c.activeSessionID != "" {
		sessions = append(sessions, c.activeSessionID)
	}
	for sessID := range c.sessions {
		if sessID != c.activeSessionID {
			sessions = append(sessions, sessID)
		}
	}
	return sessions
}

// EvaluateOnSession executes JS on a specific session
func (c *CDPClient) EvaluateOnSession(sessionID string, expression string) (interface{}, error) {
	c.mu.Lock()

	if c.ws == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}

	c.msgID++
	id := c.msgID
	ch := make(chan map[string]interface{}, 1)
	c.pendingRequests[id] = ch

	ws := c.ws
	c.mu.Unlock()

	params := map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
	}
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Runtime.evaluate",
		"params":    params,
		"sessionId": sessionID,
	}

	if err := c.sendJSON(ws, cmd); err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, err
	}

	// Wait for response
	select {
	case response := <-ch:
		if resultParams, ok := response["result"].(map[string]interface{}); ok {
			if res, ok := resultParams["result"].(map[string]interface{}); ok {
				return res["value"], nil
			}
		}
		return nil, fmt.Errorf("invalid response structure")
	case <-time.After(2 * time.Second):
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("timeout")
	}
}

func (c *CDPClient) GetDevicePixelRatio() (float64, error) {
	result, err := c.Evaluate("window.devicePixelRatio || 1")
	if err != nil {
		return 0, err
	}

	dpr := numberFromAny(result)
	if dpr <= 0 {
		return 0, fmt.Errorf("invalid devicePixelRatio: %v", result)
	}
	return dpr, nil
}

func (c *CDPClient) ensureChromeManagerZoomExtensionSession() (string, error) {
	c.extensionSessionMu.Lock()
	defer c.extensionSessionMu.Unlock()

	c.mu.Lock()
	if c.extensionSessionID != "" {
		sessionID := c.extensionSessionID
		c.mu.Unlock()
		return sessionID, nil
	}
	c.mu.Unlock()

	attachExtensionTarget := func(targetID, url string) (string, error) {
		attachResult, err := c.callCommand("Target.attachToTarget", map[string]interface{}{
			"targetId": targetID,
			"flatten":  true,
		}, "", 2*time.Second)
		if err != nil {
			return "", err
		}
		sessionID, _ := attachResult["sessionId"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("zoom extension attach returned empty session")
		}

		c.mu.Lock()
		c.extensionSessionID = sessionID
		c.extensionTargetID = targetID
		c.extensionURL = url
		c.mu.Unlock()

		_, _ = c.callCommand("Runtime.enable", nil, sessionID, 2*time.Second)
		return sessionID, nil
	}

	findAndAttachExtensionTarget := func() (string, bool, error) {
		result, err := c.callCommand("Target.getTargets", nil, "", 2*time.Second)
		if err != nil {
			return "", false, err
		}

		targetInfos, _ := result["targetInfos"].([]interface{})
		for _, rawInfo := range targetInfos {
			info, ok := rawInfo.(map[string]interface{})
			if !ok {
				continue
			}
			url, _ := info["url"].(string)
			targetType, _ := info["type"].(string)
			targetID, _ := info["targetId"].(string)
			isZoomTarget := (targetType == "service_worker" && isChromeManagerZoomExtensionURL(url)) || (targetType == "page" && isChromeManagerZoomExtensionControlURL(url))
			if targetID == "" || !isZoomTarget {
				continue
			}

			sessionID, err := attachExtensionTarget(targetID, url)
			return sessionID, true, err
		}
		return "", false, nil
	}

	if sessionID, found, err := findAndAttachExtensionTarget(); found || err != nil {
		return sessionID, err
	}

	c.mu.Lock()
	discoveredScopeURL := extensionScopeFromWorkerURL(c.extensionURL)
	c.mu.Unlock()
	scopeCandidates := []string{chromeManagerZoomExtensionScopeURL()}
	if discoveredScopeURL != "" && discoveredScopeURL != scopeCandidates[0] {
		scopeCandidates = append(scopeCandidates, discoveredScopeURL)
	}

	for _, scopeURL := range scopeCandidates {
		_, _ = c.callCommand("ServiceWorker.enable", nil, "", 2*time.Second)
		if _, err := c.callCommand("ServiceWorker.startWorker", map[string]interface{}{"scopeURL": scopeURL}, "", 2*time.Second); err != nil {
			continue
		}
		time.Sleep(150 * time.Millisecond)

		if sessionID, found, err := findAndAttachExtensionTarget(); found || err != nil {
			return sessionID, err
		}
	}

	return "", fmt.Errorf("ChromeManager zoom extension service worker is unavailable on port %d", c.debugPort)
}

func (c *CDPClient) evaluateChromeManagerZoomExtension(expression string) (interface{}, error) {
	sessionID, err := c.ensureChromeManagerZoomExtensionSession()
	if err != nil {
		return nil, err
	}

	evaluate := func(sID string) (map[string]interface{}, error) {
		return c.callCommand("Runtime.evaluate", map[string]interface{}{
			"expression":    expression,
			"awaitPromise":  true,
			"returnByValue": true,
			"userGesture":   true,
		}, sID, 3*time.Second)
	}

	result, err := evaluate(sessionID)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "session") {
		c.mu.Lock()
		c.extensionSessionID = ""
		c.extensionTargetID = ""
		c.mu.Unlock()
		if retrySessionID, retryErr := c.ensureChromeManagerZoomExtensionSession(); retryErr == nil {
			result, err = evaluate(retrySessionID)
		}
	}
	if err != nil {
		return nil, err
	}

	if exception, ok := result["exceptionDetails"]; ok {
		return nil, fmt.Errorf("extension evaluate exception: %v", exception)
	}
	if remoteObject, ok := result["result"].(map[string]interface{}); ok {
		return remoteObject["value"], nil
	}
	return nil, fmt.Errorf("invalid extension evaluate response: %v", result)
}

func nativeZoomResultFromValue(value interface{}) NativeZoomResult {
	payload, _ := value.(map[string]interface{})
	return NativeZoomResult{
		Zoom:     numberFromAny(payload["zoom"]),
		TabID:    int(numberFromAny(payload["tabId"])),
		WindowID: int(numberFromAny(payload["windowId"])),
		URL: func() string {
			if url, ok := payload["url"].(string); ok {
				return url
			}
			return ""
		}(),
		Mode: "chrome-tabs-setZoom",
		Error: func() string {
			if msg, ok := payload["error"].(string); ok {
				return msg
			}
			return ""
		}(),
	}
}

var chromeNativeZoomSteps = []float64{0.25, 0.33, 0.50, 0.67, 0.75, 0.80, 0.90, 1.00, 1.10, 1.25, 1.50, 1.75, 2.00, 2.50, 3.00, 4.00, 5.00}

func zoomDelta(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func nearestChromeNativeZoomIndex(zoom float64) int {
	bestIndex := 0
	bestDelta := zoomDelta(chromeNativeZoomSteps[0], zoom)
	for i, step := range chromeNativeZoomSteps[1:] {
		delta := zoomDelta(step, zoom)
		if delta < bestDelta {
			bestIndex = i + 1
			bestDelta = delta
		}
	}
	return bestIndex
}

func chromeManagerActiveTabJS(action string) string {
	return fmt.Sprintf(`(async () => {
		if (!chrome.tabs || !chrome.tabs.query) {
			throw new Error("chrome.tabs API is unavailable in the current extension context");
		}
		const extensionURL = chrome.runtime && chrome.runtime.getURL ? chrome.runtime.getURL("") : "chrome-extension://";
		let tabs = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
		if (!tabs || tabs.length === 0) {
			tabs = await chrome.tabs.query({ active: true });
		}
		const activeTab = tabs.find((tab) => tab && tab.id >= 0 && !(tab.url || "").startsWith(extensionURL)) || tabs[0];
		if (!activeTab || typeof activeTab.id !== "number") {
			throw new Error("ChromeManager zoom controller could not find the active tab");
		}
		%s
	})()`, action)
}

func (c *CDPClient) GetChromeNativeZoom() (NativeZoomResult, error) {
	value, err := c.evaluateChromeManagerZoomExtension(chromeManagerActiveTabJS(`
		const zoom = await chrome.tabs.getZoom(activeTab.id);
		return { tabId: activeTab.id, windowId: activeTab.windowId, url: activeTab.url || "", zoom };
	`))
	if err != nil {
		return NativeZoomResult{}, err
	}
	result := nativeZoomResultFromValue(value)
	if result.Zoom <= 0 {
		return NativeZoomResult{}, fmt.Errorf("invalid native zoom result: %v", value)
	}
	return result, nil
}

func (c *CDPClient) zoomFactorForAPILevel(level int) (float64, error) {
	switch level {
	case -1, 1:
		current, err := c.GetChromeNativeZoom()
		if err != nil {
			return 0, err
		}
		index := nearestChromeNativeZoomIndex(current.Zoom)
		if level < 0 && index > 0 {
			index--
		}
		if level > 0 && index < len(chromeNativeZoomSteps)-1 {
			index++
		}
		return chromeNativeZoomSteps[index], nil
	case 0:
		return 1.0, nil
	default:
		zoom := float64(level) / 100.0
		if zoom < 0.25 || zoom > 5.0 {
			return 0, fmt.Errorf("unsupported zoom level: %d", level)
		}
		return zoom, nil
	}
}

func (c *CDPClient) SetChromeNativeZoomLevel(level int) (NativeZoomResult, error) {
	zoom, err := c.zoomFactorForAPILevel(level)
	if err != nil {
		return NativeZoomResult{}, err
	}
	return c.SetChromeNativeZoom(zoom)
}

func (c *CDPClient) SetChromeNativeZoom(zoom float64) (NativeZoomResult, error) {
	if zoom < 0.25 {
		zoom = 0.25
	} else if zoom > 5.0 {
		zoom = 5.0
	}

	value, err := c.evaluateChromeManagerZoomExtension(chromeManagerActiveTabJS(fmt.Sprintf(`
		let beforeZoom = 0;
		try {
			beforeZoom = await chrome.tabs.getZoom(activeTab.id);
		} catch (_) {}
		try {
			await chrome.tabs.setZoomSettings(activeTab.id, { mode: "automatic", scope: "per-tab" });
			await chrome.tabs.setZoom(activeTab.id, %g);
			const zoom = await chrome.tabs.getZoom(activeTab.id);
			return { tabId: activeTab.id, windowId: activeTab.windowId, url: activeTab.url || "", zoom };
		} catch (err) {
			return {
				tabId: activeTab.id,
				windowId: activeTab.windowId,
				url: activeTab.url || "",
				zoom: beforeZoom || 0,
				error: err && (err.message || String(err)) || "chrome.tabs.setZoom failed"
			};
		}
	`, zoom)))
	if err != nil {
		result, fallbackErr := c.setCDPPageZoom(zoom, "")
		if fallbackErr == nil {
			return result, nil
		}
		return NativeZoomResult{Zoom: zoom, Mode: "failed"}, fmt.Errorf("chrome.tabs.setZoom failed: %v; CDP fallback failed: %w", err, fallbackErr)
	}
	result := nativeZoomResultFromValue(value)
	if result.Error != "" || result.Zoom <= 0 || result.Zoom < zoom-0.001 || result.Zoom > zoom+0.001 {
		fallbackResult, fallbackErr := c.setCDPPageZoom(zoom, result.URL)
		if fallbackErr == nil {
			return fallbackResult, nil
		}
		if result.Error != "" {
			return NativeZoomResult{Zoom: zoom, Mode: "failed"}, fmt.Errorf("chrome.tabs.setZoom rejected for url=%s: %s; CDP fallback failed: %w", result.URL, result.Error, fallbackErr)
		}
		return NativeZoomResult{Zoom: zoom, Mode: "failed"}, fmt.Errorf("chrome.tabs.setZoom returned unexpected zoom %.2f for url=%s target=%.2f; CDP fallback failed: %w", result.Zoom, result.URL, zoom, fallbackErr)
	}
	return result, nil
}

func (c *CDPClient) findPageSessionForURL(targetURL string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if targetURL != "" {
		for sessionID, currentURL := range c.sessionURLs {
			if c.sessionTypes[sessionID] == "page" && sameComparableURL(currentURL, targetURL) {
				return sessionID
			}
		}
	}

	if c.activeSessionID != "" && c.sessionTypes[c.activeSessionID] == "page" && !isExtensionURL(c.sessionURLs[c.activeSessionID]) {
		return c.activeSessionID
	}
	for _, sessionID := range c.sessionOrder {
		if c.sessionTypes[sessionID] == "page" && !isExtensionURL(c.sessionURLs[sessionID]) {
			return sessionID
		}
	}
	return ""
}

func (c *CDPClient) setCDPPageZoom(zoom float64, targetURL string) (NativeZoomResult, error) {
	sessionID := c.findPageSessionForURL(targetURL)
	if sessionID == "" {
		return NativeZoomResult{}, fmt.Errorf("no active page session")
	}

	c.mu.Lock()
	sessionURL := c.sessionURLs[sessionID]
	sessionType := c.sessionTypes[sessionID]
	c.mu.Unlock()

	mode := "cdp-page-zoom"
	var pageErr error
	if _, err := c.callCommand("Page.setZoomFactor", map[string]interface{}{
		"zoomFactor": zoom,
	}, sessionID, 2*time.Second); err != nil {
		pageErr = err
		if zoom < 1.0 {
			_, _ = c.callCommand("Emulation.setPageScaleFactor", map[string]interface{}{
				"pageScaleFactor": 1.0,
			}, sessionID, 2*time.Second)
			if cssErr := c.setCSSPageZoom(sessionID, zoom); cssErr != nil {
				return NativeZoomResult{}, fmt.Errorf("Page.setZoomFactor failed session=%s type=%s url=%s zoom=%.2f: %v; CSS zoom fallback failed: %w", sessionID, sessionType, sessionURL, zoom, pageErr, cssErr)
			}
			mode = "cdp-css-zoom"
		} else {
			_ = c.setCSSPageZoom(sessionID, 1.0)
			mode = "cdp-emulation-scale"
			if _, emulationErr := c.callCommand("Emulation.setPageScaleFactor", map[string]interface{}{
				"pageScaleFactor": zoom,
			}, sessionID, 2*time.Second); emulationErr != nil {
				if cssErr := c.setCSSPageZoom(sessionID, zoom); cssErr != nil {
					return NativeZoomResult{}, fmt.Errorf("Page.setZoomFactor failed session=%s type=%s url=%s zoom=%.2f: %v; Emulation.setPageScaleFactor failed: %v; CSS zoom fallback failed: %w", sessionID, sessionType, sessionURL, zoom, pageErr, emulationErr, cssErr)
				}
				mode = "cdp-css-zoom"
			}
		}
	} else {
		_ = c.setCSSPageZoom(sessionID, 1.0)
	}

	return NativeZoomResult{
		Zoom: zoom,
		URL:  sessionURL,
		Mode: mode,
	}, nil
}

func (c *CDPClient) resetCDPPageZoomFallback(targetURL string) error {
	_, err := c.setCDPPageZoom(1.0, targetURL)
	return err
}

func (c *CDPClient) setCSSPageZoom(sessionID string, zoom float64) error {
	zoomValue := ""
	if zoom != 1.0 {
		zoomValue = fmt.Sprintf("%.4g", zoom)
	}
	expression := fmt.Sprintf(`(() => {
		const value = %q;
		if (document.documentElement) {
			document.documentElement.style.zoom = value;
		}
		if (document.body) {
			document.body.style.zoom = "";
		}
		return true;
	})()`, zoomValue)

	_, err := c.callCommand("Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
	}, sessionID, 2*time.Second)
	return err
}

// Evaluate executes JS in the ACTIVE session
func (c *CDPClient) Evaluate(expression string) (interface{}, error) {
	c.mu.Lock()

	// Wait logic
	if c.activeSessionID == "" {
		c.mu.Unlock()
		time.Sleep(500 * time.Millisecond) // Wait for auto-attach
		c.mu.Lock()
		if c.activeSessionID == "" {
			c.mu.Unlock()
			return nil, fmt.Errorf("no active session")
		}
	}

	c.msgID++
	id := c.msgID
	ch := make(chan map[string]interface{}, 1)
	c.pendingRequests[id] = ch

	sessionID := c.activeSessionID
	ws := c.ws
	c.mu.Unlock()

	params := map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
	}
	cmd := map[string]interface{}{
		"id":        id,
		"method":    "Runtime.evaluate",
		"params":    params,
		"sessionId": sessionID,
	}

	if err := c.sendJSON(ws, cmd); err != nil {
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, err
	}

	// Wait for response via channel
	select {
	case response := <-ch:
		// Result parsing
		// In Flat mode, result is inside 'result' key of the response params?
		// No, the response itself IS the message with 'id'.

		if resultParams, ok := response["result"].(map[string]interface{}); ok {
			// resultParams is {result: {type:..., value:...}}
			if res, ok := resultParams["result"].(map[string]interface{}); ok {
				return res["value"], nil
			}
		}
		return nil, fmt.Errorf("invalid response structure: %v", response)
	case <-time.After(2 * time.Second):
		c.mu.Lock()
		delete(c.pendingRequests, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("timeout")
	}
}

// EnsureConnected checks if connection is active and reconnects if needed
func (c *CDPClient) EnsureConnected() error {
	c.mu.Lock()
	// Check both ws and isConnected - connection might be dead even if ws != nil
	if c.ws != nil && c.isConnected {
		c.mu.Unlock()
		return nil
	}

	// Connection is dead or doesn't exist - close old socket if any
	if c.ws != nil {
		c.ws.Close()
		c.ws = nil
	}
	// Reset session state for fresh reconnection
	c.sessions = make(map[string]bool)
	c.sessionURLs = make(map[string]string)
	c.sessionTargets = make(map[string]string)
	c.sessionTypes = make(map[string]string)
	c.sessionLoaderIDs = make(map[string]string)
	c.targetSessions = make(map[string]string)
	c.domClickSessions = make(map[string]bool)
	c.tabVisibilitySessions = make(map[string]bool)
	c.sessionOrder = nil
	c.activeSessionID = ""
	c.activeSessionURL = ""
	c.lastActivePageSessionID = ""
	c.isConnected = false
	c.mu.Unlock()

	cdpDebugf("[CDP] Reconnecting...")
	return c.Connect()
}

// sendJSON provides thread-safe writes to the CDP websocket
func (c *CDPClient) sendJSON(ws *websocket.Conn, cmd interface{}) error {
	if ws == nil {
		return fmt.Errorf("websocket is nil")
	}
	c.wsWriteMu.Lock()
	defer c.wsWriteMu.Unlock()
	return websocket.JSON.Send(ws, cmd)
}

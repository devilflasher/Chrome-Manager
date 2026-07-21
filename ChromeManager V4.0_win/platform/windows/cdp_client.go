//go:build windows

package windows

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const cdpVerboseLogging = false

func cdpVerbosef(format string, args ...interface{}) {
	if cdpVerboseLogging {
		fmt.Printf(format, args...)
	}
}

type cdpClient struct {
	debugPort int
	conn      net.Conn
	reader    *bufio.Reader

	mu                  sync.Mutex
	writeMu             sync.Mutex
	msgID               int
	connected           bool
	initialized         bool
	pendingRequests     map[int]chan map[string]interface{}
	knownPageTargets    map[string]bool
	sessionTargets      map[string]string
	sessionURLs         map[string]string
	sessionTypes        map[string]string
	targetSessions      map[string]string
	sessionAttachedAt   map[string]time.Time
	sessionLoaderIDs    map[string]string
	sessionOrder        []string
	activeSessionID     string // session of the currently visible tab
	pageZoomUnsupported bool

	extensionPanelBehaviorMu       sync.Mutex
	extensionPanelBehaviorOriginal map[string]bool

	onTargetCreated   func(targetID string, targetURL string)
	onTargetDestroyed func(targetID string)
	onTabActivated    func(targetID string, targetURL string)
	onDomAction       func(sessionID string, actionJSON string)
	onURLChanged      func(sessionID string, targetURL string)
	onURLReloaded     func(sessionID string, targetURL string)
	currentZoom       float64
	currentStyleZoom  float64

	// 防回环：执行同步 DOM Action 时设置为 true，JavaScript 检查此标志跳过捕获
	syncActionInProgress sync.Map // key: sessionID, value: bool
}

func newCDPClient(debugPort int) *cdpClient {
	return &cdpClient{
		debugPort:                      debugPort,
		pendingRequests:                make(map[int]chan map[string]interface{}),
		knownPageTargets:               make(map[string]bool),
		currentZoom:                    1.0,
		currentStyleZoom:               1.0,
		sessionTargets:                 make(map[string]string),
		sessionURLs:                    make(map[string]string),
		sessionTypes:                   make(map[string]string),
		targetSessions:                 make(map[string]string),
		sessionAttachedAt:              make(map[string]time.Time),
		sessionLoaderIDs:               make(map[string]string),
		extensionPanelBehaviorOriginal: make(map[string]bool),
	}
}

func (c *cdpClient) connect() error {
	wsURL, err := getBrowserWebSocketURL(c.debugPort)
	if err != nil {
		return err
	}

	parsed, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("invalid browser websocket URL: %w", err)
	}

	host := parsed.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to dial CDP websocket: %w", err)
	}

	key, err := websocketKey()
	if err != nil {
		_ = conn.Close()
		return err
	}

	path := parsed.RequestURI()
	if path == "" {
		path = "/"
	}

	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path,
		parsed.Host,
		key,
	)
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to send websocket handshake: %w", err)
	}

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to read websocket handshake: %w", err)
	}
	if !strings.Contains(status, " 101 ") {
		_ = conn.Close()
		return fmt.Errorf("unexpected websocket handshake status: %s", strings.TrimSpace(status))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("failed to read websocket handshake headers: %w", err)
		}
		if line == "\r\n" {
			break
		}
	}

	c.mu.Lock()
	c.conn = conn
	c.reader = reader
	c.connected = true
	c.initialized = false
	c.mu.Unlock()

	go c.readLoop()

	if err := c.sendCommandNoWait("Target.setAutoAttach", map[string]interface{}{
		"autoAttach":             true,
		"waitForDebuggerOnStart": false,
		"flatten":                true,
	}); err != nil {
		c.close()
		return err
	}

	if err := c.sendCommandNoWait("Target.setDiscoverTargets", map[string]interface{}{"discover": true}); err != nil {
		c.close()
		return err
	}

	go func() {
		time.Sleep(700 * time.Millisecond)
		c.mu.Lock()
		c.initialized = true
		c.mu.Unlock()
	}()

	return nil
}

func getBrowserWebSocketURL(debugPort int) (string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", debugPort))
	if err != nil {
		return "", fmt.Errorf("failed to request CDP version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP version endpoint returned status %d", resp.StatusCode)
	}

	var payload struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode CDP version response: %w", err)
	}
	if payload.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("CDP version response does not include webSocketDebuggerUrl")
	}
	return payload.WebSocketDebuggerURL, nil
}

func websocketKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate websocket key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func (c *cdpClient) close() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.reader = nil
	c.connected = false
	for id, ch := range c.pendingRequests {
		close(ch)
		delete(c.pendingRequests, id)
	}
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
}

func (c *cdpClient) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *cdpClient) isReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected && c.initialized
}

func (c *cdpClient) readLoop() {
	for {
		payload, err := c.readTextFrame()
		if err != nil {
			c.close()
			return
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		c.handleMessage(msg)
	}
}

func (c *cdpClient) readTextFrame() ([]byte, error) {
	c.mu.Lock()
	reader := c.reader
	c.mu.Unlock()
	if reader == nil {
		return nil, io.ErrClosedPipe
	}

	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}

	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)

	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(reader, maskKey[:]); err != nil {
			return nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	switch opcode {
	case 0x1:
		return payload, nil
	case 0x8:
		return nil, io.ErrClosedPipe
	case 0x9:
		_ = c.writeFrame(0xA, payload)
		return c.readTextFrame()
	default:
		return c.readTextFrame()
	}
}

func (c *cdpClient) handleMessage(msg map[string]interface{}) {
	if idValue, ok := msg["id"].(float64); ok {
		id := int(idValue)
		c.mu.Lock()
		ch := c.pendingRequests[id]
		if ch != nil {
			ch <- msg
			delete(c.pendingRequests, id)
		}
		c.mu.Unlock()
		return
	}

	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]interface{})
	sessionID, _ := msg["sessionId"].(string)

	switch method {
	case "Target.attachedToTarget":
		c.handleAttachedToTarget(params)
	case "Target.targetCreated":
		targetInfo, _ := params["targetInfo"].(map[string]interface{})
		targetType, _ := targetInfo["type"].(string)
		targetID, _ := targetInfo["targetId"].(string)
		targetURL, _ := targetInfo["url"].(string)
		timestamp := time.Now().Format("15:04:05.000")
		cdpVerbosef("[CDP] Target.targetCreated: targetID=%s type=%s url=%s timestamp=%s\n", targetID, targetType, targetURL, timestamp)

		if !isSupportedTarget(targetType, targetURL) {
			cdpVerbosef("[CDP] Target.targetCreated: SKIPPED - not supported targetType=%s url=%s\n", targetType, targetURL)
			return
		}
		if targetID == "" {
			cdpVerbosef("[CDP] Target.targetCreated: SKIPPED - empty targetID\n")
			return
		}
		if isIgnoredCDPTargetURL(targetURL) {
			cdpVerbosef("[CDP] Target.targetCreated: SKIPPED - ignored URL\n")
			return
		}

		c.mu.Lock()
		c.knownPageTargets[targetID] = true
		initialized := c.initialized
		callback := c.onTargetCreated
		c.mu.Unlock()

		cdpVerbosef("[CDP] Target.targetCreated: callback triggered (initialized=%v callback=%v)\n", initialized, callback != nil)
		_, isExtensionPopup := extensionPopupID(targetURL)
		if callback != nil && shouldNotifyTargetCreated(targetType) && (initialized || isExtensionPopup) {
			go callback(targetID, targetURL)
		}
	case "Target.targetInfoChanged":
		c.handleTargetInfoChanged(params)
	case "Target.targetDestroyed":
		targetID, _ := params["targetId"].(string)
		if targetID == "" {
			return
		}

		c.mu.Lock()
		known := c.knownPageTargets[targetID]
		delete(c.knownPageTargets, targetID)
		callback := c.onTargetDestroyed
		c.mu.Unlock()

		if known && callback != nil {
			go callback(targetID)
		}
	case "Target.detachedFromTarget":
		sessionID, _ := params["sessionId"].(string)
		if sessionID == "" {
			return
		}
		c.mu.Lock()
		targetID := c.sessionTargets[sessionID]
		delete(c.sessionTargets, sessionID)
		delete(c.sessionURLs, sessionID)
		delete(c.sessionTypes, sessionID)
		delete(c.sessionAttachedAt, sessionID)
		delete(c.sessionLoaderIDs, sessionID)
		if targetID != "" {
			delete(c.targetSessions, targetID)
		}
		c.sessionOrder = removeString(c.sessionOrder, sessionID)
		c.mu.Unlock()
	case "Page.frameNavigated":
		c.handleFrameNavigated(sessionID, params)
	case "Runtime.bindingCalled":
		c.handleRuntimeBindingCalled(sessionID, params)
	}
}

func (c *cdpClient) handleAttachedToTarget(params map[string]interface{}) {
	sessionID, _ := params["sessionId"].(string)
	targetInfo, _ := params["targetInfo"].(map[string]interface{})
	targetType, _ := targetInfo["type"].(string)
	targetID, _ := targetInfo["targetId"].(string)
	targetURL, _ := targetInfo["url"].(string)
	cdpVerbosef("[CDP] Target.attachedToTarget: sessionID=%s targetID=%s type=%s url=%s\n", sessionID, targetID, targetType, targetURL)
	if sessionID == "" || targetID == "" {
		return
	}

	c.mu.Lock()
	c.sessionTargets[sessionID] = targetID
	c.sessionURLs[sessionID] = targetURL
	c.sessionTypes[sessionID] = targetType
	c.targetSessions[targetID] = sessionID
	if _, ok := c.sessionAttachedAt[sessionID]; !ok {
		c.sessionAttachedAt[sessionID] = time.Now()
	}
	c.mu.Unlock()

	if !isSupportedTarget(targetType, targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return
	}

	c.mu.Lock()
	if !containsString(c.sessionOrder, sessionID) {
		c.sessionOrder = append(c.sessionOrder, sessionID)
	}
	c.knownPageTargets[targetID] = true
	// Track the first attached session as active until a tab-switch event updates it.
	if targetType == "page" && c.activeSessionID == "" && !isExtensionURL(targetURL) {
		c.activeSessionID = sessionID
	}
	c.mu.Unlock()

	c.installActivationBinding(sessionID)

	// Self-healing: for about:blank or chrome://newtab/ targets, poll for URL changes that may have been
	// missed because Page.frameNavigated fired before Page.enable took effect.
	if isPlaceholderURL(targetURL) {
		go c.pollForURLChange(sessionID)
	}
}

// isPlaceholderURL 检查 URL 是否是浏览器占位页（非真实目标页面）
func (c *cdpClient) handleTargetInfoChanged(params map[string]interface{}) {
	targetInfo, _ := params["targetInfo"].(map[string]interface{})
	targetType, _ := targetInfo["type"].(string)
	targetID, _ := targetInfo["targetId"].(string)
	targetURL, _ := targetInfo["url"].(string)
	if targetID == "" {
		return
	}

	c.mu.Lock()
	wasKnown := c.knownPageTargets[targetID]
	sessionID := c.targetSessions[targetID]
	previousURL := ""
	if sessionID != "" {
		previousURL = c.sessionURLs[sessionID]
		c.sessionURLs[sessionID] = targetURL
		c.sessionTypes[sessionID] = targetType
		if _, ok := c.sessionAttachedAt[sessionID]; !ok {
			c.sessionAttachedAt[sessionID] = time.Now()
		}
	}
	c.mu.Unlock()

	if !isSupportedTarget(targetType, targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return
	}

	c.mu.Lock()
	c.knownPageTargets[targetID] = true
	if sessionID != "" && !containsString(c.sessionOrder, sessionID) {
		c.sessionOrder = append(c.sessionOrder, sessionID)
	}
	if targetType == "page" && sessionID != "" && c.activeSessionID == "" && !isExtensionURL(targetURL) {
		c.activeSessionID = sessionID
	}
	initialized := c.initialized
	callback := c.onTargetCreated
	c.mu.Unlock()

	notifyCreated := !wasKnown
	if targetURL != previousURL && (targetType == "page" || targetType == "popup") {
		_, wasExtensionPopup := extensionPopupID(previousURL)
		_, isExtensionPopup := extensionPopupID(targetURL)
		if isExtensionPopup && !wasExtensionPopup {
			notifyCreated = true
		}
	}

	cdpVerbosef("[CDP] Target.targetInfoChanged: targetID=%s type=%s url=%s previousURL=%s known=%v sessionID=%s\n", targetID, targetType, targetURL, previousURL, wasKnown, sessionID)
	if sessionID != "" {
		go c.installActivationBinding(sessionID)
	}
	_, isExtensionPopup := extensionPopupID(targetURL)
	if notifyCreated && shouldNotifyTargetCreated(targetType) && callback != nil && (initialized || isExtensionPopup) {
		go callback(targetID, targetURL)
	}
}

func isPlaceholderURL(u string) bool {
	return u == "" ||
		u == "about:blank" ||
		u == "chrome://newtab/" ||
		u == "chrome://new-tab-page/" ||
		u == "edge://newtab/" ||
		u == "edge://new-tab-page/" ||
		u == "brave://newtab/" ||
		u == "brave://new-tab-page/" ||
		u == "opera://startpage/" ||
		u == "opera://newtab/"
}

// pollForURLChange polls for URL changes on a session that started with about:blank or chrome://newtab/.
// This handles the race condition where Chrome navigates the new tab before our
// Page.enable command is processed, causing us to miss the Page.frameNavigated event.
func (c *cdpClient) pollForURLChange(sessionID string) {
	time.Sleep(150 * time.Millisecond)

	deadline := time.Now().Add(1500 * time.Millisecond) // 增加超时时间
	for time.Now().Before(deadline) {
		if !c.isConnected() {
			return
		}

		c.mu.Lock()
		currentKnownURL := c.sessionURLs[sessionID]
		c.mu.Unlock()

		// If URL was already updated by Page.frameNavigated to a real URL, no further polling needed
		if !isPlaceholderURL(currentKnownURL) {
			return
		}

		resp, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
			"expression":    "window.location.href",
			"returnByValue": true,
		}, sessionID, true)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		result, _ := resp["result"].(map[string]interface{})
		inner, _ := result["result"].(map[string]interface{})
		actualURL, _ := inner["value"].(string)

		if !isPlaceholderURL(actualURL) && !isIgnoredCDPTargetURL(actualURL) {
			c.mu.Lock()
			c.sessionURLs[sessionID] = actualURL
			callback := c.onURLChanged
			c.mu.Unlock()

			cdpVerbosef("[CDP] Self-healed URL change for session %s: %s -> %s\n", sessionID, currentKnownURL, actualURL)
			if callback != nil {
				go callback(sessionID, actualURL)
			}
			return
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func (c *cdpClient) handleFrameNavigated(sessionID string, params map[string]interface{}) {
	if sessionID == "" {
		return
	}

	frame, _ := params["frame"].(map[string]interface{})
	if frame == nil {
		return
	}
	if _, hasParent := frame["parentId"]; hasParent {
		return
	}

	targetURL, _ := frame["url"].(string)
	loaderID, _ := frame["loaderId"].(string)
	cdpVerbosef("[CDP] Page.frameNavigated: sessionID=%s url=%s\n", sessionID, targetURL)
	if targetURL == "" {
		return
	}

	c.mu.Lock()
	previousURL := c.sessionURLs[sessionID]
	previousLoaderID := c.sessionLoaderIDs[sessionID]
	initialized := c.initialized
	c.sessionURLs[sessionID] = targetURL
	if loaderID != "" {
		c.sessionLoaderIDs[sessionID] = loaderID
	}
	callback := c.onURLChanged
	reloadCallback := c.onURLReloaded
	c.mu.Unlock()

	if callback != nil {
		go callback(sessionID, targetURL)
	}
	if reloadCallback != nil &&
		initialized &&
		previousURL == targetURL &&
		!isPlaceholderURL(targetURL) &&
		(loaderID == "" || previousLoaderID == "" || loaderID != previousLoaderID) {
		go reloadCallback(sessionID, targetURL)
	}

	// 关键修复：在每次顶层页面导航后重新注入捕获脚本。
	// Page.addScriptToEvaluateOnNewDocument 会被严格 CSP（如 app.startale.com）阻止，
	// 但 Runtime.evaluate 能绕过 CSP。脚本内置的热清理机制防止处理器重复绑定。
	if !isIgnoredCDPTargetURL(targetURL) {
		go c.installActivationBinding(sessionID)
	}
}

func (c *cdpClient) installActivationBinding(sessionID string) {
	const bindingName = "chromeManagerTabActivated"
	const visibilityScript = `(() => {
  if (window.__chromeManagerTabActivationInstalled) return;
  window.__chromeManagerTabActivationInstalled = true;
  const notify = () => {
    if (document.visibilityState === "visible" && window.chromeManagerTabActivated) {
      window.chromeManagerTabActivated(window.location.href);
    }
  };
  document.addEventListener("visibilitychange", notify);
  window.addEventListener("focus", notify);
  notify();
})();`

	const domActionBindingName = "chromeDuoDomAction"
	const domActionCaptureScript = `(() => {
  // 清理旧处理器，避免重复触发，并确保捕获算法保持最新。
  if (window.__chromeDuoDomActionMousedownHandler) {
    document.removeEventListener('mousedown', window.__chromeDuoDomActionMousedownHandler, true);
  }
  if (window.__chromeDuoDomActionClickHandler) {
    document.removeEventListener('click', window.__chromeDuoDomActionClickHandler, true);
    document.removeEventListener('input', window.__chromeDuoDomActionInputHandler, true);
    window.removeEventListener('scroll', window.__chromeDuoDomActionScrollHandler, true);
  }
  function getSingleLevelSelector(el) {
    if (!el || el.nodeType !== 1) return '';
    if (el.id && typeof el.id === 'string' && !/^[0-9]|^[0-9a-f]{8}-|^[0-9a-f]{32}|[a-f0-9]{8,}/i.test(el.id)) {
      return '#' + cssEscape(el.id);
    }
    const testIdAttrs = ['data-testid', 'data-qa', 'data-cy', 'data-action', 'aria-label'];
    for (const attr of testIdAttrs) {
      const val = el.getAttribute(attr);
      if (val) {
        return el.tagName.toLowerCase() + '[' + attr + '="' + val.replace(/"/g, '\\\\"') + '"]';
      }
    }
    if (el.name && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT')) {
      return el.tagName.toLowerCase() + '[name="' + el.name.replace(/"/g, '\\\\"') + '"]';
    }
    
    let selector = el.tagName.toLowerCase();
    
    // 极其安全地提取并拼接稳定 class (规避 SVG className 转换崩溃漏洞)
    const classAttr = el.getAttribute && el.getAttribute('class');
    if (classAttr && typeof classAttr === 'string') {
      const classes = classAttr.split(/\s+/).filter(c => {
        if (!c) return false;
        if (/^[0-9_-]+$/.test(c)) return false;
        if (/[a-f0-9]{8,}/i.test(c)) return false;
        return true;
      });
      if (classes.length > 0) {
        selector += '.' + classes.map(c => cssEscape(c)).join('.');
      }
    }
    
    const parent = el.parentNode || (el.getRootNode ? el.getRootNode() : null);
    const children = parent ? (parent.children || (parent.childNodes ? Array.from(parent.childNodes).filter(n => n.nodeType === 1) : [])) : [];
    const siblings = Array.from(children);
    const sameTagSiblings = siblings.filter(s => s.tagName === el.tagName);
    if (sameTagSiblings.length > 1) {
      const index = sameTagSiblings.indexOf(el) + 1;
      selector += ':nth-of-type(' + index + ')';
    }
    return selector;
  }

  function getUniqueSelector(el) {
    if (!el || el.nodeType !== 1) return '';
    const parts = [];
    let current = el;
    while (current) {
      if (current.nodeType === 11 && current.host) {
        current = current.host;
        continue;
      }
      
      const path = [];
      let layerCurrent = current;
      while (layerCurrent && layerCurrent.nodeType === 1) {
        const selector = getSingleLevelSelector(layerCurrent);
        path.unshift(selector);
        
        if (layerCurrent.id && typeof layerCurrent.id === 'string' && !/^[0-9]|^[0-9a-f]{8}-|^[0-9a-f]{32}|[a-f0-9]{8,}/i.test(layerCurrent.id)) {
          break;
        }
        const hasTestId = ['data-testid', 'data-qa', 'data-cy', 'data-action', 'aria-label'].some(attr => layerCurrent.getAttribute && layerCurrent.getAttribute(attr));
        if (hasTestId) {
          break;
        }
        
        layerCurrent = layerCurrent.parentNode;
      }
      
      parts.unshift(path.join(' > '));
      
      const root = current.getRootNode();
      if (root && root.host) {
        current = root.host;
      } else {
        break;
      }
    }
    
    return parts.filter(p => p.trim() !== '').join(' >>> ');
  }

  function cssEscape(value) {
    if (typeof CSS !== 'undefined' && CSS.escape) return CSS.escape(value);
    return value.replace(/([!"#$%&'()*+,./:;<=>?@[\\]^{|}~])/g, '\\\\$1');
  }

  // SVG 内部元素集合（用于过滤无意义的深层 SVG 节点）
  const SVG_INTERNALS = new Set(['path','circle','rect','line','polygon','polyline','use','g','defs','clippath','ellipse','text','tspan','image','symbol','marker']);
  // 扩展后的交互性 ARIA 角色集合
  const INTERACTIVE_ROLES = new Set(['button','link','tab','menuitem','option','checkbox','radio','switch','slider','combobox','treeitem','gridcell','menuitemcheckbox','menuitemradio']);

  // 两阶段目标选择：Phase1 交互元素优先，Phase2 非平凡元素兜底
  function findClickTarget(path) {
    // Phase 1: 查找最近的交互型祖先节点（扩展检测范围）
    for (const el of path) {
      if (!el || el.nodeType !== 1) continue;
      const tag = el.tagName.toUpperCase();
      if (tag === 'BODY' || tag === 'HTML') break;
      if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' || tag === 'SELECT' || tag === 'TEXTAREA' || tag === 'LABEL' || tag === 'SUMMARY') {
        return el;
      }
      const role = el.getAttribute && el.getAttribute('role');
      if (role && INTERACTIVE_ROLES.has(role)) return el;
      if (el.getAttribute && (el.getAttribute('data-testid') || el.getAttribute('data-action') || el.getAttribute('data-qa') || el.getAttribute('data-cy'))) return el;
      if (el.hasAttribute && el.hasAttribute('onclick')) return el;
      const tabIdx = el.getAttribute && el.getAttribute('tabindex');
      if (tabIdx !== null && tabIdx !== undefined && parseInt(tabIdx, 10) >= 0) return el;
      try {
        const style = window.getComputedStyle(el);
        if (style && style.cursor === 'pointer') return el;
      } catch(e) {}
    }
    // Phase 2: 兜底 - 使用路径中第一个非平凡元素
    for (const el of path) {
      if (!el || el.nodeType !== 1) continue;
      const tag = el.tagName.toLowerCase();
      if (tag === 'html' || tag === 'body') continue;
      if (SVG_INTERNALS.has(tag)) continue;
      return el;
    }
    return path[0] || null;
  }

  // Hook Mousedown (在元素被 React 销毁前抓取 Selector 唯一标识)
  window.__chromeDuoDomActionMousedownHandler = (e) => {
    if (!e.isTrusted) return;
    const path = e.composedPath ? e.composedPath() : [];
    const target = findClickTarget(path) || e.target;
    const sel = getUniqueSelector(target);
    window.__lastMousedownSelector = sel;
    window.__lastMousedownTime = Date.now();
  };

  // Hook Click (结合 mousedown 对已销毁的 Detached DOM 节点实施退避兜底)
  window.__chromeDuoDomActionClickHandler = (e) => {
    // 防回环：如果正在执行同步操作，跳过捕获（防止 slave 同步过来的操作又被捕获并广播回去）
    if (window.__chromeDuoSyncActionInProgress) return;
    if (!e.isTrusted) return;
    const path = e.composedPath ? e.composedPath() : [];
    const target = findClickTarget(path) || e.target;

    // 检测会打开新标签页的点击（Ctrl/Meta+click 或 <a target="_blank">）
    // 这类点击不发送同步动作，由 handleMasterTargetCreated 处理标签创建
    let willOpenNewTab = e.ctrlKey || e.metaKey;
    if (!willOpenNewTab) {
      for (const el of path) {
        if (el.tagName === 'A' && el.target && el.target.toLowerCase() === '_blank') {
          willOpenNewTab = true;
          break;
        }
        if (el === document) break;
      }
    }
    if (willOpenNewTab) return;

    let selector = "";
    const isDetached = !target || !document.contains(target);
    
    if (isDetached && window.__lastMousedownSelector && (Date.now() - window.__lastMousedownTime < 300)) {
      selector = window.__lastMousedownSelector;
    } else {
      selector = getUniqueSelector(target);
      if (!selector && window.__lastMousedownSelector && (Date.now() - window.__lastMousedownTime < 300)) {
        selector = window.__lastMousedownSelector;
      }
    }

    // 过滤 html 和 body 最外层，避免无效的背景/穿透点击对模态框和同步造成毁灭性干扰
    if (selector && selector !== 'html' && selector !== 'body' && window.chromeDuoDomAction) {
      const payload = JSON.stringify({
        type: 'click',
        selector: selector,
        vpX: e.clientX / window.innerWidth,
        vpY: e.clientY / window.innerHeight
      });
      window.chromeDuoDomAction(payload);
    }
  };

  // Hook Input (命名全局函数以便清理)
  window.__chromeDuoDomActionInputHandler = (e) => {
    if (!e.isTrusted) return;
    const target = (e.composedPath && e.composedPath()[0]) || e.target;
    const selector = getUniqueSelector(target);
    if (selector && window.chromeDuoDomAction) {
      const val = target.isContentEditable ? target.innerHTML : target.value;
      window.chromeDuoDomAction(JSON.stringify({
        type: 'input',
        selector: selector,
        value: val
      }));
    }
  };

  // Hook Scroll (命名全局函数以便清理)
  let lastScroll = 0;
  let lastWheelTime = 0;
  window.addEventListener('wheel', () => {
    lastWheelTime = Date.now();
  }, { passive: true });

  window.__chromeDuoDomActionScrollHandler = (e) => {
    // 若最近 200ms 内有物理滚轮滚动，由物理滚轮投射接管，直接丢弃该 DOM 滚动同步包
    if (Date.now() - lastWheelTime < 200) {
      return;
    }
    const now = Date.now();
    if (now - lastScroll < 120) return;
    lastScroll = now;

    const docEl = document.documentElement;
    const maxScrollY = docEl.scrollHeight - window.innerHeight;
    const percentY = maxScrollY > 0 ? window.scrollY / maxScrollY : 0;

    const maxScrollX = docEl.scrollWidth - window.innerWidth;
    const percentX = maxScrollX > 0 ? window.scrollX / maxScrollX : 0;

    if (window.chromeDuoDomAction) {
      window.chromeDuoDomAction(JSON.stringify({
        type: 'scroll',
        percentX: percentX,
        percentY: percentY
      }));
    }
  };

  // 重新绑定全新的全局处理器
  document.addEventListener('mousedown', window.__chromeDuoDomActionMousedownHandler, true);
  document.addEventListener('click', window.__chromeDuoDomActionClickHandler, true);
  document.addEventListener('input', window.__chromeDuoDomActionInputHandler, true);
  window.addEventListener('scroll', window.__chromeDuoDomActionScrollHandler, true);
})();`

	_ = c.sendCommandNoWait("Runtime.addBinding", map[string]interface{}{"name": bindingName, "executionContextName": ""})
	_ = c.sendCommandNoWait("Runtime.addBinding", map[string]interface{}{"name": domActionBindingName, "executionContextName": ""})
	_ = c.sendCommandNoWait("Runtime.enable", nil)
	_ = c.sendSessionCommandNoWait(sessionID, "Page.enable", nil)
	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.addBinding", map[string]interface{}{"name": bindingName})
	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.addBinding", map[string]interface{}{"name": domActionBindingName})
	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.enable", nil)
	_ = c.sendSessionCommandNoWait(sessionID, "Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": visibilityScript})
	_ = c.sendSessionCommandNoWait(sessionID, "Page.addScriptToEvaluateOnNewDocument", map[string]interface{}{"source": domActionCaptureScript})
	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.evaluate", map[string]interface{}{"expression": visibilityScript})
	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.evaluate", map[string]interface{}{"expression": domActionCaptureScript})
}

func (c *cdpClient) handleRuntimeBindingCalled(sessionID string, params map[string]interface{}) {
	name, _ := params["name"].(string)
	if name == "chromeDuoDomAction" {
		payload, _ := params["payload"].(string)
		if sessionID != "" && c.isSyncActionInProgress(sessionID) {
			cdpVerbosef("[DOM ACTION CAPTURE] Suppressed sync echo sessionID=%s payload=%s\n", sessionID, payload)
			return
		}
		cdpVerbosef("[DOM ACTION CAPTURE] Received action payload: %s\n", payload)
		if callback := c.onDomAction; callback != nil {
			go callback(sessionID, payload)
		}
		return
	}

	if name != "chromeManagerTabActivated" {
		return
	}

	payload, _ := params["payload"].(string)
	if sessionID == "" {
		return
	}

	c.mu.Lock()
	sessionType := c.sessionTypes[sessionID]
	c.mu.Unlock()
	if sessionType != "page" {
		return
	}

	activatedURL := payload
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &activatedURL)
	}

	c.mu.Lock()
	targetID := c.sessionTargets[sessionID]
	if activatedURL == "" || activatedURL == "about:blank" {
		if knownURL := c.sessionURLs[sessionID]; knownURL != "" && knownURL != "about:blank" {
			activatedURL = knownURL
		}
	}
	if activatedURL != "" {
		c.sessionURLs[sessionID] = activatedURL
	}
	// Keep activeSessionID pointing at a normal user-visible tab, not an extension popup.
	if !isExtensionURL(activatedURL) {
		c.activeSessionID = sessionID
	}
	callback := c.onTabActivated
	c.mu.Unlock()

	if targetID != "" && callback != nil {
		go callback(targetID, activatedURL)
	}
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

func isIgnoredCDPTargetURL(targetURL string) bool {
	if strings.HasPrefix(targetURL, "devtools://") || strings.Contains(targetURL, "omnibox-popup") {
		return true
	}
	if strings.HasPrefix(targetURL, "chrome-extension://") {
		if strings.Contains(targetURL, "background") || strings.Contains(targetURL, "service_worker") {
			return true
		}
	}
	return false
}

func isExtensionURL(targetURL string) bool {
	return strings.HasPrefix(targetURL, "chrome-extension://")
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
	return isExtensionURL(targetURL) && isVisibleExtensionTargetType(targetType) && !isIgnoredCDPTargetURL(targetURL)
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

func (c *cdpClient) hasExtensionPopupSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sessionID, targetURL := range c.sessionURLs {
		if isVisibleExtensionTarget(c.sessionTypes[sessionID], targetURL) {
			return true
		}
	}
	return false
}

func shouldNotifyTargetCreated(targetType string) bool {
	return targetType == "page" || targetType == "popup"
}

func isSupportedTarget(targetType string, targetURL string) bool {
	if targetType == "page" {
		return true
	}
	// 允许以 chrome-extension:// 开头的 other、webview、iframe、popup 等可视化扩展 Target
	if (targetType == "other" || targetType == "webview" || targetType == "iframe" || targetType == "popup") && strings.HasPrefix(targetURL, "chrome-extension://") {
		return true
	}
	return false
}

func (c *cdpClient) findSessionIDByURL(targetURL string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sID := c.sessionOrder[i]
		u := c.sessionURLs[sID]
		if u == targetURL {
			return sID
		}
	}
	targetExtensionID := extensionIDFromURL(targetURL)
	targetExtensionKey := extensionPageMatchKey(targetURL)
	if targetExtensionKey != "" {
		for i := len(c.sessionOrder) - 1; i >= 0; i-- {
			sID := c.sessionOrder[i]
			u := c.sessionURLs[sID]
			if isVisibleExtensionTarget(c.sessionTypes[sID], u) && extensionPageMatchKey(u) == targetExtensionKey {
				return sID
			}
		}
	}
	if targetExtensionID != "" {
		for i := len(c.sessionOrder) - 1; i >= 0; i-- {
			sID := c.sessionOrder[i]
			u := c.sessionURLs[sID]
			if isVisibleExtensionTarget(c.sessionTypes[sID], u) && extensionIDFromURL(u) == targetExtensionID {
				return sID
			}
		}
	}
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sID := c.sessionOrder[i]
		u := c.sessionURLs[sID]
		if strings.HasPrefix(u, targetURL) || strings.HasPrefix(targetURL, u) {
			return sID
		}
	}
	return ""
}

func (c *cdpClient) findTargetIDByURL(targetURL string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sID := c.sessionOrder[i]
		u := c.sessionURLs[sID]
		if u == targetURL {
			return c.sessionTargets[sID]
		}
	}
	targetExtensionID := extensionIDFromURL(targetURL)
	targetExtensionKey := extensionPageMatchKey(targetURL)
	if targetExtensionKey != "" {
		for i := len(c.sessionOrder) - 1; i >= 0; i-- {
			sID := c.sessionOrder[i]
			u := c.sessionURLs[sID]
			if isVisibleExtensionTarget(c.sessionTypes[sID], u) && extensionPageMatchKey(u) == targetExtensionKey {
				return c.sessionTargets[sID]
			}
		}
	}
	if targetExtensionID != "" {
		for i := len(c.sessionOrder) - 1; i >= 0; i-- {
			sID := c.sessionOrder[i]
			u := c.sessionURLs[sID]
			if isVisibleExtensionTarget(c.sessionTypes[sID], u) && extensionIDFromURL(u) == targetExtensionID {
				return c.sessionTargets[sID]
			}
		}
	}
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sID := c.sessionOrder[i]
		u := c.sessionURLs[sID]
		if strings.HasPrefix(u, targetURL) || strings.HasPrefix(targetURL, u) {
			return c.sessionTargets[sID]
		}
	}
	return ""
}

func (c *cdpClient) findNewestPlaceholderPageTargetAttachedSince(since time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		if c.sessionTypes[sessionID] != "page" {
			continue
		}
		targetID := c.sessionTargets[sessionID]
		if targetID == "" || !isPlaceholderURL(c.sessionURLs[sessionID]) {
			continue
		}
		if attachedAt, ok := c.sessionAttachedAt[sessionID]; ok && attachedAt.Before(since) {
			continue
		}
		return targetID
	}
	return ""
}

func (c *cdpClient) ensureTargetAttachedByURL(targetURL string) string {
	if targetURL == "" {
		return ""
	}
	if targetID := c.findTargetIDByURL(targetURL); targetID != "" {
		return targetID
	}

	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		fmt.Printf("[CDP] Target.getTargets failed for %s: %v\n", targetURL, err)
		return ""
	}

	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		currentURL, _ := info["url"].(string)
		if targetID == "" || currentURL != targetURL || !isSupportedTarget(targetType, currentURL) || isIgnoredCDPTargetURL(currentURL) {
			continue
		}

		return c.ensureTargetAttached(targetID, targetType, currentURL)
	}

	return ""
}

func (c *cdpClient) ensureExtensionTargetsAttached(extensionID string) {
	if extensionID == "" || !c.isConnected() {
		return
	}

	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		cdpVerbosef("[CDP] Target.getTargets failed for extension %s: %v\n", extensionID, err)
		return
	}

	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
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
		c.ensureTargetAttached(targetID, targetType, targetURL)
	}
}

func (c *cdpClient) ensureVisibleExtensionTargetsAttached() bool {
	if !c.isConnected() {
		return false
	}

	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		cdpVerbosef("[CDP] Target.getTargets failed for visible extension targets: %v\n", err)
		return false
	}

	found := false
	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || !isVisibleExtensionTarget(targetType, targetURL) {
			continue
		}
		if c.ensureTargetAttached(targetID, targetType, targetURL) != "" {
			found = true
		}
	}
	return found
}

func (c *cdpClient) ensureTargetAttached(targetID string, targetType string, targetURL string) string {
	if targetID == "" {
		return ""
	}

	c.mu.Lock()
	sessionID := c.targetSessions[targetID]
	if sessionID != "" {
		c.sessionURLs[sessionID] = targetURL
		c.sessionTypes[sessionID] = targetType
		c.knownPageTargets[targetID] = true
		if !containsString(c.sessionOrder, sessionID) {
			c.sessionOrder = append(c.sessionOrder, sessionID)
		}
	}
	c.mu.Unlock()

	if sessionID != "" {
		c.installActivationBinding(sessionID)
		return targetID
	}

	attachResp, err := c.sendCommand("Target.attachToTarget", map[string]interface{}{
		"targetId": targetID,
		"flatten":  true,
	}, true)
	if err != nil {
		time.Sleep(20 * time.Millisecond)
		if sessionID := c.getSessionIDByTargetID(targetID); sessionID != "" {
			c.installActivationBinding(sessionID)
			return targetID
		}
		cdpVerbosef("[CDP] Target.attachToTarget failed for %s targetID=%s: %v\n", targetURL, targetID, err)
		return ""
	}

	attachResult, _ := attachResp["result"].(map[string]interface{})
	sessionID, _ = attachResult["sessionId"].(string)
	if sessionID == "" {
		return ""
	}

	c.mu.Lock()
	c.sessionTargets[sessionID] = targetID
	c.sessionURLs[sessionID] = targetURL
	c.sessionTypes[sessionID] = targetType
	c.targetSessions[targetID] = sessionID
	if !containsString(c.sessionOrder, sessionID) {
		c.sessionOrder = append(c.sessionOrder, sessionID)
	}
	c.knownPageTargets[targetID] = true
	if targetType == "page" && c.activeSessionID == "" && !isExtensionURL(targetURL) {
		c.activeSessionID = sessionID
	}
	c.mu.Unlock()

	c.installActivationBinding(sessionID)
	cdpVerbosef("[CDP] Target.attachToTarget ensured: sessionID=%s targetID=%s type=%s url=%s\n", sessionID, targetID, targetType, targetURL)
	return targetID
}

type extensionRuntimeTarget struct {
	extensionID string
	targetID    string
	targetType  string
	targetURL   string
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

func (c *cdpClient) getExtensionRuntimeTargets() ([]extensionRuntimeTarget, error) {
	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		return nil, fmt.Errorf("Target.getTargets failed: %w", err)
	}

	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	byExtension := make(map[string]extensionRuntimeTarget)
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		tType, _ := info["type"].(string)
		tID, _ := info["targetId"].(string)
		tURL, _ := info["url"].(string)
		extensionID := extensionIDFromURL(tURL)
		if tID == "" || extensionID == "" {
			continue
		}
		if !isExtensionRuntimeTargetType(tType, tURL) {
			continue
		}
		byExtension[extensionID] = preferExtensionRuntimeTarget(byExtension[extensionID], extensionRuntimeTarget{
			extensionID: extensionID,
			targetID:    tID,
			targetType:  tType,
			targetURL:   tURL,
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

func (c *cdpClient) getExtensionRuntimeTarget(extensionID string) (extensionRuntimeTarget, error) {
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

func (c *cdpClient) getExtensionRuntimeTargetWithRetry(extensionID string, timeout time.Duration) (extensionRuntimeTarget, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		target, err := c.getExtensionRuntimeTarget(extensionID)
		if err == nil {
			return target, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	return extensionRuntimeTarget{}, lastErr
}

func (c *cdpClient) ensureAttachedToTarget(targetID, targetType, targetURL string) (string, error) {
	sessionID := c.getSessionIDByTargetID(targetID)
	if sessionID == "" {
		attachResp, err := c.sendCommand("Target.attachToTarget", map[string]interface{}{
			"targetId": targetID,
			"flatten":  true,
		}, true)
		if err != nil {
			return "", fmt.Errorf("Target.attachToTarget failed for %s: %w", targetID, err)
		}
		attachResult, _ := attachResp["result"].(map[string]interface{})
		sessionID, _ = attachResult["sessionId"].(string)
		if sessionID == "" {
			return "", fmt.Errorf("Target.attachToTarget returned empty session for %s", targetID)
		}

		c.mu.Lock()
		c.sessionTargets[sessionID] = targetID
		c.sessionURLs[sessionID] = targetURL
		c.sessionTypes[sessionID] = targetType
		c.targetSessions[targetID] = sessionID
		if !containsString(c.sessionOrder, sessionID) {
			c.sessionOrder = append(c.sessionOrder, sessionID)
		}
		c.knownPageTargets[targetID] = true
		c.mu.Unlock()
	}
	return sessionID, nil
}

func (c *cdpClient) prepareExtensionActionPopupBehavior() int {
	targets, err := c.getExtensionRuntimeTargets()
	if err != nil {
		fmt.Printf("[ExtensionPopup] prepare target scan failed: %v\n", err)
		return 0
	}

	changed := 0
	for _, target := range targets {
		sessionID, err := c.ensureAttachedToTarget(target.targetID, target.targetType, target.targetURL)
		if err != nil {
			fmt.Printf("[ExtensionPopup] prepare attach failed extension=%s: %v\n", target.extensionID, err)
			continue
		}
		_, _ = c.sendCommandWithSession("Runtime.enable", nil, sessionID, false)
		evalResp, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
			"expression": `(async()=>{
const out={hasActionPopup:false,hasSidePanel:false,previous:null,changed:false};
const manifest=chrome.runtime.getManifest();
out.hasActionPopup=!!(manifest.action&&manifest.action.default_popup);
out.hasSidePanel=!!(chrome.sidePanel&&chrome.sidePanel.getPanelBehavior&&chrome.sidePanel.setPanelBehavior);
if(!out.hasActionPopup||!out.hasSidePanel) return JSON.stringify(out);
const behavior=await chrome.sidePanel.getPanelBehavior();
out.previous=!!behavior.openPanelOnActionClick;
if(out.previous){
  await chrome.sidePanel.setPanelBehavior({openPanelOnActionClick:false});
  out.changed=true;
}
return JSON.stringify(out);
})()`,
			"awaitPromise":  true,
			"returnByValue": true,
		}, sessionID, true)
		if err != nil {
			fmt.Printf("[ExtensionPopup] prepare behavior failed extension=%s: %v\n", target.extensionID, err)
			continue
		}
		if exception, ok := evalResp["exceptionDetails"]; ok {
			fmt.Printf("[ExtensionPopup] prepare behavior exception extension=%s: %v\n", target.extensionID, exception)
			continue
		}
		result, _ := evalResp["result"].(map[string]interface{})
		inner, _ := result["result"].(map[string]interface{})
		value, _ := inner["value"].(string)
		var parsed struct {
			Previous *bool `json:"previous"`
			Changed  bool  `json:"changed"`
		}
		if err := json.Unmarshal([]byte(value), &parsed); err != nil {
			continue
		}
		if parsed.Previous != nil {
			c.extensionPanelBehaviorMu.Lock()
			if c.extensionPanelBehaviorOriginal == nil {
				c.extensionPanelBehaviorOriginal = make(map[string]bool)
			}
			if _, exists := c.extensionPanelBehaviorOriginal[target.extensionID]; !exists {
				c.extensionPanelBehaviorOriginal[target.extensionID] = *parsed.Previous
			}
			c.extensionPanelBehaviorMu.Unlock()
		}
		if parsed.Changed {
			changed++
		}
	}
	return changed
}

func (c *cdpClient) restoreExtensionActionPopupBehavior() {
	c.extensionPanelBehaviorMu.Lock()
	originals := make(map[string]bool, len(c.extensionPanelBehaviorOriginal))
	for extensionID, previous := range c.extensionPanelBehaviorOriginal {
		originals[extensionID] = previous
	}
	c.extensionPanelBehaviorOriginal = make(map[string]bool)
	c.extensionPanelBehaviorMu.Unlock()

	for extensionID, previous := range originals {
		target, err := c.getExtensionRuntimeTarget(extensionID)
		if err != nil {
			continue
		}
		sessionID, err := c.ensureAttachedToTarget(target.targetID, target.targetType, target.targetURL)
		if err != nil {
			continue
		}
		_, _ = c.sendCommandWithSession("Runtime.enable", nil, sessionID, false)
		_, _ = c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
			"expression":    fmt.Sprintf(`(async()=>{ if(chrome.sidePanel&&chrome.sidePanel.setPanelBehavior) await chrome.sidePanel.setPanelBehavior({openPanelOnActionClick:%t}); })()`, previous),
			"awaitPromise":  true,
			"returnByValue": true,
		}, sessionID, true)
	}
}

func (c *cdpClient) openExtensionActionPopup(extensionID string, activateAfterOpen bool) error {
	if extensionID == "" {
		return fmt.Errorf("empty extension id")
	}

	existingPopupTargets := c.extensionPopupTargetIDs(extensionID)
	c.bringActivePageToFront()

	triggerErr := c.triggerExtensionAction(extensionID)
	if triggerErr == nil {
		if c.waitForVisibleExtensionTarget(extensionID, 800*time.Millisecond) {
			if activateAfterOpen {
				c.activateNewExtensionPopupTarget(extensionID, existingPopupTargets)
			}
			return nil
		}
	} else {
		cdpVerbosef("[ExtensionPopup] Extensions.triggerAction failed extension=%s: %v\n", extensionID, triggerErr)
	}

	target, err := c.getExtensionRuntimeTargetWithRetry(extensionID, 900*time.Millisecond)
	if err != nil {
		return err
	}
	sessionID, err := c.ensureAttachedToTarget(target.targetID, target.targetType, target.targetURL)
	if err != nil {
		return err
	}

	_, _ = c.sendCommandWithSession("Runtime.enable", nil, sessionID, false)
	evalResp, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression": `(async()=>{
const manifest=chrome.runtime.getManifest();
const defaultPopup=manifest.action&&manifest.action.default_popup;
if(chrome.sidePanel&&chrome.sidePanel.getPanelBehavior&&chrome.sidePanel.setPanelBehavior&&defaultPopup){
  const behavior=await chrome.sidePanel.getPanelBehavior();
  if(behavior.openPanelOnActionClick){
    await chrome.sidePanel.setPanelBehavior({openPanelOnActionClick:false});
  }
}
if(!chrome.action||!chrome.action.openPopup) throw new Error("chrome.action.openPopup unavailable");
try{
  await chrome.action.openPopup();
  return "OPENED";
}catch(firstErr){
  if(!defaultPopup||!chrome.action.setPopup) throw firstErr;
  try{
    let tabId;
    if(chrome.tabs&&chrome.tabs.query){
      const tabs=await chrome.tabs.query({active:true,currentWindow:true});
      tabId=tabs&&tabs[0]&&tabs[0].id;
    }
    if(tabId!==undefined&&tabId!==null){
      let currentPopup="";
      if(chrome.action.getPopup){
        try{ currentPopup=await chrome.action.getPopup({tabId}); }catch(e){}
      }
      if(!currentPopup){
        await chrome.action.setPopup({tabId,popup:defaultPopup});
      }
    }else{
      await chrome.action.setPopup({popup:defaultPopup});
    }
    await chrome.action.openPopup();
    return "OPENED_AFTER_SET_POPUP";
  }catch(secondErr){
    throw new Error((firstErr&&firstErr.message?firstErr.message:String(firstErr))+"; setPopup retry failed: "+(secondErr&&secondErr.message?secondErr.message:String(secondErr)));
  }
}
})()`,
		"awaitPromise":  true,
		"returnByValue": true,
	}, sessionID, true)
	if err != nil {
		return fmt.Errorf("runtime.evaluate openPopup failed: %w", err)
	}
	if exception, ok := evalResp["exceptionDetails"]; ok {
		return fmt.Errorf("chrome.action.openPopup exception: %v", exception)
	}
	if !c.waitForVisibleExtensionTarget(extensionID, 700*time.Millisecond) {
		return fmt.Errorf("extension popup not visible after openPopup")
	}
	if activateAfterOpen {
		c.activateNewExtensionPopupTarget(extensionID, existingPopupTargets)
	}
	return nil
}

func (c *cdpClient) triggerExtensionAction(extensionID string) error {
	targetID := c.findTabTargetForActivation()
	if targetID == "" {
		return fmt.Errorf("no active tab target")
	}
	_, err := c.sendCommand("Extensions.triggerAction", map[string]interface{}{
		"id":       extensionID,
		"targetId": targetID,
	}, true)
	return err
}

func (c *cdpClient) findTabTargetForActivation() string {
	activeURL := ""
	if sessionID := c.findPageSessionForActivation(); sessionID != "" {
		c.mu.Lock()
		activeURL = c.sessionURLs[sessionID]
		c.mu.Unlock()
	}

	resp, err := c.sendCommand("Target.getTargets", map[string]interface{}{
		"filter": []map[string]interface{}{
			{"type": "browser", "exclude": true},
			{},
		},
	}, true)
	if err != nil {
		return ""
	}

	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	fallback := ""
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetType != "tab" || targetID == "" || targetURL == "" || isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
			continue
		}
		if activeURL != "" && targetURL == activeURL {
			return targetID
		}
		if fallback == "" {
			fallback = targetID
		}
	}
	return fallback
}

func (c *cdpClient) extensionPopupTargetIDs(extensionID string) map[string]bool {
	targets := make(map[string]bool)
	for _, target := range c.extensionPopupTargets(extensionID) {
		targets[target.targetID] = true
	}
	return targets
}

func (c *cdpClient) waitForVisibleExtensionTarget(extensionID string, timeout time.Duration) bool {
	if extensionID == "" {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		targets := c.extensionPopupTargets(extensionID)
		if len(targets) > 0 {
			for _, target := range targets {
				c.ensureAttachedToTarget(target.targetID, target.targetType, target.targetURL)
			}
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (c *cdpClient) activateNewExtensionPopupTarget(extensionID string, existing map[string]bool) {
	deadline := time.Now().Add(1200 * time.Millisecond)
	var fallback extensionRuntimeTarget
	for time.Now().Before(deadline) {
		targets := c.extensionPopupTargets(extensionID)
		for _, target := range targets {
			if fallback.targetID == "" {
				fallback = target
			}
			if existing[target.targetID] {
				continue
			}
			c.activateExtensionPopupTarget(target)
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	if fallback.targetID != "" {
		c.activateExtensionPopupTarget(fallback)
	}
}

func (c *cdpClient) activateExtensionPopupTarget(target extensionRuntimeTarget) {
	sessionID, err := c.ensureAttachedToTarget(target.targetID, target.targetType, target.targetURL)
	if err != nil {
		fmt.Printf("[ExtensionPopup] activate attach failed extension=%s targetID=%s: %v\n", target.extensionID, target.targetID, err)
		return
	}
	_ = c.activateTarget(target.targetID)
	_ = c.sendSessionCommandNoWait(sessionID, "Page.bringToFront", nil)
	cdpVerbosef("[ExtensionPopup] activated popup target extension=%s targetID=%s url=%s\n", target.extensionID, target.targetID, target.targetURL)
}

func (c *cdpClient) extensionPopupTargets(extensionID string) []extensionRuntimeTarget {
	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		return nil
	}
	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	targets := make([]extensionRuntimeTarget, 0, 2)
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || targetURL == "" {
			continue
		}
		foundExtensionID, ok := extensionPopupID(targetURL)
		if !ok || foundExtensionID != extensionID {
			continue
		}
		if !isVisibleExtensionTargetType(targetType) {
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

func (c *cdpClient) closeExtensionPageTargets() {
	if !c.isConnected() {
		return
	}

	resp, err := c.sendCommand("Target.getTargets", nil, true)
	if err != nil {
		return
	}
	result, _ := resp["result"].(map[string]interface{})
	targetInfos, _ := result["targetInfos"].([]interface{})
	for _, item := range targetInfos {
		info, _ := item.(map[string]interface{})
		if info == nil {
			continue
		}
		targetType, _ := info["type"].(string)
		targetID, _ := info["targetId"].(string)
		targetURL, _ := info["url"].(string)
		if targetID == "" || !isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
			continue
		}
		if targetType != "page" && targetType != "popup" {
			continue
		}
		_ = c.sendCommandNoWait("Target.closeTarget", map[string]interface{}{"targetId": targetID})
	}
}

func (c *cdpClient) sendCommandNoWait(method string, params map[string]interface{}) error {
	_, err := c.sendCommand(method, params, false)
	return err
}

func (c *cdpClient) sendSessionCommandNoWait(sessionID string, method string, params map[string]interface{}) error {
	_, err := c.sendCommandWithSession(method, params, sessionID, false)
	return err
}

func (c *cdpClient) sendCommand(method string, params map[string]interface{}, wait bool) (map[string]interface{}, error) {
	return c.sendCommandWithSession(method, params, "", wait)
}

func (c *cdpClient) sendCommandWithSession(method string, params map[string]interface{}, sessionID string, wait bool) (map[string]interface{}, error) {
	c.mu.Lock()
	if c.conn == nil || !c.connected {
		c.mu.Unlock()
		return nil, fmt.Errorf("CDP websocket is not connected")
	}

	c.msgID++
	id := c.msgID
	var ch chan map[string]interface{}
	if wait {
		ch = make(chan map[string]interface{}, 1)
		c.pendingRequests[id] = ch
	}
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

	payload, err := json.Marshal(cmd)
	if err != nil {
		if wait {
			c.removePending(id)
		}
		return nil, err
	}

	if err := c.writeFrame(0x1, payload); err != nil {
		if wait {
			c.removePending(id)
		}
		return nil, err
	}

	if !wait {
		return nil, nil
	}

	select {
	case response, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("CDP connection closed")
		}
		if errPayload, exists := response["error"]; exists {
			return nil, fmt.Errorf("CDP command %s failed: %v", method, errPayload)
		}
		return response, nil
	case <-time.After(2 * time.Second):
		c.removePending(id)
		return nil, fmt.Errorf("CDP command %s timed out", method)
	}
}

func (c *cdpClient) removePending(id int) {
	c.mu.Lock()
	delete(c.pendingRequests, id)
	c.mu.Unlock()
}

func (c *cdpClient) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return io.ErrClosedPipe
	}

	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xFFFF:
		header = append(header, 0x80|126)
		tmp := make([]byte, 2)
		binary.BigEndian.PutUint16(tmp, uint16(length))
		header = append(header, tmp...)
	default:
		header = append(header, 0x80|127)
		tmp := make([]byte, 8)
		binary.BigEndian.PutUint64(tmp, uint64(length))
		header = append(header, tmp...)
	}

	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	header = append(header, maskKey[:]...)

	maskedPayload := make([]byte, len(payload))
	for i := range payload {
		maskedPayload[i] = payload[i] ^ maskKey[i%4]
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(maskedPayload)
	return err
}

func (c *cdpClient) createTarget(targetURL string) (string, error) {
	if targetURL == "" {
		targetURL = "about:blank"
	}

	response, err := c.sendCommand("Target.createTarget", map[string]interface{}{"url": targetURL}, true)
	if err != nil {
		return "", err
	}

	result, _ := response["result"].(map[string]interface{})
	targetID, _ := result["targetId"].(string)
	if targetID == "" {
		return "", fmt.Errorf("Target.createTarget returned no targetId")
	}
	return targetID, nil
}

func (c *cdpClient) activateTarget(targetID string) error {
	if targetID == "" {
		return nil
	}

	c.mu.Lock()
	sessionID := c.targetSessions[targetID]
	if sessionID != "" {
		c.activeSessionID = sessionID
	}
	c.mu.Unlock()

	return c.sendCommandNoWait("Target.activateTarget", map[string]interface{}{"targetId": targetID})
}

func (c *cdpClient) getActiveURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeSessionID == "" {
		return ""
	}
	return c.sessionURLs[c.activeSessionID]
}

func (c *cdpClient) getTargetIDBySessionID(sessionID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionTargets[sessionID]
}

func (c *cdpClient) getSessionIDByTargetID(targetID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.targetSessions[targetID]
}

// getURLForTarget 根据 targetID 获取对应的 URL（用于等待有效 URL）
func (c *cdpClient) getURLForTarget(targetID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	sessionID := c.targetSessions[targetID]
	if sessionID == "" {
		return ""
	}
	return c.sessionURLs[sessionID]
}

// setSyncActionInProgress 标记指定 session 正在执行同步操作，防止回环
func (c *cdpClient) setSyncActionInProgress(sessionID string) {
	c.syncActionInProgress.Store(sessionID, true)
}

// clearSyncActionInProgress 清除指定 session 的同步操作标记
func (c *cdpClient) clearSyncActionInProgress(sessionID string) {
	c.syncActionInProgress.Delete(sessionID)
}

// isSyncActionInProgress 检查指定 session 是否正在执行同步操作
func (c *cdpClient) isSyncActionInProgress(sessionID string) bool {
	val, _ := c.syncActionInProgress.Load(sessionID)
	return val == true
}

func (c *cdpClient) closeTarget(targetID string) error {
	if targetID == "" {
		return nil
	}
	return c.sendCommandNoWait("Target.closeTarget", map[string]interface{}{"targetId": targetID})
}

func (c *cdpClient) getTargetIndex(targetID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	sessionID := c.targetSessions[targetID]
	if sessionID == "" {
		return -1
	}
	for index, current := range c.sessionOrder {
		if current == sessionID {
			return index
		}
	}
	return -1
}

func (c *cdpClient) getTargetIDAtIndex(index int) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if index < 0 || index >= len(c.sessionOrder) {
		return ""
	}
	return c.sessionTargets[c.sessionOrder[index]]
}

// dispatchKeyEvent sends Input.dispatchKeyEvent to the active tab session.
// eventType is "keyDown", "keyUp", or "rawKeyDown".
// vkCode is the Windows virtual key code. modifiers uses CDP bits: Alt=1 Ctrl=2 Shift=8.
func (c *cdpClient) dispatchKeyEvent(eventType string, vkCode uint32, modifiers int, text string) error {
	sessionID := c.findBestActiveSessionID()
	if sessionID == "" {
		return nil
	}

	return c.dispatchKeyEventToSession(sessionID, eventType, vkCode, modifiers, text)
}

func (c *cdpClient) dispatchKeyEventToSession(sessionID string, eventType string, vkCode uint32, modifiers int, text string) error {
	if sessionID == "" {
		return nil
	}

	key, code := vkCodeToDOMKey(vkCode)
	params := map[string]interface{}{
		"type":                  eventType,
		"modifiers":             modifiers,
		"windowsVirtualKeyCode": int(vkCode),
		"nativeVirtualKeyCode":  int(vkCode),
		"key":                   key,
		"code":                  code,
	}
	if text != "" {
		params["text"] = text
		params["unmodifiedText"] = text
	}

	// Map Ctrl shortcuts to editor commands so Chrome executes them even when
	// the renderer doesn't hold OS keyboard focus.
	if modifiers&2 != 0 { // Ctrl held
		var commands []string
		switch strings.ToLower(key) {
		case "c":
			commands = append(commands, "copy")
		case "v":
			commands = append(commands, "paste")
		case "x":
			commands = append(commands, "cut")
		case "a":
			commands = append(commands, "selectAll")
		case "z":
			if modifiers&8 != 0 {
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

	return c.sendSessionCommandNoWait(sessionID, "Input.dispatchKeyEvent", params)
}

func (c *cdpClient) dispatchMouseEventToSession(sessionID string, eventType string, x, y float64, button string, modifiers int, deltaY float64) error {
	if sessionID == "" {
		return fmt.Errorf("no session")
	}

	params := map[string]interface{}{
		"type":      eventType,
		"x":         x,
		"y":         y,
		"modifiers": modifiers,
	}

	if eventType == "mouseWheel" {
		params["deltaX"] = 0
		params["deltaY"] = deltaY
	} else {
		params["button"] = button
		// Set buttons mask: Left=1, Right=2, Middle=4.
		// For mouseReleased and mouseMoved (unless dragging), it's typically 0,
		// but for mousePressed, it should reflect the button being pressed.
		if eventType == "mousePressed" {
			switch button {
			case "left":
				params["buttons"] = 1
			case "right":
				params["buttons"] = 2
			case "middle":
				params["buttons"] = 4
			}
		} else {
			params["buttons"] = 0
		}

		if eventType == "mousePressed" || eventType == "mouseReleased" {
			params["clickCount"] = 1
		}
	}

	resp, err := c.sendCommandWithSession("Input.dispatchMouseEvent", params, sessionID, false)
	if err != nil {
		fmt.Printf("[CDP ERROR] dispatchMouseEvent failed: %v\n", err)
		return err
	}
	if resp != nil {
		cdpVerbosef("[CDP] dispatchMouseEvent response: %v\n", resp)
	}
	return nil
}

func (c *cdpClient) bringActivePageToFront() {
	sessionID := c.findPageSessionForActivation()
	if sessionID == "" {
		return
	}
	_, _ = c.sendCommandWithSession("Page.bringToFront", nil, sessionID, true)
}

func (c *cdpClient) findPageSessionForActivation() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.activeSessionID != "" {
		sessionID := c.activeSessionID
		targetURL := c.sessionURLs[sessionID]
		if c.sessionTypes[sessionID] == "page" && !isExtensionURL(targetURL) && !isIgnoredCDPTargetURL(targetURL) {
			return sessionID
		}
	}

	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		targetURL := c.sessionURLs[sessionID]
		if c.sessionTypes[sessionID] == "page" && !isExtensionURL(targetURL) && !isIgnoredCDPTargetURL(targetURL) {
			return sessionID
		}
	}

	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		if c.sessionTypes[sessionID] == "page" && !isExtensionURL(c.sessionURLs[sessionID]) {
			return sessionID
		}
	}
	return ""
}

func (c *cdpClient) newestVisibleExtensionURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.sessionOrder) - 1; i >= 0; i-- {
		sessionID := c.sessionOrder[i]
		targetURL := c.sessionURLs[sessionID]
		if isVisibleExtensionTarget(c.sessionTypes[sessionID], targetURL) {
			return targetURL
		}
	}
	return ""
}

// vkCodeToDOMKey maps a Windows VK code to CDP DOM Level 3 key and code strings.
func vkCodeToDOMKey(vk uint32) (key, code string) {
	switch vk {
	case 0x08:
		return "Backspace", "Backspace"
	case 0x09:
		return "Tab", "Tab"
	case 0x0D:
		return "Enter", "Enter"
	case 0x10:
		return "Shift", "ShiftLeft"
	case 0x11:
		return "Control", "ControlLeft"
	case 0x12:
		return "Alt", "AltLeft"
	case 0x1B:
		return "Escape", "Escape"
	case 0x20:
		return " ", "Space"
	case 0x25:
		return "ArrowLeft", "ArrowLeft"
	case 0x26:
		return "ArrowUp", "ArrowUp"
	case 0x27:
		return "ArrowRight", "ArrowRight"
	case 0x28:
		return "ArrowDown", "ArrowDown"
	case 0x2E:
		return "Delete", "Delete"
	case 0xBB:
		return "=", "Equal"
	case 0xBD:
		return "-", "Minus"
	case 0x60:
		return "0", "Numpad0"
	case 0x6B:
		return "+", "NumpadAdd"
	case 0x6D:
		return "-", "NumpadSubtract"
	case 0xA0:
		return "Shift", "ShiftLeft"
	case 0xA1:
		return "Shift", "ShiftRight"
	case 0xA2:
		return "Control", "ControlLeft"
	case 0xA3:
		return "Control", "ControlRight"
	}
	// 0-9
	if vk >= 0x30 && vk <= 0x39 {
		ch := string(rune(vk))
		return ch, "Digit" + ch
	}
	// A-Z
	if vk >= 0x41 && vk <= 0x5A {
		lower := string(rune(vk + 32))
		upper := string(rune(vk))
		return lower, "Key" + upper
	}
	return "Unidentified", "Unidentified"
}

func (c *cdpClient) GetUIOffset() float64 {
	sessionID := c.findBestActiveSessionID()
	if sessionID == "" {
		return 80.0
	}
	res, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression":    "window.outerHeight - window.innerHeight",
		"returnByValue": true,
	}, sessionID, true)
	if err == nil {
		if result, ok := res["result"].(map[string]interface{}); ok {
			if val, ok := result["value"].(float64); ok {
				return val
			}
		}
	}
	return 80.0
}

func (c *cdpClient) GetViewportMetrics() (float64, float64) {
	sessionID := c.findBestActiveSessionID()
	if sessionID == "" {
		return 0, 0
	}
	res, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression":    "[window.innerWidth, window.innerHeight]",
		"returnByValue": true,
	}, sessionID, true)
	if err == nil {
		if result, ok := res["result"].(map[string]interface{}); ok {
			if val, ok := result["value"].([]interface{}); ok && len(val) == 2 {
				w, _ := val[0].(float64)
				h, _ := val[1].(float64)
				return w, h
			}
		}
	}
	return 0, 0
}

func (c *cdpClient) findBestActiveSessionID() string {
	c.mu.Lock()
	sessionID := c.activeSessionID
	sessionOrder := append([]string(nil), c.sessionOrder...)
	c.mu.Unlock()

	if sessionID != "" {
		return sessionID
	}

	if len(sessionOrder) == 0 {
		return ""
	}

	// 1. 尝试从本地 /json 端点获取当前真正活跃的 Tab ID
	client := http.Client{Timeout: 200 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json", c.debugPort))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var targets []map[string]interface{}
			if err := json.NewDecoder(resp.Body).Decode(&targets); err == nil {
				// 优先在列表中寻找 type == "page" 的 target，
				// 第一个 type == "page" 且 URL 不是空白页的，通常就是用户当前看见的 tab。
				for _, t := range targets {
					tType, _ := t["type"].(string)
					tID, _ := t["id"].(string)
					tURL, _ := t["url"].(string)
					if tType == "page" && tID != "" && !isIgnoredCDPTargetURL(tURL) && !isExtensionURL(tURL) {
						// 寻找该 target 对应的 sessionID
						c.mu.Lock()
						sID := c.targetSessions[tID]
						if sID != "" {
							c.activeSessionID = sID // 缓存活跃 session，减少高频 HTTP 查询。
						}
						c.mu.Unlock()
						if sID != "" {
							return sID
						}
					}
				}
			}
		}
	}

	// 2. 如果 /json 读取失败，使用 sessionOrder 中第一个不是空白页的 session
	c.mu.Lock()
	var bestSID string
	for _, sID := range c.sessionOrder {
		u := c.sessionURLs[sID]
		t := c.sessionTypes[sID]
		if t == "page" && u != "" && u != "about:blank" && !isIgnoredCDPTargetURL(u) && !isExtensionURL(u) {
			bestSID = sID
			c.activeSessionID = sID // 缓存活跃 session。
			break
		}
	}
	c.mu.Unlock()

	if bestSID != "" {
		return bestSID
	}

	// 3. 实在没有，兜底使用第一个并缓存
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sID := range c.sessionOrder {
		if c.sessionTypes[sID] == "page" && !isExtensionURL(c.sessionURLs[sID]) {
			c.activeSessionID = sID
			return sID
		}
	}
	if len(c.sessionOrder) > 0 {
		c.activeSessionID = c.sessionOrder[0]
		return c.sessionOrder[0]
	}

	return ""
}

// executeDomActionWithSession executes a captured DOM action in the specified session.
func (c *cdpClient) executeDomActionWithSession(sessionID string, actionJSON string) {
	if sessionID == "" {
		sessionID = c.findBestActiveSessionID()
	}
	if sessionID == "" {
		fmt.Printf("[DOM ACTION EXECUTION ERROR] No active session found for executeDomAction\n")
		return
	}
	cdpVerbosef("[DOM ACTION EXECUTE] Executing action on session %s: %s\n", sessionID, actionJSON)

	c.setSyncActionInProgress(sessionID)
	defer func(sid string) {
		time.AfterFunc(350*time.Millisecond, func() {
			c.clearSyncActionInProgress(sid)
		})
	}(sessionID)

	// 防回环：设置同步操作标志，防止派发的事件被重新捕获并广播回去
	// 注意：必须先同步等待标志设置完成，然后再发送 CDP click，否则存在竞态条件
	// （CDP click 可能在标志设置前就触发了 mouse 事件）
	_, _ = c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression": "window.__chromeDuoSyncActionInProgress = true",
	}, sessionID, true)

	// 解析 action，对 click 类型优先使用 CDP Input（产生浏览器级别的真实事件）
	var action map[string]interface{}
	if err := json.Unmarshal([]byte(actionJSON), &action); err == nil {
		actionType, _ := action["type"].(string)
		if actionType == "click" {
			vpX, xOK := action["vpX"].(float64)
			vpY, yOK := action["vpY"].(float64)
			if xOK && yOK && (vpX > 0 || vpY > 0) {
				if err := c.cdpDispatchClick(sessionID, vpX, vpY); err == nil {
					// CDP Input 完成后延迟清除标志
					go func() {
						time.Sleep(100 * time.Millisecond)
						_ = c.sendSessionCommandNoWait(sessionID, "Runtime.evaluate", map[string]interface{}{
							"expression": "window.__chromeDuoSyncActionInProgress = false",
						})
					}()
					return
				}
				cdpVerbosef("[DOM ACTION EXECUTE] CDP Input click failed, falling back to JS dispatch\n")
			}
		}
	}

	// 兜底：对 input/scroll 类型 或 CDP Input 失败时，使用 JavaScript 派发
	const execScript = `((actionStr) => {
  (async () => {
    try {
      const action = JSON.parse(actionStr);
      
      const sleep = ms => new Promise(r => setTimeout(r, ms));
      
      function querySelectorShadow(selector) {
        const parts = selector.split(' >>> ');
        let current = document;
        for (let i = 0; i < parts.length; i++) {
          if (!current) return null;
          const part = parts[i].trim();
          if (i === parts.length - 1) {
            return current.querySelector(part);
          }
          const host = current.querySelector(part);
          if (!host || !host.shadowRoot) return null;
          current = host.shadowRoot;
        }
        return null;
      }

      async function querySelectorShadowWithRetry(selector, timeout = 1500, interval = 25) {
        const start = Date.now();
        while (Date.now() - start < timeout) {
          const el = querySelectorShadow(selector);
          if (el) return el;
          await sleep(interval);
        }
        return null;
      }

      if (action.type === 'click') {
        let el = await querySelectorShadowWithRetry(action.selector);
        // 坐标兜底：selector 匹配失败时，使用视口坐标定位元素
        if (!el && action.vpX !== undefined && action.vpY !== undefined) {
          const x = action.vpX * window.innerWidth;
          const y = action.vpY * window.innerHeight;
          el = document.elementFromPoint(x, y);
        }
        if (el) {
          const tagName = el.tagName ? el.tagName.toUpperCase() : '';
          const needsFocus = tagName === 'INPUT' || tagName === 'TEXTAREA' || tagName === 'SELECT' || el.isContentEditable;
          if (needsFocus) {
            el.focus();
          }
          // 派发完整 PointerEvent + MouseEvent 事件流
          if (typeof PointerEvent !== 'undefined') {
            el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, cancelable: true, view: window, composed: true, button: 0, buttons: 1, pointerType: 'mouse', isPrimary: true }));
          }
          el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, view: window, composed: true, button: 0, buttons: 1 }));
          
          if (typeof PointerEvent !== 'undefined') {
            el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, cancelable: true, view: window, composed: true, button: 0, buttons: 0, pointerType: 'mouse', isPrimary: true }));
          }
          el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true, view: window, composed: true, button: 0, buttons: 0 }));
          el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: window, composed: true, button: 0, buttons: 0 }));
        }
      } else if (action.type === 'input') {
        const el = await querySelectorShadowWithRetry(action.selector);
        if (el) {
          el.focus();
          if (el.isContentEditable) {
            el.innerHTML = action.value;
          } else {
            el.value = action.value;
          }
          // Dispatch essential web events for modern Vue/React frameworks to update states
          el.dispatchEvent(new Event('input', { bubbles: true }));
          el.dispatchEvent(new Event('change', { bubbles: true }));
        }
      } else if (action.type === 'scroll') {
        const docEl = document.documentElement;
        const maxScrollY = docEl.scrollHeight - window.innerHeight;
        const targetY = maxScrollY > 0 ? action.percentY * maxScrollY : 0;

        const maxScrollX = docEl.scrollWidth - window.innerWidth;
        const targetX = maxScrollX > 0 ? action.percentX * maxScrollX : 0;

        window.scrollTo({
          left: targetX,
          top: targetY,
          behavior: 'auto'
        });
      }
    } catch(e) {}
  })();
})`

	_ = c.sendSessionCommandNoWait(sessionID, "Runtime.evaluate", map[string]interface{}{
		"expression": fmt.Sprintf("(%s)(%q)", execScript, actionJSON),
	})

	// 延迟清除防回环标志（给事件派发和捕获留出时间窗口）
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = c.sendSessionCommandNoWait(sessionID, "Runtime.evaluate", map[string]interface{}{
			"expression": "window.__chromeDuoSyncActionInProgress = false",
		})
	}()
}

// cdpDispatchClick 使用 CDP Input.dispatchMouseEvent 在从端执行点击。
// 这产生浏览器级别的真实输入事件（isTrusted=true），能触发所有原生行为：
// - <select> 下拉框打开
// - <a> 链接导航
// - chrome:// 页面的 Custom Elements (cr-toggle 等)
// - 任何检查 e.isTrusted 的框架/库
func (c *cdpClient) cdpDispatchClick(sessionID string, vpX, vpY float64) error {
	// 1. 获取从端视口尺寸
	resp, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression":    "JSON.stringify({w:window.innerWidth,h:window.innerHeight})",
		"returnByValue": true,
	}, sessionID, true)
	if err != nil {
		return fmt.Errorf("viewport query failed: %w", err)
	}

	result, _ := resp["result"].(map[string]interface{})
	inner, _ := result["result"].(map[string]interface{})
	valStr, _ := inner["value"].(string)

	var viewport struct {
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := json.Unmarshal([]byte(valStr), &viewport); err != nil || viewport.W <= 0 || viewport.H <= 0 {
		return fmt.Errorf("invalid viewport: %s", valStr)
	}

	// 2. 计算绝对坐标
	x := vpX * viewport.W
	y := vpY * viewport.H

	// 3. 派发 mousePressed + mouseReleased（等同于真实鼠标点击）
	_, err = c.sendCommandWithSession("Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mousePressed",
		"x":          x,
		"y":          y,
		"button":     "left",
		"clickCount": 1,
	}, sessionID, true)
	if err != nil {
		return fmt.Errorf("mousePressed failed: %w", err)
	}

	_, err = c.sendCommandWithSession("Input.dispatchMouseEvent", map[string]interface{}{
		"type":       "mouseReleased",
		"x":          x,
		"y":          y,
		"button":     "left",
		"clickCount": 1,
	}, sessionID, true)
	if err != nil {
		return fmt.Errorf("mouseReleased failed: %w", err)
	}

	return nil
}

func (c *cdpClient) SetPageZoom(zoom float64) (string, error) {
	if zoom < 0.25 {
		zoom = 0.25
	} else if zoom > 5.0 {
		zoom = 5.0
	}

	sessionID := c.findBestActiveSessionID()
	if sessionID == "" {
		return "", fmt.Errorf("no active session")
	}

	c.mu.Lock()
	c.currentZoom = zoom
	c.currentStyleZoom = zoom
	pageZoomUnsupported := c.pageZoomUnsupported
	c.mu.Unlock()

	mode := "cdp-page-zoom"
	if !pageZoomUnsupported {
		if _, err := c.sendCommandWithSession("Page.setZoomFactor", map[string]interface{}{
			"zoomFactor": zoom,
		}, sessionID, true); err != nil {
			if !isCDPMethodNotFound(err) {
				c.mu.Lock()
				sessionURL := c.sessionURLs[sessionID]
				sessionType := c.sessionTypes[sessionID]
				c.mu.Unlock()
				return "", fmt.Errorf("Page.setZoomFactor failed session=%s type=%s url=%s zoom=%.2f: %w", sessionID, sessionType, sessionURL, zoom, err)
			}
			c.mu.Lock()
			c.pageZoomUnsupported = true
			c.mu.Unlock()
			pageZoomUnsupported = true
		}
	}

	if pageZoomUnsupported {
		mode = "cdp-emulation-scale"
		method := "Emulation.setPageScaleFactor"
		params := map[string]interface{}{"pageScaleFactor": zoom}
		if zoom == 1.0 {
			method = "Emulation.resetPageScaleFactor"
			params = nil
		}
		if _, err := c.sendCommandWithSession(method, params, sessionID, true); err != nil {
			return "", fmt.Errorf("%s failed zoom=%.2f: %w", method, zoom, err)
		}
	}

	if _, err := c.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
		"expression":    "document.documentElement.style.zoom = ''; if (document.body) document.body.style.zoom = ''; undefined",
		"returnByValue": true,
	}, sessionID, true); err != nil {
		fmt.Printf("[SyncZoom] clear legacy CSS zoom failed session=%s zoom=%.2f: %v\n", sessionID, zoom, err)
	}
	return mode, nil
}

func isCDPMethodNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "-32601") || strings.Contains(msg, "wasn't found") || strings.Contains(msg, "Method not found")
}

func (c *cdpClient) GetCurrentStyleZoom() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.currentStyleZoom <= 0 {
		return 1.0
	}
	return c.currentStyleZoom
}

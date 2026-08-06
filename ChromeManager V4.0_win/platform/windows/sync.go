//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"chromemanager/utils"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SyncManager implements common.SyncProvider for Windows.
type SyncManager struct {
	mu sync.RWMutex

	// State
	isRunning bool
	isPaused  bool
	config    common.SyncConfig

	// 防重入：追踪最近处理的 URL 导航，避免重复触发
	recentNavigations     map[string]time.Time // "sessionID:url" -> timestamp
	navigationDedupeMu    sync.Mutex
	extensionPopupMu      sync.Mutex
	recentExtensionPopups map[string]time.Time
	lastExtensionPopupAt  time.Time

	// Native dialog blocking: 阻止 DOM action 回调执行
	domActionsBlocked bool

	// Window Handles
	masterWindow common.WindowHandle
	masterPID    uint32
	slaveWindows []common.WindowHandle

	// Hooks
	keyboardHookID    uintptr
	mouseHookID       uintptr
	keyboardHookProc  uintptr
	mouseHookProc     uintptr
	stopChan          chan struct{}
	messageLoopActive bool
	loopThreadID      uint32
	hookEventCh       chan hookEvent // async dispatch channel; hook callbacks write here

	// Optimization & Caching

	lastWheelTime time.Time
	popupCache    map[uintptr][]uintptr // Cache for expensive checks

	popupCacheMu         sync.RWMutex
	popupRefreshMu       sync.Mutex
	popupRefreshing      atomic.Bool // true while a refresh is in progress; used for tryLock
	relatedWindowCache   map[uintptr]relatedWindowCacheEntry
	relatedWindowCacheMu sync.RWMutex
	popupSessionMu       sync.RWMutex
	popupSessionUntil    time.Time
	lastPopupRefresh     time.Time
	popupRefreshRequest  chan struct{}
	clickRouteMu         sync.Mutex
	leftClickRoute       clickRouteState
	rightClickRoute      clickRouteState
	extensionTriggerMu   sync.Mutex
	lastExtensionTrigger clickRouteState
	lastExtensionClickAt time.Time

	// Native Dialog Detection (native 对话框检测，用于在 Chrome 弹出 native 对话框时暂停 DOM Action)
	nativeDialogEnumCB uintptr

	// Optimized intervals (from config)
	optimizedKeyboardInterval time.Duration
	optimizedMouseInterval    time.Duration
	optimizedWheelInterval    time.Duration

	// Windows API
	user32                   *windows.LazyDLL
	setWindowsHookEx         *windows.LazyProc
	unhookWindowsHookEx      *windows.LazyProc
	callNextHookEx           *windows.LazyProc
	getAsyncKeyState         *windows.LazyProc
	postMessage              *windows.LazyProc
	setWindowPos             *windows.LazyProc
	getClientRect            *windows.LazyProc
	clientToScreen           *windows.LazyProc
	getWindowRect            *windows.LazyProc
	getForegroundWindow      *windows.LazyProc
	setForegroundWindow      *windows.LazyProc
	enumChildWindows         *windows.LazyProc
	enumWindows              *windows.LazyProc
	getWindowTextW           *windows.LazyProc
	getWindowThreadProcessId *windows.LazyProc
	isWindowVisible          *windows.LazyProc
	getWindowLong            *windows.LazyProc
	screenToClient           *windows.LazyProc
	windowFromPoint          *windows.LazyProc
	getAncestor              *windows.LazyProc
	getDpiForWindow          *windows.LazyProc
	monitorFromWindow        *windows.LazyProc
	getDpiForMonitor         *windows.LazyProc
	getCursorPos             *windows.LazyProc

	tabManager       *utils.TabManager
	windowMetadataMu sync.RWMutex
	masterMetadata   *common.WindowInfo
	slaveMetadata    map[uintptr]common.WindowInfo
	viewportMetrics  map[uintptr]ViewportMetrics
	popupArrangeSet  []uintptr

	cdpMu     sync.Mutex
	masterCDP *cdpClient
	slaveCDPs map[uintptr]*cdpClient

	uiOffsetMu sync.RWMutex
	uiOffsets  map[uintptr]float64

	targetMap               map[string]map[uintptr]string
	externallyMappedTargets map[string]bool
	cdpStarting             bool
	cdpGeneration           uint64

	lastClickYMu sync.RWMutex
	lastClickY   int32

	// Action deduplication: prevent echo loops
	actionDedupMu sync.RWMutex
	recentActions map[string]time.Time // key: actionHash, value: last execution time

	domClickMu       sync.Mutex
	lastDomClickTime time.Time

	// Zoom sync state
	syncZoomIndex  int
	syncZoomSeq    int64
	syncZoomTarget float64
	syncZoomActive bool

	// Modifier key states (atomic)
	ctrlPressed  atomic.Bool
	shiftPressed atomic.Bool
	altPressed   atomic.Bool
}

// Action deduplication: prevent echo loops by tracking recently executed actions.
const actionDedupWindow = 300 * time.Millisecond

// isDuplicateAction checks if an action was recently executed and marks it if not.
// Returns true if this is a duplicate (should be skipped).
func (sm *SyncManager) isDuplicateAction(actionJSON string, sessionID string) bool {
	// Create a hash from action content + session ID
	hash := fmt.Sprintf("%s:%s", sessionID, actionJSON)

	sm.actionDedupMu.Lock()
	defer sm.actionDedupMu.Unlock()

	// Initialize map if needed
	if sm.recentActions == nil {
		sm.recentActions = make(map[string]time.Time)
	}

	// Check if this action was recently executed
	if lastTime, exists := sm.recentActions[hash]; exists {
		if time.Since(lastTime) < actionDedupWindow {
			// Duplicate detected
			return true
		}
	}

	// Mark this action as executed
	sm.recentActions[hash] = time.Now()

	// Clean up old entries periodically
	if len(sm.recentActions) > 100 {
		now := time.Now()
		for k, v := range sm.recentActions {
			if now.Sub(v) > actionDedupWindow*2 {
				delete(sm.recentActions, k)
			}
		}
	}

	return false
}

func isClickDomAction(actionJSON string) bool {
	return domActionType(actionJSON) == "click"
}

func domActionType(actionJSON string) string {
	var action struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(actionJSON), &action); err != nil {
		return ""
	}
	return action.Type
}

func (sm *SyncManager) markDomClickHandled() {
	sm.domClickMu.Lock()
	sm.lastDomClickTime = time.Now()
	sm.domClickMu.Unlock()
}

func (sm *SyncManager) hasRecentDomClickHandledSince(since time.Time) bool {
	sm.domClickMu.Lock()
	defer sm.domClickMu.Unlock()
	if sm.lastDomClickTime.IsZero() || time.Since(sm.lastDomClickTime) > privilegedDomClickSuppressWindow {
		return false
	}
	return !sm.lastDomClickTime.Before(since.Add(-privilegedDomClickFallbackDelay))
}

func (sm *SyncManager) shouldSuppressPageClickAfterExtensionPopup(masterURL string, actionJSON string) bool {
	if !isClickDomAction(actionJSON) || isExtensionURL(masterURL) {
		return false
	}

	sm.extensionPopupMu.Lock()
	lastPopupAt := sm.lastExtensionPopupAt
	sm.extensionPopupMu.Unlock()

	if !lastPopupAt.IsZero() && time.Since(lastPopupAt) <= extensionPopupPageClickSuppressWindow {
		return true
	}

	sm.cdpMu.Lock()
	masterCDP := sm.masterCDP
	sm.cdpMu.Unlock()
	return masterCDP != nil && masterCDP.hasExtensionPopupSession()
}

func (sm *SyncManager) browserInitialNewTabURL() string {
	sm.windowMetadataMu.RLock()
	defer sm.windowMetadataMu.RUnlock()

	if sm.masterMetadata == nil {
		return "chrome://newtab/"
	}

	haystack := strings.ToLower(sm.masterMetadata.CommandLine + " " + sm.masterMetadata.UserDataDir + " " + sm.masterMetadata.Title)
	switch {
	case strings.Contains(haystack, "msedge.exe") || strings.Contains(haystack, "\\edge") || strings.Contains(haystack, "/edge"):
		return "edge://newtab/"
	case strings.Contains(haystack, "opera.exe") || strings.Contains(haystack, "launcher.exe") && strings.Contains(haystack, "opera"):
		return "opera://startpage/"
	case strings.Contains(haystack, "brave.exe") || strings.Contains(haystack, "\\brave") || strings.Contains(haystack, "/brave"):
		return "brave://newtab/"
	default:
		return "chrome://newtab/"
	}
}

// Ensure SyncManager implements common.SyncProvider
var _ common.SyncProvider = (*SyncManager)(nil)

type relatedWindowCacheEntry struct {
	related   bool
	checkedAt time.Time
}

type clickRouteState struct {
	sourceWindow uintptr
	screenX      int32
	screenY      int32
	relX         float64
	relY         float64
	isPopup      bool
	reliable     bool
	synthetic    bool
	createdAt    time.Time
	tabAware     *tabAwareClickState

	popupBaselineKnown       bool
	popupBaselineMasterCount int
	popupBaselineCounts      map[uintptr]int
}

type tabAwareClickState struct {
}

const defaultHookEventBufferSize = 5000
const privilegedDomClickFallbackDelay = 180 * time.Millisecond
const privilegedDomClickSuppressWindow = 350 * time.Millisecond
const extensionPopupPageClickSuppressWindow = 2500 * time.Millisecond

// hookEventKind distinguishes keyboard from mouse low-level hook events.
type hookEventKind uint8

const (
	hookKindKeyboard hookEventKind = iota
	hookKindMouse
)

// hookEvent is a lightweight copy of the hook data passed to the dispatcher goroutine.
// Keeping this small ensures the hook callback stays fast.
type hookEvent struct {
	kind         hookEventKind
	wParam       int
	kb           KBDLLHOOKSTRUCT // valid when kind == hookKindKeyboard
	ms           MSLLHOOKSTRUCT  // valid when kind == hookKindMouse
	activeWindow uintptr         // source window captured at hook time
}

const (
	WH_KEYBOARD_LL = 13
	WH_MOUSE_LL    = 14

	WM_KEYDOWN     = 0x0100
	WM_KEYUP       = 0x0101
	WM_SYSKEYDOWN  = 0x0104
	WM_SYSKEYUP    = 0x0105
	WM_MOUSEMOVE   = 0x0200
	WM_LBUTTONDOWN = 0x0201
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONDOWN = 0x0204
	WM_RBUTTONUP   = 0x0205
	WM_MOUSEWHEEL  = 0x020A
	GA_ROOT        = 2

	VK_SHIFT    = 0x10
	VK_CONTROL  = 0x11
	VK_MENU     = 0x12
	VK_LWIN     = 0x5B
	VK_RWIN     = 0x5C
	VK_LSHIFT   = 0xA0
	VK_RSHIFT   = 0xA1
	VK_LCONTROL = 0xA2
	VK_RCONTROL = 0xA3
	VK_LMENU    = 0xA4
	VK_RMENU    = 0xA5
	MK_LBUTTON  = 0x0001
	MK_RBUTTON  = 0x0002
	MK_SHIFT    = 0x0004
	MK_CONTROL  = 0x0008

	VK_A = 0x41
	VK_C = 0x43
	VK_V = 0x56
	VK_X = 0x58
	VK_Z = 0x5A
)

const (
	popupBaseRefreshInterval   = 2500 * time.Millisecond
	popupActiveRefreshInterval = 75 * time.Millisecond
	popupSessionDuration       = 850 * time.Millisecond
	popupSessionExtension      = 450 * time.Millisecond
	popupToolbarProbeHeight    = 140
	popupUpwardDetectMargin    = 12
	popupSideShift             = 36
	popupSideShiftStep         = 12
	popupColumnGroupingGap     = 48
	relatedPositiveCacheTTL    = 5 * time.Second
	relatedNegativeCacheTTL    = 2 * time.Second
	popupReliableConfirmDelay  = 85 * time.Millisecond
	popupReliableConfirmStep   = 70 * time.Millisecond
	popupReliableRetryGap      = 12 * time.Millisecond
	popupReliableConfirmTries  = 8
	clickRouteTTL              = 900 * time.Millisecond
	cdpStartupTimeout          = 60 * time.Second
	cdpConnectRetryDelay       = 250 * time.Millisecond
	// Keep tab-aware interception limited to the tab strip. A broad toolbar
	// region can replay extension/menu clicks against the wrong slave chrome UI.
	tabAwareProbeHeight        = 48
	tabAwareRightControlMargin = 150
)

const (
	gwlStyleIndex         = ^uintptr(15) // -16
	gwlExStyleIndex       = ^uintptr(19) // -20
	wsChild               = uintptr(0x40000000)
	wsDlgFrame            = uintptr(0x00400000)
	wsExDlgModalFrame     = uintptr(0x00000001)
	dsModalFrame          = uintptr(0x00000080)
	nativeDialogMaxArea   = 500000
	nativeDialogMaxWidth  = 1000
	nativeDialogMaxHeight = 800
)

const defaultChromeZoomIndex = 7 // 100%

var chromeZoomSteps = []float64{0.25, 0.33, 0.50, 0.67, 0.75, 0.80, 0.90, 1.00, 1.10, 1.25, 1.50, 1.75, 2.00, 2.50, 3.00, 4.00, 5.00}

func NewSyncManager() *SyncManager {
	user32 := windows.NewLazyDLL("user32.dll")
	shcore := windows.NewLazyDLL("shcore.dll")
	return &SyncManager{
		syncZoomIndex:            defaultChromeZoomIndex,
		syncZoomTarget:           1.0,
		user32:                   user32,
		setWindowsHookEx:         user32.NewProc("SetWindowsHookExW"),
		unhookWindowsHookEx:      user32.NewProc("UnhookWindowsHookEx"),
		callNextHookEx:           user32.NewProc("CallNextHookEx"),
		getAsyncKeyState:         user32.NewProc("GetAsyncKeyState"),
		postMessage:              user32.NewProc("PostMessageW"),
		setWindowPos:             user32.NewProc("SetWindowPos"),
		getClientRect:            user32.NewProc("GetClientRect"),
		clientToScreen:           user32.NewProc("ClientToScreen"),
		getWindowRect:            user32.NewProc("GetWindowRect"),
		getForegroundWindow:      user32.NewProc("GetForegroundWindow"),
		setForegroundWindow:      user32.NewProc("SetForegroundWindow"),
		enumChildWindows:         user32.NewProc("EnumChildWindows"),
		screenToClient:           user32.NewProc("ScreenToClient"),
		windowFromPoint:          user32.NewProc("WindowFromPoint"),
		getAncestor:              user32.NewProc("GetAncestor"),
		getDpiForWindow:          user32.NewProc("GetDpiForWindow"),
		monitorFromWindow:        user32.NewProc("MonitorFromWindow"),
		getDpiForMonitor:         shcore.NewProc("GetDpiForMonitor"),
		getCursorPos:             user32.NewProc("GetCursorPos"),
		enumWindows:              user32.NewProc("EnumWindows"),
		getWindowTextW:           user32.NewProc("GetWindowTextW"),
		getWindowThreadProcessId: user32.NewProc("GetWindowThreadProcessId"),
		isWindowVisible:          user32.NewProc("IsWindowVisible"),
		getWindowLong:            user32.NewProc("GetWindowLongPtrW"),
		tabManager:               utils.NewTabManager(),
		popupCache:               make(map[uintptr][]uintptr),
		slaveMetadata:            make(map[uintptr]common.WindowInfo),
		viewportMetrics:          make(map[uintptr]ViewportMetrics),
		slaveCDPs:                make(map[uintptr]*cdpClient),
		uiOffsets:                make(map[uintptr]float64),
		targetMap:                make(map[string]map[uintptr]string),
		externallyMappedTargets:  make(map[string]bool),
		relatedWindowCache:       make(map[uintptr]relatedWindowCacheEntry),
		relatedWindowCacheMu:     sync.RWMutex{},
		popupRefreshRequest:      make(chan struct{}, 1),
		recentNavigations:        make(map[string]time.Time),
		recentExtensionPopups:    make(map[string]time.Time),
		config: common.SyncConfig{ // Default config
			MouseMoveInterval:   5 * time.Millisecond,
			KeyboardInterval:    3 * time.Millisecond,
			WheelEventThreshold: 5 * time.Millisecond,
		},
	}
}

// Start initiates synchronization.
func (sm *SyncManager) Start(masterWindow common.WindowHandle, slaveWindows []common.WindowHandle) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isRunning {
		return fmt.Errorf("sync is already running")
	}

	if masterWindow == 0 {
		return fmt.Errorf("invalid master window handle")
	}

	sm.masterWindow = masterWindow
	sm.masterPID = 0
	sm.slaveWindows = make([]common.WindowHandle, len(slaveWindows))
	copy(sm.slaveWindows, slaveWindows)

	if masterPID, err := utils.GetWindowProcessID(uintptr(masterWindow)); err == nil {
		sm.masterPID = masterPID
	}

	// Reset state
	sm.stopChan = make(chan struct{})
	// Buffered channel absorbs bursts of rapid clicks across many
	// slaves without blocking the hook callback. Events dropped when full
	// (system overloaded) are preferable to blocking.
	sm.hookEventCh = make(chan hookEvent, sm.hookEventBufferSize())
	sm.popupCache = make(map[uintptr][]uintptr)
	sm.relatedWindowCacheMu.Lock()
	sm.relatedWindowCache = make(map[uintptr]relatedWindowCacheEntry)
	sm.relatedWindowCacheMu.Unlock()
	sm.popupSessionMu.Lock()
	sm.popupSessionUntil = time.Time{}
	sm.lastPopupRefresh = time.Time{}
	sm.popupRefreshRequest = make(chan struct{}, 1)
	sm.popupSessionMu.Unlock()
	sm.popupArrangeSet = nil
	sm.clearClickRoutes()
	sm.domClickMu.Lock()
	sm.lastDomClickTime = time.Time{}
	sm.domClickMu.Unlock()
	sm.extensionPopupMu.Lock()
	sm.recentExtensionPopups = make(map[string]time.Time)
	sm.lastExtensionPopupAt = time.Time{}
	sm.extensionPopupMu.Unlock()
	if sm.nativeDialogEnumCB == 0 {
		sm.nativeDialogEnumCB = windows.NewCallback(nativeDialogEnumCallback)
	}

	// Ensure clean state
	sm.forceUnhookAll()

	sm.isRunning = true
	// Keep hooks installed but muted until CDP/extension-popup sync is ready.
	sm.isPaused = true

	sm.syncZoomIndex = defaultChromeZoomIndex
	sm.syncZoomSeq = 0
	sm.syncZoomTarget = chromeZoomSteps[defaultChromeZoomIndex]
	sm.syncZoomActive = false

	// Critical: Hooks must be installed and pumped on the SAME thread.
	// We launch a dedicated goroutine for this, locking it to an OS thread.
	errChan := make(chan error)
	go sm.runMessageLoop(errChan)

	// Unlock to allow runMessageLoop to initialize (avoid deadlock)
	sm.mu.Unlock()

	// Wait for installation result
	installErr := <-errChan

	// Re-acquire lock
	sm.mu.Lock()

	if installErr != nil {
		sm.isRunning = false
		return installErr
	}

	// Initialize cache
	go sm.initializePopupCache()

	// Start the async event dispatcher that processes hook events off the hook thread.
	go sm.eventDispatcher(sm.hookEventCh, sm.stopChan)

	// Start native dialog detection goroutine
	go sm.nativeDialogDetector()

	return nil
}

func (sm *SyncManager) StartCDPTabSyncAsync(delay time.Duration) {
	if delay <= 0 {
		sm.mu.RLock()
		isRunning := sm.isRunning
		sm.mu.RUnlock()
		if !isRunning {
			return
		}

		cdpGeneration := sm.markCDPTabSyncStarting()
		go sm.startCDPTabSync(cdpGeneration)
		return
	}

	go func() {
		time.Sleep(delay)

		sm.mu.RLock()
		isRunning := sm.isRunning
		sm.mu.RUnlock()
		if !isRunning {
			return
		}

		cdpGeneration := sm.markCDPTabSyncStarting()
		sm.startCDPTabSync(cdpGeneration)
	}()
}

func (sm *SyncManager) shouldContinueCDPStartup(generation uint64) bool {
	sm.mu.RLock()
	isRunning := sm.isRunning
	sm.mu.RUnlock()
	return isRunning && sm.isCurrentCDPTabSyncGeneration(generation)
}

func (sm *SyncManager) connectCDPWithRetry(cdp *cdpClient, generation uint64, deadline time.Time) error {
	var lastErr error
	for {
		if !sm.shouldContinueCDPStartup(generation) {
			return fmt.Errorf("sync startup was cancelled")
		}
		if err := cdp.connect(); err != nil {
			lastErr = err
			cdp.close()
		} else {
			return nil
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("CDP connect timed out")
		}
		time.Sleep(cdpConnectRetryDelay)
	}
}

func (sm *SyncManager) waitForCDPClientsReady(masterCDP *cdpClient, slaveCDPs map[uintptr]*cdpClient, generation uint64, deadline time.Time) bool {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		if !sm.shouldContinueCDPStartup(generation) {
			return false
		}

		ready := masterCDP != nil && masterCDP.isReady()
		if ready {
			for _, cdp := range slaveCDPs {
				if cdp == nil || !cdp.isReady() {
					ready = false
					break
				}
			}
		}
		if ready {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}

		<-ticker.C
	}
}

// Stop terminates synchronization.
func (sm *SyncManager) Stop() error {
	sm.popupRefreshMu.Lock()
	sm.resetArrangedPopups()
	sm.popupRefreshMu.Unlock()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return nil
	}

	sm.isRunning = false
	sm.isPaused = false
	sm.masterPID = 0

	if sm.stopChan != nil {
		close(sm.stopChan)
		sm.stopChan = nil
	}

	sm.popupSessionMu.Lock()
	sm.popupSessionUntil = time.Time{}
	sm.popupRefreshRequest = nil
	sm.popupSessionMu.Unlock()
	sm.navigationDedupeMu.Lock()
	sm.recentNavigations = make(map[string]time.Time)
	sm.navigationDedupeMu.Unlock()
	sm.extensionPopupMu.Lock()
	sm.recentExtensionPopups = make(map[string]time.Time)
	sm.lastExtensionPopupAt = time.Time{}
	sm.extensionPopupMu.Unlock()
	sm.domClickMu.Lock()
	sm.lastDomClickTime = time.Time{}
	sm.domClickMu.Unlock()
	sm.clearClickRoutes()
	sm.stopCDPTabSync()

	// Post WM_QUIT to break the message loop
	if sm.loopThreadID != 0 {
		user32 := windows.NewLazyDLL("user32.dll")
		postThreadMessage := user32.NewProc("PostThreadMessageW")
		// WM_QUIT = 0x0012
		postThreadMessage.Call(uintptr(sm.loopThreadID), 0x0012, 0, 0)
	}

	return nil
}

func (sm *SyncManager) startCDPTabSync(generation uint64) {
	defer sm.markCDPTabSyncStoppedStarting(generation)
	deadline := time.Now().Add(cdpStartupTimeout)

	sm.windowMetadataMu.RLock()
	var masterPort int
	if sm.masterMetadata != nil {
		masterPort = sm.masterMetadata.DebugPort
	}
	slaveInfos := make(map[uintptr]common.WindowInfo, len(sm.slaveMetadata))
	for handle, info := range sm.slaveMetadata {
		slaveInfos[handle] = info
	}
	sm.windowMetadataMu.RUnlock()

	if masterPort == 0 || len(slaveInfos) == 0 {
		return
	}

	masterCDP := newCDPClient(masterPort)
	slaveCDPs := make(map[uintptr]*cdpClient, len(slaveInfos))
	var slaveCDPMu sync.Mutex

	masterCDP.onTargetCreated = func(masterTargetID string, targetURL string) {
		sm.handleMasterTargetCreated(masterTargetID, targetURL)
	}
	masterCDP.onTargetDestroyed = func(masterTargetID string) {
		sm.handleMasterTargetDestroyed(masterTargetID)
	}
	masterCDP.onTabActivated = func(masterTargetID string, targetURL string) {
		sm.handleMasterTabActivated(masterTargetID, targetURL)
	}
	masterCDP.onURLChanged = func(sessionID string, targetURL string) {
		startTime := time.Now()
		timestamp := startTime.Format("15:04:05.000")
		cdpVerbosef("[SyncManager] onURLChanged: sessionID=%s url=%s timestamp=%s\n", sessionID, targetURL, timestamp)
		sm.mu.RLock()
		if sm.isPaused {
			sm.mu.RUnlock()
			return
		}
		sm.mu.RUnlock()

		if isIgnoredCDPTargetURL(targetURL) {
			return
		}

		// 1. 获取主端发生 URL 导航变更会话对应的 masterTargetID
		if extensionID, ok := extensionPopupID(targetURL); ok {
			sm.maybeOpenExtensionPopupOnSlaves(extensionID)
			return
		}
		if strings.HasPrefix(targetURL, "chrome-extension://") {
			sm.ensureExtensionTargetsAttachedSoon(extensionIDFromURL(targetURL))
			return
		}

		masterTargetID := masterCDP.getTargetIDBySessionID(sessionID)
		if masterTargetID == "" {
			// 100ms 轮询自愈：如果主端的 sessionID 还没绑定好 TargetID，进行自愈重试获取
			for i := 0; i < 10; i++ {
				time.Sleep(10 * time.Millisecond)
				masterTargetID = masterCDP.getTargetIDBySessionID(sessionID)
				if masterTargetID != "" {
					break
				}
			}
		}
		if masterTargetID == "" {
			fmt.Printf("[SyncManager Warning] Master SessionID %s not bound to any TargetID yet\n", sessionID)
			return
		}

		// ========== 防重入检查 ==========
		// 避免同一 sessionID + URL 的导航被重复处理（Chrome 可能发送多次 Page.frameNavigated）
		navKey := sessionID + "|" + targetURL
		sm.navigationDedupeMu.Lock()
		if lastTime, exists := sm.recentNavigations[navKey]; exists {
			if time.Since(lastTime) < 500*time.Millisecond {
				sm.navigationDedupeMu.Unlock()
				cdpVerbosef("[SyncManager] DEDUPE: Skipping duplicate navigation for %s (seen %v ago)\n", targetURL, time.Since(lastTime).Round(time.Millisecond))
				return
			}
		}
		sm.recentNavigations[navKey] = time.Now()
		// 清理过期的条目
		for k, t := range sm.recentNavigations {
			if time.Since(t) > 2*time.Second {
				delete(sm.recentNavigations, k)
			}
		}
		sm.navigationDedupeMu.Unlock()
		// ========== 防重入检查结束 ==========

		// 额外检查：如果 master URL 已经是目标 URL，跳过同步（避免对同一 URL 的重复同步）
		if !isPlaceholderURL(targetURL) {
			masterCDP.mu.Lock()
			masterCurrentURL := masterCDP.sessionURLs[sessionID]
			masterCDP.mu.Unlock()
			if masterCurrentURL == targetURL {
				cdpVerbosef("[SyncManager] Master session URL confirmed: %s\n", targetURL)
			}
		}

		cdpVerbosef("[SyncManager] Master Target %s navigated to: %s. Synchronizing to slaves...\n", masterTargetID, targetURL)
		sm.reapplyCurrentZoomToPID(uintptr(sm.masterWindow), masterCDP, "master-frame-navigated")

		// 2. 在 CDP 锁保护下获取从端对应的目标 Target 映射及客户端列表
		sm.cdpMu.Lock()
		slaveTargetMap := sm.targetMap[masterTargetID]
		// 复制映射表以避免并发竞态
		slaveTargets := make(map[uintptr]string)
		for h, tID := range slaveTargetMap {
			slaveTargets[h] = tID
		}

		slaveClients := make(map[uintptr]*cdpClient)
		for h, cdp := range sm.slaveCDPs {
			if cdp != nil && cdp.isConnected() {
				slaveClients[h] = cdp
			}
		}
		sm.cdpMu.Unlock()

		// 3. 在对应的从端会话上执行同步跳转
		for h, cdp := range slaveClients {
			slaveTargetID := slaveTargets[h]
			if slaveTargetID == "" {
				// 兜底与时序自愈：可能是新标签页刚创建，映射关系（targetMap）还在从端异步注册中。
				// 我们在此轮询等待 300ms 映射建立，若建立则跳转到新页面，否则回退至在活动标签内跳转。
				go func(c *cdpClient, handle uintptr) {
					var finalTargetID string
					for i := 0; i < 30; i++ {
						sm.cdpMu.Lock()
						mapping := sm.targetMap[masterTargetID]
						if mapping != nil {
							finalTargetID = mapping[handle]
						}
						sm.cdpMu.Unlock()

						if finalTargetID != "" {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}

					if finalTargetID != "" {
						var sID string
						// 等待从端 Tab 完成 CDP attach 绑定
						for j := 0; j < 15; j++ {
							sID = c.getSessionIDByTargetID(finalTargetID)
							if sID != "" {
								break
							}
							time.Sleep(20 * time.Millisecond)
						}
						if sID != "" {
							// 检查从端标签页当前 URL 是否已经是目标 URL
							currentURL := c.getURLForTarget(finalTargetID)
							if currentURL == targetURL {
								cdpVerbosef("[SyncManager] [fallback] Slave target %s already has URL %s, skipping navigate\n", finalTargetID, targetURL)
								sm.reapplyCurrentZoomToPID(handle, c, "slave-already-at-url-fallback")
								return
							}
							_ = c.sendSessionCommandNoWait(sID, "Page.navigate", map[string]interface{}{"url": targetURL})
							sm.reapplyCurrentZoomToPID(handle, c, "slave-navigate")
						}
					} else {
						// 确实无映射时的兜底：直接在活动标签中跳转
						// 但如果目标 URL 是占位 URL，跳过导航（不应该用占位 URL 覆盖有内容的标签页）
						if isPlaceholderURL(targetURL) {
							cdpVerbosef("[SyncManager] [fallback-skip] Skipping placeholder URL navigation: %s\n", targetURL)
							return
						}
						sID := c.findBestActiveSessionID()
						if sID != "" {
							_ = c.sendSessionCommandNoWait(sID, "Page.navigate", map[string]interface{}{"url": targetURL})
							sm.reapplyCurrentZoomToPID(handle, c, "slave-navigate-skip")
						}
					}
				}(cdp, h)
				continue
			}

			// 异步处理，防止在轮询等待时阻塞主事件循环
			go func(c *cdpClient, tID string, handle uintptr) {
				navStart := time.Now()
				var sID string
				// 300ms 轮询自愈：等待异步创建的从端 Tab 完成 CDP attach 附着
				for i := 0; i < 15; i++ {
					sID = c.getSessionIDByTargetID(tID)
					if sID != "" {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}

				if sID != "" {
					// 关键修复：检查从端标签页当前 URL 是否已经是目标 URL
					// 如果是，说明该标签页是刚刚由 handleMasterTargetCreated 创建的，
					// 已经携带了正确的 URL，无需重复导航
					currentURL := c.getURLForTarget(tID)
					cdpVerbosef("[SyncManager] onURLChanged navigation: targetID=%s currentURL=%s targetURL=%s (poll took %dms)\n", tID, currentURL, targetURL, time.Since(navStart).Milliseconds())
					if currentURL == targetURL {
						cdpVerbosef("[SyncManager] Slave target %s already has URL %s, skipping duplicate navigate\n", tID, targetURL)
						sm.reapplyCurrentZoomToPID(handle, c, "slave-already-at-url")
						return
					}
					cdpVerbosef("[SyncManager] Sending Page.navigate to slave targetID=%s sessionID=%s url=%s\n", tID, sID, targetURL)
					_ = c.sendSessionCommandNoWait(sID, "Page.navigate", map[string]interface{}{"url": targetURL})
					sm.reapplyCurrentZoomToPID(handle, c, "slave-navigate")
				} else {
					// 兜底：如果目标 URL 是占位 URL，跳过导航
					if isPlaceholderURL(targetURL) {
						cdpVerbosef("[SyncManager] [fallback-poll-skip] Skipping placeholder URL navigation: %s\n", targetURL)
						return
					}
					sID = c.findBestActiveSessionID()
					if sID != "" {
						_ = c.sendSessionCommandNoWait(sID, "Page.navigate", map[string]interface{}{"url": targetURL})
						sm.reapplyCurrentZoomToPID(handle, c, "slave-navigate-fallback")
					}
				}
			}(cdp, slaveTargetID, h)
		}
	}
	masterCDP.onURLReloaded = func(sessionID string, targetURL string) {
		sm.handleMasterURLReloaded(masterCDP, sessionID, targetURL)
	}
	masterCDP.onDomAction = func(sessionID string, actionJSON string) {
		sm.mu.RLock()
		if sm.isPaused {
			sm.mu.RUnlock()
			return
		}
		sm.mu.RUnlock()

		// Action 去重：防止同一 action 在短时间内被重复处理（防止回环）
		masterCDP.mu.Lock()
		masterURL := masterCDP.sessionURLs[sessionID]
		masterCDP.mu.Unlock()
		masterTargetID := masterCDP.getTargetIDBySessionID(sessionID)

		if sm.shouldSuppressPageClickAfterExtensionPopup(masterURL, actionJSON) {
			cdpVerbosef("[ExtensionPopup] Suppressed underlying page DOM click after popup open url=%s action=%s\n", masterURL, actionJSON)
			return
		}

		actionType := domActionType(actionJSON)
		if actionType == "click" {
			sm.markDomClickHandled()
		}
		if isExtensionURL(masterURL) {
			if actionType == "input" {
				return
			}
		}

		if sm.isDuplicateAction(actionJSON, sessionID) {
			cdpVerbosef("[SyncManager] Skipping duplicate action: %s\n", actionJSON)
			return
		}

		// 1. 获取主端的 targetID 和 masterURL
		cdpVerbosef("[SyncManager] Master DOM Action captured (sessionID=%s targetID=%s url=%s): %s\n", sessionID, masterTargetID, masterURL, actionJSON)

		// 检查 native 对话框：如果 master 窗口有 native 对话框（alert/confirm/prompt），
		// 则不将 action 同步到 slave，让 slave 自己等待用户的交互
		sm.mu.RLock()
		blocked := sm.domActionsBlocked
		sm.mu.RUnlock()
		if blocked {
			cdpVerbosef("[SyncManager] Skipping sync: native dialog active in master\n")
			return
		}

		sm.cdpMu.Lock()
		slaves := make([]*cdpClient, 0, len(sm.slaveCDPs))
		slaveHandles := make([]uintptr, 0, len(sm.slaveCDPs))
		for handle, cdp := range sm.slaveCDPs {
			if cdp != nil && cdp.isConnected() {
				slaves = append(slaves, cdp)
				slaveHandles = append(slaveHandles, handle)
			}
		}

		// 在 cdpMu 保护下完成 targetMap 查询（targetMap 由 cdpMu 保护）
		slaveSessionIDs := make([]string, len(slaves))
		for idx := range slaves {
			if masterTargetID != "" && idx < len(slaveHandles) && slaveHandles[idx] != 0 {
				if mappedSlaves, exists := sm.targetMap[masterTargetID]; exists {
					slaveTargetID := mappedSlaves[slaveHandles[idx]]
					if slaveTargetID != "" {
						slaveSessionIDs[idx] = slaves[idx].getSessionIDByTargetID(slaveTargetID)
					}
				}
			}
		}
		sm.cdpMu.Unlock()

		for idx, slave := range slaves {
			slaveSessionID := slaveSessionIDs[idx]
			handle := slaveHandles[idx]

			// 异步处理以防止轮询自愈阻塞主消息处理循环
			go func(c *cdpClient, h uintptr, sSessionID string) {
				if sSessionID == "" && masterURL != "" {
					// 轮询自愈：如果这是插件 Popup (或其他被动创建目标)，等待从端 attach 完成并建立 Session
					if extensionID := extensionIDFromURL(masterURL); extensionID != "" {
						for i := 0; i < 40; i++ {
							if i == 0 || i%5 == 0 {
								c.ensureExtensionTargetsAttached(extensionID)
							}
							sSessionID = c.findSessionIDByURL(masterURL)
							if sSessionID != "" {
								break
							}
							time.Sleep(15 * time.Millisecond)
						}
					} else {
						for i := 0; i < 30; i++ {
							sSessionID = c.findSessionIDByURL(masterURL)
							if sSessionID != "" {
								break
							}
							time.Sleep(10 * time.Millisecond)
						}
					}
				}

				if sSessionID == "" && isExtensionURL(masterURL) {
					fmt.Printf("[SyncManager] Skipping extension DOM action: no matching slave session handle=%d url=%s\n", h, masterURL)
					return
				}

				cdpVerbosef("[SyncManager] Broadcasting action to slave (matchedSessionID=%s handle=%d): %s\n", sSessionID, h, actionJSON)
				c.executeDomActionWithSession(sSessionID, actionJSON)
			}(slave, handle, slaveSessionID)
		}
	}

	if err := sm.connectCDPWithRetry(masterCDP, generation, deadline); err != nil {
		fmt.Printf("[SyncManager] master CDP connect failed (port=%d): %v\n", masterPort, err)
		return
	}

	var connectWg sync.WaitGroup
	for handle, info := range slaveInfos {
		if info.DebugPort == 0 {
			continue
		}
		connectWg.Add(1)
		go func(slaveHandle uintptr, slaveInfo common.WindowInfo) {
			defer connectWg.Done()

			cdp := newCDPClient(slaveInfo.DebugPort)
			if err := sm.connectCDPWithRetry(cdp, generation, deadline); err != nil {
				fmt.Printf("[SyncManager] slave CDP connect failed (handle=%d port=%d): %v\n", slaveHandle, slaveInfo.DebugPort, err)
				return
			}

			slaveCDPMu.Lock()
			slaveCDPs[slaveHandle] = cdp
			slaveCDPMu.Unlock()
		}(handle, info)
	}
	connectWg.Wait()

	if len(slaveCDPs) == 0 {
		masterCDP.close()
		return
	}

	expectedSlaves := 0
	for _, info := range slaveInfos {
		if info.DebugPort != 0 {
			expectedSlaves++
		}
	}
	if len(slaveCDPs) < expectedSlaves {
		fmt.Printf("[SyncManager] CDP startup incomplete: slaves=%d/%d\n", len(slaveCDPs), expectedSlaves)
		masterCDP.close()
		for _, cdp := range slaveCDPs {
			cdp.close()
		}
		return
	}

	sm.mu.RLock()
	isRunning := sm.isRunning
	sm.mu.RUnlock()
	if !isRunning || !sm.isCurrentCDPTabSyncGeneration(generation) {
		masterCDP.close()
		for _, cdp := range slaveCDPs {
			cdp.close()
		}
		return
	}

	sm.cdpMu.Lock()
	if generation != sm.cdpGeneration {
		sm.cdpMu.Unlock()
		masterCDP.close()
		for _, cdp := range slaveCDPs {
			cdp.close()
		}
		return
	}
	sm.stopCDPTabSyncLocked()
	sm.cdpGeneration = generation
	sm.masterCDP = masterCDP
	sm.slaveCDPs = slaveCDPs
	sm.targetMap = make(map[string]map[uintptr]string)
	sm.externallyMappedTargets = make(map[string]bool)

	sm.cdpMu.Unlock()

	if !sm.waitForCDPClientsReady(masterCDP, slaveCDPs, generation, deadline) {
		fmt.Printf("[SyncManager] CDP clients not ready before startup deadline\n")
		masterCDP.close()
		for _, cdp := range slaveCDPs {
			cdp.close()
		}
		return
	}

	sm.mapInitialCDPTargets(masterPort, slaveInfos)
	fmt.Printf("[SyncManager] CDP tab sync connected: master port=%d, slaves=%d\n", masterPort, len(slaveCDPs))

	sm.prepareExtensionActionPopupBehavior(masterCDP, slaveCDPs)
	if !sm.isCurrentCDPTabSyncGeneration(generation) {
		return
	}
	sm.mu.Lock()
	if sm.isRunning {
		sm.isPaused = false
	}
	sm.mu.Unlock()
}

func (sm *SyncManager) stopCDPTabSync() {
	sm.cdpMu.Lock()
	defer sm.cdpMu.Unlock()
	sm.stopCDPTabSyncLocked()
}

func (sm *SyncManager) stopCDPTabSyncLocked() {
	sm.cdpStarting = false
	sm.cdpGeneration++
	if sm.masterCDP != nil {
		sm.masterCDP.restoreExtensionActionPopupBehavior()
		sm.masterCDP.close()
		sm.masterCDP = nil
	}
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil {
			cdp.restoreExtensionActionPopupBehavior()
			cdp.close()
		}
	}
	sm.slaveCDPs = make(map[uintptr]*cdpClient)
	sm.targetMap = make(map[string]map[uintptr]string)
	sm.externallyMappedTargets = make(map[string]bool)
}

func (sm *SyncManager) markCDPTabSyncStarting() uint64 {
	sm.cdpMu.Lock()
	defer sm.cdpMu.Unlock()
	sm.cdpGeneration++
	sm.cdpStarting = true
	return sm.cdpGeneration
}

func (sm *SyncManager) markCDPTabSyncStoppedStarting(generation uint64) {
	sm.cdpMu.Lock()
	defer sm.cdpMu.Unlock()
	if sm.cdpGeneration == generation {
		sm.cdpStarting = false
	}
}

func (sm *SyncManager) isCurrentCDPTabSyncGeneration(generation uint64) bool {
	sm.cdpMu.Lock()
	defer sm.cdpMu.Unlock()
	return sm.cdpGeneration == generation
}

func (sm *SyncManager) hasActiveOrStartingMasterCDP() bool {
	sm.cdpMu.Lock()
	defer sm.cdpMu.Unlock()
	return sm.cdpStarting || (sm.masterCDP != nil && sm.masterCDP.isConnected())
}

func (sm *SyncManager) WaitForCDPTabSyncReady(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		sm.mu.RLock()
		running := sm.isRunning
		paused := sm.isPaused
		sm.mu.RUnlock()
		if !running {
			return false
		}

		sm.windowMetadataMu.RLock()
		expectedSlaves := 0
		for _, info := range sm.slaveMetadata {
			if info.DebugPort != 0 {
				expectedSlaves++
			}
		}
		sm.windowMetadataMu.RUnlock()

		sm.cdpMu.Lock()
		ready := !paused &&
			!sm.cdpStarting &&
			sm.masterCDP != nil &&
			sm.masterCDP.isReady() &&
			len(sm.slaveCDPs) >= expectedSlaves
		if ready {
			for _, cdp := range sm.slaveCDPs {
				if cdp == nil || !cdp.isReady() {
					ready = false
					break
				}
			}
		}
		sm.cdpMu.Unlock()
		if ready {
			return true
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return false
		}

		<-ticker.C
	}
}

func (sm *SyncManager) prepareExtensionActionPopupBehavior(masterCDP *cdpClient, slaveCDPs map[uintptr]*cdpClient) {
	clients := make([]*cdpClient, 0, len(slaveCDPs)+1)
	if masterCDP != nil && masterCDP.isConnected() {
		clients = append(clients, masterCDP)
	}
	for _, cdp := range slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			clients = append(clients, cdp)
		}
	}
	if len(clients) == 0 {
		return
	}

	var wg sync.WaitGroup
	var changed atomic.Int32
	for _, cdp := range clients {
		wg.Add(1)
		go func(c *cdpClient) {
			defer wg.Done()
			changed.Add(int32(c.prepareExtensionActionPopupBehavior()))
		}(cdp)
	}
	wg.Wait()
	if changed.Load() > 0 {
		cdpVerbosef("[ExtensionPopup] prepared side panel action behavior: changed=%d clients=%d\n", changed.Load(), len(clients))
	}
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
		base != "index.html" {
		return "", false
	}
	return parts[0], true
}

func isExtensionRuntimeTargetNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "extension runtime target not found")
}

func (sm *SyncManager) rememberExtensionTriggerClick(route clickRouteState) {
	if !route.reliable || route.sourceWindow != uintptr(sm.masterWindow) {
		return
	}
	sm.extensionTriggerMu.Lock()
	sm.lastExtensionTrigger = route
	sm.lastExtensionClickAt = time.Now()
	sm.extensionTriggerMu.Unlock()
}

func (sm *SyncManager) openExtensionPopupOnSlaves(extensionID string) {
	if extensionID == "" {
		return
	}

	sm.extensionPopupMu.Lock()
	if sm.recentExtensionPopups == nil {
		sm.recentExtensionPopups = make(map[string]time.Time)
	}
	if lastTime, exists := sm.recentExtensionPopups[extensionID]; exists && time.Since(lastTime) < 800*time.Millisecond {
		sm.extensionPopupMu.Unlock()
		return
	}
	sm.recentExtensionPopups[extensionID] = time.Now()
	sm.lastExtensionPopupAt = time.Now()
	for id, t := range sm.recentExtensionPopups {
		if time.Since(t) > 5*time.Second {
			delete(sm.recentExtensionPopups, id)
		}
	}
	sm.extensionPopupMu.Unlock()

	sm.cdpMu.Lock()
	slaveSnapshot := make(map[uintptr]*cdpClient, len(sm.slaveCDPs))
	for handle, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			slaveSnapshot[handle] = cdp
		}
	}
	sm.cdpMu.Unlock()

	if len(slaveSnapshot) == 0 {
		return
	}

	pending := slaveSnapshot
	successCount := 0
	failedHandles := make(map[uintptr]bool)
	retryDelays := []time.Duration{0, 900 * time.Millisecond}
	for attempt, delay := range retryDelays {
		if len(pending) == 0 {
			break
		}
		if delay > 0 {
			time.Sleep(delay)
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		nextPending := make(map[uintptr]*cdpClient)
		for handle, cdp := range pending {
			wg.Add(1)
			go func(h uintptr, c *cdpClient) {
				defer wg.Done()
				if err := c.openExtensionActionPopup(extensionID, false); err != nil {
					if isExtensionRuntimeTargetNotFound(err) && attempt < len(retryDelays)-1 {
						mu.Lock()
						nextPending[h] = c
						mu.Unlock()
						return
					}
					mu.Lock()
					failedHandles[h] = true
					mu.Unlock()
					fmt.Printf("[ExtensionPopup] openPopup failed handle=%d extension=%s: %v\n", h, extensionID, err)
					return
				}
				if !c.waitForVisibleExtensionTarget(extensionID, 500*time.Millisecond) {
					if attempt < len(retryDelays)-1 {
						mu.Lock()
						nextPending[h] = c
						mu.Unlock()
						return
					}
					mu.Lock()
					failedHandles[h] = true
					mu.Unlock()
					fmt.Printf("[ExtensionPopup] openPopup not visible handle=%d extension=%s\n", h, extensionID)
					return
				}
				mu.Lock()
				successCount++
				mu.Unlock()
			}(handle, cdp)
		}
		wg.Wait()
		pending = nextPending
	}
	fmt.Printf("[ExtensionPopup] openPopup extension=%s slaves=%d/%d\n", extensionID, successCount, len(slaveSnapshot))
	if successCount > 0 {
		sm.requestPopupSession(popupSessionDuration)
		sm.ensureExtensionTargetsAttachedSoon(extensionID)
		go sm.refreshPopupCacheBurst([]time.Duration{
			40 * time.Millisecond,
			120 * time.Millisecond,
			240 * time.Millisecond,
			480 * time.Millisecond,
		})
	}
}

func (sm *SyncManager) maybeOpenExtensionPopupOnSlaves(extensionID string) {
	sm.ensureExtensionTargetsAttachedSoon(extensionID)
	sm.openExtensionPopupOnSlaves(extensionID)
}

func (sm *SyncManager) ensureExtensionTargetsAttachedSoon(extensionID string) {
	if extensionID == "" {
		return
	}

	sm.cdpMu.Lock()
	clients := make([]*cdpClient, 0, len(sm.slaveCDPs)+1)
	if sm.masterCDP != nil && sm.masterCDP.isConnected() {
		clients = append(clients, sm.masterCDP)
	}
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			clients = append(clients, cdp)
		}
	}
	sm.cdpMu.Unlock()

	if len(clients) == 0 {
		return
	}

	for _, delay := range []time.Duration{0, 180 * time.Millisecond, 520 * time.Millisecond} {
		delay := delay
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			var wg sync.WaitGroup
			for _, client := range clients {
				wg.Add(1)
				go func(cdp *cdpClient) {
					defer wg.Done()
					cdp.ensureExtensionTargetsAttached(extensionID)
				}(client)
			}
			wg.Wait()
		}()
	}
}

func (sm *SyncManager) handleMasterURLReloaded(masterCDP *cdpClient, sessionID string, targetURL string) {
	if targetURL == "" || isPlaceholderURL(targetURL) || isIgnoredCDPTargetURL(targetURL) {
		return
	}
	if _, ok := extensionPopupID(targetURL); ok {
		return
	}
	if strings.HasPrefix(targetURL, "chrome-extension://") {
		return
	}

	sm.mu.RLock()
	if sm.isPaused {
		sm.mu.RUnlock()
		return
	}
	sm.mu.RUnlock()

	masterTargetID := masterCDP.getTargetIDBySessionID(sessionID)
	if masterTargetID == "" {
		return
	}

	sm.cdpMu.Lock()
	slaveTargets := make(map[uintptr]string)
	if mapped := sm.targetMap[masterTargetID]; mapped != nil {
		for handle, targetID := range mapped {
			slaveTargets[handle] = targetID
		}
	}
	slaveClients := make(map[uintptr]*cdpClient)
	for handle, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			slaveClients[handle] = cdp
		}
	}
	sm.cdpMu.Unlock()

	cdpVerbosef("[SyncManager] Master Target %s reloaded: %s. Reloading matching slaves...\n", masterTargetID, targetURL)
	for handle, cdp := range slaveClients {
		slaveTargetID := slaveTargets[handle]
		go func(h uintptr, c *cdpClient, tID string) {
			var sID string
			if tID != "" {
				for i := 0; i < 15; i++ {
					sID = c.getSessionIDByTargetID(tID)
					if sID != "" {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				if sID == "" {
					return
				}
				if currentURL := c.getURLForTarget(tID); currentURL != targetURL {
					return
				}
			} else {
				sID = c.findSessionIDByURL(targetURL)
				if sID == "" {
					return
				}
			}

			cdpVerbosef("[SyncManager] Sending Page.reload to slave handle=%d sessionID=%s url=%s\n", h, sID, targetURL)
			_ = c.sendSessionCommandNoWait(sID, "Page.reload", map[string]interface{}{"ignoreCache": false})
			sm.reapplyCurrentZoomToPID(h, c, "slave-reload")
		}(handle, cdp, slaveTargetID)
	}
}

// handleMasterTargetCreated 处理主窗口目标创建事件
func (sm *SyncManager) handleMasterTargetCreated(masterTargetID string, targetURL string) {
	startTime := time.Now()
	cdpVerbosef("[SyncManager] handleMasterTargetCreated: masterTargetID=%s url=%s timestamp=%s\n", masterTargetID, targetURL, startTime.Format("15:04:05.000"))
	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	sm.mu.RUnlock()

	// 如果是 chrome-extension 开头的目标，从端会通过物理同步自动打开，无需在这里重复创建
	if extensionID, ok := extensionPopupID(targetURL); ok {
		sm.maybeOpenExtensionPopupOnSlaves(extensionID)
		return
	}

	if strings.HasPrefix(targetURL, "chrome-extension://") {
		sm.ensureExtensionTargetsAttachedSoon(extensionIDFromURL(targetURL))
		return
	}

	sm.cdpMu.Lock()
	slaveSnapshot := make(map[uintptr]*cdpClient, len(sm.slaveCDPs))
	for handle, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			slaveSnapshot[handle] = cdp
		}
	}
	if _, ok := sm.targetMap[masterTargetID]; !ok {
		sm.targetMap[masterTargetID] = make(map[uintptr]string)
	}
	sm.cdpMu.Unlock()

	if len(slaveSnapshot) == 0 {
		return
	}
	recentDomClickHandled := sm.hasRecentDomClickHandledSince(startTime)

	mappedExisting := make(map[uintptr]bool)
	sm.cdpMu.Lock()
	for handle, targetID := range sm.targetMap[masterTargetID] {
		if targetID != "" {
			mappedExisting[handle] = true
		}
	}
	sm.cdpMu.Unlock()

	if isBrowserInternalURL(targetURL) && !isPlaceholderURL(targetURL) {
		deadline := time.Now().Add(250 * time.Millisecond)
		for {
			for handle, cdp := range slaveSnapshot {
				if mappedExisting[handle] {
					continue
				}
				if targetID := cdp.ensureTargetAttachedByURL(targetURL); targetID != "" {
					sm.cdpMu.Lock()
					if _, ok := sm.targetMap[masterTargetID]; !ok {
						sm.targetMap[masterTargetID] = make(map[uintptr]string)
					}
					sm.targetMap[masterTargetID][handle] = targetID
					sm.cdpMu.Unlock()
					mappedExisting[handle] = true
				}
			}
			if len(mappedExisting) == len(slaveSnapshot) || time.Now().After(deadline) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if len(mappedExisting) > 0 {
			cdpVerbosef("[SyncManager] reused existing internal target url=%s slaves=%d/%d\n", targetURL, len(mappedExisting), len(slaveSnapshot))
		}
		if len(mappedExisting) == len(slaveSnapshot) {
			return
		}
	}

	if isPlaceholderURL(targetURL) && recentDomClickHandled {
		attachedSince := startTime.Add(-250 * time.Millisecond)
		deadline := time.Now().Add(250 * time.Millisecond)
		for {
			for handle, cdp := range slaveSnapshot {
				if mappedExisting[handle] {
					continue
				}
				if targetID := cdp.findNewestPlaceholderPageTargetAttachedSince(attachedSince); targetID != "" {
					sm.cdpMu.Lock()
					if _, ok := sm.targetMap[masterTargetID]; !ok {
						sm.targetMap[masterTargetID] = make(map[uintptr]string)
					}
					sm.targetMap[masterTargetID][handle] = targetID
					sm.cdpMu.Unlock()
					mappedExisting[handle] = true
				}
			}
			if len(mappedExisting) == len(slaveSnapshot) || time.Now().After(deadline) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if len(mappedExisting) > 0 {
			cdpVerbosef("[SyncManager] reused existing placeholder target url=%s slaves=%d/%d\n", targetURL, len(mappedExisting), len(slaveSnapshot))
		}
		if len(mappedExisting) == len(slaveSnapshot) {
			return
		}
	}

	for handle, cdp := range slaveSnapshot {
		if mappedExisting[handle] {
			continue
		}
		go func(slaveHandle uintptr, slaveCDP *cdpClient) {
			// 使用 master 传入的 targetURL 创建标签页（如果有的话）
			// 如果是占位 URL（空白页），让 Chrome 默认创建，URL 导航由 onURLChanged 处理
			createURL := targetURL
			if isPlaceholderURL(targetURL) {
				if recentDomClickHandled {
					createURL = ""
				} else {
					createURL = sm.browserInitialNewTabURL()
					cdpVerbosef("[SyncManager] placeholder target without DOM click, creating browser new tab: %s\n", createURL)
				}
			}
			slaveTargetID, err := slaveCDP.createTarget(createURL)
			if err != nil {
				fmt.Printf("[SyncManager] slave CDP create target failed (handle=%d): %v\n", slaveHandle, err)
				return
			}

			sm.cdpMu.Lock()
			if _, ok := sm.targetMap[masterTargetID]; !ok {
				sm.targetMap[masterTargetID] = make(map[uintptr]string)
			}
			sm.targetMap[masterTargetID][slaveHandle] = slaveTargetID
			sm.cdpMu.Unlock()

			// 等待 session 建立（Target.attachedToTarget 事件触发）
			// 这样可以确保 activateTarget 和后续的 Page.navigate 发送到正确的 session
			for i := 0; i < 20; i++ {
				slaveCDP.mu.Lock()
				hasSession := slaveCDP.targetSessions[slaveTargetID] != ""
				slaveCDP.mu.Unlock()
				if hasSession {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			_ = slaveCDP.activateTarget(slaveTargetID)
			sm.reapplyCurrentZoomToPID(slaveHandle, slaveCDP, "slave-target-created")

			// ========== 方案 A: 轮询 slave URL 直到不再是占位 URL ==========
			// 这是 onURLChanged 的备份机制：当 Page.frameNavigated 没有被触发时
			// （例如 Chrome 没有发送该事件或 session 尚未建立），我们直接轮询获取真实 URL
			// 然后把这个 URL 同步给所有其他 slave
			go func(sID string, sTargetID string, sHandle uintptr) {
				deadline := time.Now().Add(5000 * time.Millisecond)
				for time.Now().Before(deadline) {
					sm.mu.RLock()
					isRunning := sm.isRunning && !sm.isPaused
					sm.mu.RUnlock()
					if !isRunning {
						return
					}

					// 通过 Runtime.evaluate 获取当前真实的 URL
					resp, err := slaveCDP.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
						"expression":    "window.location.href",
						"returnByValue": true,
					}, sID, false)
					if err == nil {
						result, _ := resp["result"].(map[string]interface{})
						if result != nil {
							valueMap, _ := result["result"].(map[string]interface{})
							if valueMap != nil {
								actualURL, _ := valueMap["value"].(string)
								if !isPlaceholderURL(actualURL) && !isIgnoredCDPTargetURL(actualURL) {
									cdpVerbosef("[SyncManager] [fallback-poll] Detected URL change via polling: session=%s url=%s\n", sID, actualURL)

									// 把检测到的 URL 同步给所有其他 slave（除了当前这个）
									sm.cdpMu.Lock()
									for otherHandle, otherCDP := range sm.slaveCDPs {
										if otherHandle == sHandle || otherCDP == nil || !otherCDP.isConnected() {
											continue
										}
										// 获取其他 slave 对应 masterTargetID 的 targetID
										otherTargetID := ""
										if slaveMap, ok := sm.targetMap[masterTargetID]; ok {
											otherTargetID = slaveMap[otherHandle]
										}
										if otherTargetID == "" {
											continue
										}
										otherSessionID := otherCDP.getSessionIDByTargetID(otherTargetID)
										if otherSessionID == "" {
											continue
										}
										// 检查其他 slave 是否已经有这个 URL
										otherResp, _ := otherCDP.sendCommandWithSession("Runtime.evaluate", map[string]interface{}{
											"expression":    "window.location.href",
											"returnByValue": true,
										}, otherSessionID, false)
										if otherResp != nil {
											if res, ok := otherResp["result"].(map[string]interface{}); ok {
												if res2, ok := res["result"].(map[string]interface{}); ok {
													if url, ok := res2["value"].(string); ok && url == actualURL {
														continue // 其他 slave 已经有这个 URL，跳过
													}
												}
											}
										}
										// 导航到目标 URL
										_ = otherCDP.sendSessionCommandNoWait(otherSessionID, "Page.navigate", map[string]interface{}{"url": actualURL})
										sm.reapplyCurrentZoomToPID(otherHandle, otherCDP, "slave-navigate-poll")
									}
									sm.cdpMu.Unlock()
									return
								}
							}
						}
					}
					time.Sleep(100 * time.Millisecond)
				}
				cdpVerbosef("[SyncManager] [fallback-poll] Polling timeout, giving up on session %s\n", sID)
			}(slaveCDP.targetSessions[slaveTargetID], slaveTargetID, handle)

			cdpVerbosef("[SyncManager] Slave target created (handle=%d) slaveTargetID=%s totalElapsed=%dms\n", slaveHandle, slaveTargetID, time.Since(startTime).Milliseconds())
		}(handle, cdp)
	}
}

func (sm *SyncManager) handleMasterTargetDestroyed(masterTargetID string) {
	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	sm.mu.RUnlock()

	sm.cdpMu.Lock()
	slaveMap := sm.targetMap[masterTargetID]
	externalMapping := sm.externallyMappedTargets[masterTargetID]
	slaveSnapshot := make(map[uintptr]*cdpClient, len(slaveMap))
	for handle := range slaveMap {
		if cdp := sm.slaveCDPs[handle]; cdp != nil && cdp.isConnected() {
			slaveSnapshot[handle] = cdp
		}
	}
	delete(sm.targetMap, masterTargetID)
	delete(sm.externallyMappedTargets, masterTargetID)
	sm.cdpMu.Unlock()

	if externalMapping {
		return
	}

	for handle, cdp := range slaveSnapshot {
		targetID := slaveMap[handle]
		if targetID == "" {
			continue
		}
		go func(slaveHandle uintptr, slaveCDP *cdpClient, slaveTargetID string) {
			if err := slaveCDP.closeTarget(slaveTargetID); err != nil {
				fmt.Printf("[SyncManager] slave CDP close target failed (handle=%d): %v\n", slaveHandle, err)
			}
		}(handle, cdp, targetID)
	}
}

func (sm *SyncManager) handleMasterTabActivated(masterTargetID string, targetURL string) {
	startTime := time.Now()
	cdpVerbosef("[SyncManager] handleMasterTabActivated: masterTargetID=%s timestamp=%s\n", masterTargetID, startTime.Format("15:04:05.000"))
	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	sm.mu.RUnlock()

	sm.cdpMu.Lock()
	masterIndex := -1
	if sm.masterCDP != nil {
		if targetURL == "" {
			targetURL = sm.masterCDP.getURLForTarget(masterTargetID)
		}
		masterIndex = sm.masterCDP.getTargetIndex(masterTargetID)
	}
	slaveMap := sm.targetMap[masterTargetID]
	slaveSnapshot := make(map[uintptr]*cdpClient, len(sm.slaveCDPs))
	for handle, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			slaveSnapshot[handle] = cdp
		}
	}
	sm.cdpMu.Unlock()

	if strings.HasPrefix(targetURL, "chrome-extension://") {
		return
	}

	if sm.masterCDP != nil {
		sm.reapplyCurrentZoomToPID(uintptr(sm.masterWindow), sm.masterCDP, "master-tab-activated")
	}

	for handle, cdp := range slaveSnapshot {
		targetID := ""
		if slaveMap != nil {
			targetID = slaveMap[handle]
		}
		if targetID == "" && masterIndex >= 0 {
			targetID = cdp.getTargetIDAtIndex(masterIndex)
			if targetID != "" {
				sm.cdpMu.Lock()
				if _, ok := sm.targetMap[masterTargetID]; !ok {
					sm.targetMap[masterTargetID] = make(map[uintptr]string)
				}
				sm.targetMap[masterTargetID][handle] = targetID
				sm.cdpMu.Unlock()
			}
		}
		if targetID == "" {
			continue
		}
		go func(slaveCDP *cdpClient, slaveTargetID string, h uintptr) {
			_ = slaveCDP.activateTarget(slaveTargetID)
			sm.reapplyCurrentZoomToPID(h, slaveCDP, "slave-tab-activated")
		}(cdp, targetID, handle)
	}
}

func (sm *SyncManager) mapInitialCDPTargets(masterPort int, slaveInfos map[uintptr]common.WindowInfo) {
	masterTabs, err := sm.tabManager.GetPageTabs(masterPort)
	if err != nil || len(masterTabs) == 0 {
		return
	}

	for handle, info := range slaveInfos {
		if info.DebugPort == 0 {
			continue
		}

		slaveTabs, err := sm.tabManager.GetPageTabs(info.DebugPort)
		if err != nil || len(slaveTabs) == 0 {
			continue
		}

		minLen := len(masterTabs)
		if len(slaveTabs) < minLen {
			minLen = len(slaveTabs)
		}

		sm.cdpMu.Lock()
		for i := 0; i < minLen; i++ {
			masterTargetID := masterTabs[i].ID
			slaveTargetID := slaveTabs[i].ID
			if masterTargetID == "" || slaveTargetID == "" {
				continue
			}
			if _, ok := sm.targetMap[masterTargetID]; !ok {
				sm.targetMap[masterTargetID] = make(map[uintptr]string)
			}
			sm.targetMap[masterTargetID][handle] = slaveTargetID
		}
		sm.cdpMu.Unlock()
	}

}

// IsRunning checks if sync is active.
func (sm *SyncManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.isRunning
}

// GetState returns current sync state.
func (sm *SyncManager) GetState() common.SyncState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := common.SyncStatusIdle
	if sm.isRunning {
		status = common.SyncStatusRunning
	}
	if sm.isPaused {
		status = common.SyncStatusPaused
	}

	var masterInfo *common.WindowInfo
	if sm.masterWindow != 0 {
		masterInfo = &common.WindowInfo{Handle: sm.masterWindow}
	}

	slaves := make([]common.WindowInfo, len(sm.slaveWindows))
	for i, h := range sm.slaveWindows {
		slaves[i] = common.WindowInfo{Handle: h}
	}

	return common.SyncState{
		Status:       status,
		MasterWindow: masterInfo,
		SlaveWindows: slaves,
		TotalAgents:  len(slaves),
		ActiveAgents: len(slaves),
		Config:       sm.config,
	}
}

// SetConfig updates sync configuration.
func (sm *SyncManager) SetConfig(config common.SyncConfig) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.config = config

	// Update optimized intervals
	sm.optimizedKeyboardInterval = config.KeyboardInterval
	sm.optimizedMouseInterval = config.MouseMoveInterval
	sm.optimizedWheelInterval = config.WheelEventThreshold

	return nil
}

// GetConfig returns current configuration.
func (sm *SyncManager) GetConfig() common.SyncConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config
}

func (sm *SyncManager) hookEventBufferSize() int {
	if sm.config.EventBufferSize > 0 {
		return sm.config.EventBufferSize
	}
	return defaultHookEventBufferSize
}

func (sm *SyncManager) SetWindowMetadata(master *common.WindowInfo, slaves []common.WindowInfo) error {
	sm.windowMetadataMu.Lock()
	defer sm.windowMetadataMu.Unlock()

	if master != nil {
		masterCopy := *master
		sm.masterMetadata = &masterCopy
	} else {
		sm.masterMetadata = nil
	}

	sm.slaveMetadata = make(map[uintptr]common.WindowInfo, len(slaves))
	for _, slave := range slaves {
		slaveCopy := slave
		handle := uintptr(slaveCopy.Handle)
		if handle == 0 {
			handle = slaveCopy.HWND
		}
		if handle == 0 {
			continue
		}
		sm.slaveMetadata[handle] = slaveCopy
	}

	return nil
}

// PauseSync pauses implementation.
func (sm *SyncManager) PauseSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.isRunning {
		sm.isPaused = true
	}
	return nil
}

// ResumeSync resumes implementation.
func (sm *SyncManager) ResumeSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.isRunning {
		sm.isPaused = false
	}
	return nil
}

func (sm *SyncManager) runMessageLoop(errChan chan<- error) {
	// Hooks require the thread to stay alive and pump messages
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	startReported := false
	defer func() {
		if r := recover(); r != nil {
			if !startReported {
				errChan <- fmt.Errorf("hook message loop panic: %v", r)
				return
			}
			fmt.Printf("[SyncManager] hook message loop panic after start: %v\n", r)
		}
	}()

	// Store thread ID for Stop() to target
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	getCurrentThreadId := kernel32.NewProc("GetCurrentThreadId")
	tid, _, _ := getCurrentThreadId.Call()
	sm.mu.Lock()
	sm.loopThreadID = uint32(tid)
	sm.mu.Unlock()

	// Install hooks ON THIS THREAD
	var errs []string
	if err := sm.setupKeyboardHook(); err != nil {
		errs = append(errs, fmt.Sprintf("keyboard hook failed: %v", err))
	}
	if err := sm.setupMouseHook(); err != nil {
		errs = append(errs, fmt.Sprintf("mouse hook failed: %v", err))
	}

	if len(errs) > 0 {
		sm.forceUnhookAll()
		errChan <- fmt.Errorf("hook installation failed: %s", strings.Join(errs, "; "))
		startReported = true
		return
	}

	// Signal success
	errChan <- nil
	startReported = true

	// Message Loop
	user32 := windows.NewLazyDLL("user32.dll")
	getMessage := user32.NewProc("GetMessageW")
	translateMessage := user32.NewProc("TranslateMessage")
	dispatchMessage := user32.NewProc("DispatchMessageW")

	msg := &struct {
		Hwnd    uintptr
		Message uint32
		WParam  uintptr
		LParam  uintptr
		Time    uint32
		Pt      struct{ X, Y int32 }
	}{}

	sm.messageLoopActive = true
	defer func() {
		sm.messageLoopActive = false
		sm.forceUnhookAll() // Cleanup on exit
	}()

	for {
		// GetMessage blocks until a message arrives
		ret, _, _ := getMessage.Call(uintptr(unsafe.Pointer(msg)), 0, 0, 0)

		// 0 = WM_QUIT, -1 = Error
		if ret == 0 || int32(ret) == -1 {
			break
		}

		// Check if we should stop (extra safety if PostThreadMessage fails)
		select {
		case <-sm.stopChan:
			return
		default:
		}

		translateMessage.Call(uintptr(unsafe.Pointer(msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(msg)))
	}
}

// --- Hook Implementations ---

func (sm *SyncManager) forceUnhookAll() {
	if sm.keyboardHookID != 0 {
		sm.unhookWindowsHookEx.Call(sm.keyboardHookID)
		sm.keyboardHookID = 0
	}
	if sm.mouseHookID != 0 {
		sm.unhookWindowsHookEx.Call(sm.mouseHookID)
		sm.mouseHookID = 0
	}
}

var (
	globalSyncManager   *SyncManager
	globalSyncManagerMu sync.RWMutex

	keyboardHookCallback uintptr
	mouseHookCallback    uintptr
	hooksOnce            sync.Once

	// enumChildForRenderWidget is allocated once and reused for every call to
	// findRenderWidgetHostHWND. windows.NewCallback consumes a finite CGO thread
	// slot each time it is called; allocating inside the hot path exhausts the
	// pool and causes silent failures on some windows.
	enumChildForRenderWidget     uintptr
	enumChildForRenderWidgetOnce sync.Once
)

func initGlobalHooks() {
	hooksOnce.Do(func() {
		keyboardHookCallback = windows.NewCallback(func(nCode int, wParam uintptr, lParam uintptr) uintptr {
			defer func() {
				if r := recover(); r != nil {
					// 静默吞噬：键盘全局事件 Hook 发生 panic
					_ = r
				}
			}()

			globalSyncManagerMu.RLock()
			sm := globalSyncManager
			globalSyncManagerMu.RUnlock()

			if sm != nil && nCode >= 0 {
				kb := *(*KBDLLHOOKSTRUCT)(unsafe.Pointer(lParam)) //nolint:govet,unsafeptr // Windows API interaction

				// 1. 同步更新修饰键状态 (确保绝对灵敏且及时的检测)
				sm.updateModifiersGlobally(int(wParam), kb)
				if sm.handleZoomShortcutInHook(int(wParam), kb) {
					return 1
				}
				sm.processKeyboardHook(int(wParam), kb)
				hookResult, _, _ := sm.callNextHookEx.Call(sm.keyboardHookID, uintptr(nCode), wParam, lParam)
				return hookResult
			}
			return 0
		})

		mouseHookCallback = windows.NewCallback(func(nCode int, wParam uintptr, lParam uintptr) uintptr {
			defer func() {
				if r := recover(); r != nil {
					// 静默吞噬：鼠标全局事件 Hook 发生 panic
					_ = r
				}
			}()

			globalSyncManagerMu.RLock()
			sm := globalSyncManager
			globalSyncManagerMu.RUnlock()

			if sm != nil && nCode >= 0 {
				ms := *(*MSLLHOOKSTRUCT)(unsafe.Pointer(lParam)) //nolint:govet,unsafeptr // Windows API interaction
				sm.processMouseHook(int(wParam), ms)
				r, _, _ := sm.callNextHookEx.Call(sm.mouseHookID, uintptr(nCode), wParam, lParam)
				return r
			}
			return 0
		})
	})
}

func (sm *SyncManager) setupKeyboardHook() error {
	initGlobalHooks()
	globalSyncManagerMu.Lock()
	globalSyncManager = sm
	globalSyncManagerMu.Unlock()

	// Match Legacy: Use windows.NewCallback
	sm.keyboardHookProc = keyboardHookCallback

	// Match Legacy: Get Module Handle
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	getModuleHandle := kernel32.NewProc("GetModuleHandleW")
	hMod, _, _ := getModuleHandle.Call(0)

	ret, _, err := sm.setWindowsHookEx.Call(13, keyboardHookCallback, hMod, 0)
	if ret == 0 {
		return err
	}
	sm.keyboardHookID = ret
	return nil
}

func (sm *SyncManager) setupMouseHook() error {
	initGlobalHooks()
	globalSyncManagerMu.Lock()
	globalSyncManager = sm
	globalSyncManagerMu.Unlock()
	sm.mouseHookProc = mouseHookCallback

	// Match Legacy: Get Module Handle
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	getModuleHandle := kernel32.NewProc("GetModuleHandleW")
	hMod, _, _ := getModuleHandle.Call(0)

	ret, _, err := sm.setWindowsHookEx.Call(14, mouseHookCallback, hMod, 0)
	if ret == 0 {
		return err
	}
	sm.mouseHookID = ret
	return nil
}

// --- Processing Logic ---

type KBDLLHOOKSTRUCT struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type MSLLHOOKSTRUCT struct {
	Pt          struct{ X, Y int32 }
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

func (sm *SyncManager) updateModifiersGlobally(wParam int, kb KBDLLHOOKSTRUCT) {
	switch kb.VkCode {
	case VK_CONTROL, VK_LCONTROL, VK_RCONTROL:
		if wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN {
			sm.ctrlPressed.Store(true)
		} else if wParam == WM_KEYUP || wParam == WM_SYSKEYUP {
			sm.ctrlPressed.Store(false)
		}
	case VK_SHIFT, VK_LSHIFT, VK_RSHIFT:
		if wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN {
			sm.shiftPressed.Store(true)
		} else if wParam == WM_KEYUP || wParam == WM_SYSKEYUP {
			sm.shiftPressed.Store(false)
		}
	case VK_MENU, VK_LMENU, VK_RMENU:
		if wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN {
			sm.altPressed.Store(true)
		} else if wParam == WM_KEYUP || wParam == WM_SYSKEYUP {
			sm.altPressed.Store(false)
		}
	}
}

func (sm *SyncManager) processKeyboardHook(wParam int, kb KBDLLHOOKSTRUCT) {
	if sm.isPaused {
		return
	}

	sm.updateModifiersGlobally(wParam, kb)

	// Capture foreground window at hook time, same reason as mouse hook.
	activeWindow, _, _ := sm.getForegroundWindow.Call()
	ev := hookEvent{kind: hookKindKeyboard, wParam: wParam, kb: kb, activeWindow: uintptr(activeWindow)}
	select {
	case sm.hookEventCh <- ev:
	default:
	}
}

func (sm *SyncManager) vkCodeToChar(vk uint32, scanCode uint32) string {
	user32 := windows.NewLazyDLL("user32.dll")
	toUnicode := user32.NewProc("ToUnicode")

	var keyState [256]byte
	if sm.isVirtualKeyPressed(VK_SHIFT) {
		keyState[VK_SHIFT] = 0x80
		keyState[0xA0] = 0x80 // VK_LSHIFT
	}
	if sm.isVirtualKeyPressed(VK_CONTROL) {
		keyState[VK_CONTROL] = 0x80
		keyState[0xA2] = 0x80 // VK_LCONTROL
	}
	if sm.isVirtualKeyPressed(VK_MENU) {
		keyState[VK_MENU] = 0x80
		keyState[0xA4] = 0x80 // VK_LMENU
	}

	var buf [16]uint16
	ret, _, _ := toUnicode.Call(
		uintptr(vk),
		uintptr(scanCode),
		uintptr(unsafe.Pointer(&keyState[0])),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)

	if ret > 0 {
		return string(utf16.Decode(buf[:ret]))
	}
	return ""
}

func (sm *SyncManager) dispatchKeyboardEvent(wParam int, kb KBDLLHOOKSTRUCT, activeWindow uintptr) {
	if kb.DwExtraInfo == 0xFFFFFFFF {
		return
	}

	currentWindow := activeWindow
	normalizedWindow, isManagedSource := sm.normalizeManagedSyncSourceWindow(currentWindow)
	isMaster := normalizedWindow == uintptr(sm.masterWindow)
	isPopup := sm.isWindowRelatedToMaster(currentWindow)

	if !isManagedSource && !isPopup {
		return
	}

	if !shouldSyncKeyboardMessage(wParam, kb.VkCode) {
		return
	}

	// Calculate if last click was in UI Zone using physical Y origin offset
	sm.lastClickYMu.RLock()
	clickScreenY := sm.lastClickY
	sm.lastClickYMu.RUnlock()

	masterHwnd := uintptr(sm.masterWindow)
	masterRenderHwnd := sm.findRenderWidgetHostHWND(masterHwnd)

	var ptClient struct{ X, Y int32 }
	sm.clientToScreen.Call(masterHwnd, uintptr(unsafe.Pointer(&ptClient)))
	var ptRender struct{ X, Y int32 }
	sm.clientToScreen.Call(masterRenderHwnd, uintptr(unsafe.Pointer(&ptRender)))

	physUIOffY := ptRender.Y - ptClient.Y
	physY := clickScreenY - ptClient.Y
	inUIZone := physY <= physUIOffY

	isZoomKey := false
	if sm.isVirtualKeyPressed(VK_CONTROL) {
		switch kb.VkCode {
		case 0xBB, 0xBD, 0x30, 0x6B, 0x6D, 0x60:
			if isManagedSource && !isPopup && (wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN) {
				cdpVerbosef("[SyncZoom] shortcut captured vk=0x%X source=%d normalized=%d mode=win32-postmessage\n", kb.VkCode, currentWindow, normalizedWindow)
				sm.broadcastKeyboardMessageToManagedWindows(wParam, kb.VkCode, normalizedWindow)
				return
			}
		}
	}
	if isManagedSource && !isPopup && isControlKey(kb.VkCode) {
		sm.broadcastKeyboardMessageToManagedWindows(wParam, kb.VkCode, normalizedWindow)
		return
	}
	isZoomKey = false
	if sm.isVirtualKeyPressed(VK_CONTROL) {
		switch kb.VkCode {
		case 0xBB, 0xBD, 0x30, 0x6B, 0x6D, 0x60: // +, -, 0, 小键盘 +, -, 0
			isZoomKey = true
			isZoomKey = false
		}
	}

	// 缩放快捷键专用处理器：拦截缩放按键，改由 CDP 执行样式缩放同步广播。
	if isZoomKey && isManagedSource && (wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN) {
		cdpVerbosef("[SyncZoom] shortcut captured vk=0x%X source=%d normalized=%d\n", kb.VkCode, currentWindow, normalizedWindow)
		var keyName string
		switch kb.VkCode {
		case 0xBB, 0x6B: // =+ 和 小键盘 +
			keyName = "+"
		case 0xBD, 0x6D: // - 和 小键盘 -
			keyName = "-"
		case 0x30, 0x60: // 0 和 小键盘 0
			keyName = "0"
		}
		if keyName != "" {
			zoom, seq, ok := sm.trackLegacyZoomShortcut(keyName)
			if ok {
				go sm.broadcastSyncZoom(zoom, seq)
			}
			return
		}
	}

	var eventType string
	if wParam == WM_KEYDOWN || wParam == WM_SYSKEYDOWN {
		eventType = "keyDown" // Use keyDown so CDP auto-generates char events for typing
	} else if wParam == WM_KEYUP || wParam == WM_SYSKEYUP {
		eventType = "keyUp"
	}

	if eventType != "" {
		modifiers := 0
		if sm.isVirtualKeyPressed(VK_SHIFT) {
			modifiers |= 8
		}
		if sm.isVirtualKeyPressed(VK_CONTROL) {
			modifiers |= 2
		}
		if sm.isVirtualKeyPressed(VK_MENU) {
			modifiers |= 1
		}

		ctrlHeld := (modifiers & 2) != 0
		if isMaster && ctrlHeld {
			switch kb.VkCode {
			case VK_C, VK_X:
				return // Skip copy/cut to avoid overwriting shared clipboard
			}
		}

		if isPopup {
			text := ""
			if eventType == "keyDown" {
				text = sm.vkCodeToChar(kb.VkCode, kb.ScanCode)
			}
			if sm.broadcastExtensionPopupKey(eventType, kb.VkCode, modifiers, text) {
				return
			}
		}

		if !inUIZone && !isPopup {
			sm.cdpMu.Lock()
			slavesCDP := make([]*cdpClient, 0, len(sm.slaveCDPs))
			for _, cdp := range sm.slaveCDPs {
				if cdp != nil && cdp.isConnected() {
					slavesCDP = append(slavesCDP, cdp)
				}
			}
			sm.cdpMu.Unlock()

			if len(slavesCDP) > 0 {
				text := ""
				if eventType == "keyDown" {
					text = sm.vkCodeToChar(kb.VkCode, kb.ScanCode)
				}

				// Fast CDP path for Webpage - run concurrently
				var wg sync.WaitGroup
				for _, cdp := range slavesCDP {
					wg.Add(1)
					go func(c *cdpClient) {
						defer wg.Done()
						_ = c.dispatchKeyEvent(eventType, kb.VkCode, modifiers, text)
					}(cdp)
				}
				wg.Wait()
				return // CDP handled it perfectly
			}
		}
	}

	// Legacy Fallback if CDP is not connected or in UI Zone
	if isPopup {
		sm.requestPopupSession(popupSessionExtension)
		sm.syncKeyToMatchingPopups(uintptr(sm.masterWindow), currentWindow, int(kb.VkCode), wParam)
	} else {
		sm.mu.RLock()
		slaves := make([]common.WindowHandle, len(sm.slaveWindows))
		copy(slaves, sm.slaveWindows)
		sm.mu.RUnlock()

		var wg sync.WaitGroup
		for _, slave := range slaves {
			wg.Add(1)
			go func(s common.WindowHandle) {
				defer wg.Done()
				var target uintptr
				if inUIZone {
					// 处于顶栏 UI 区域，按键消息接收者应直接是顶级窗口本身
					target = uintptr(s)
				} else {
					target = sm.findRenderWidgetHostHWND(uintptr(s))
				}
				sm.postMessage.Call(target, uintptr(wParam), uintptr(kb.VkCode), 0)
			}(slave)
		}
		wg.Wait()
	}
}

func (sm *SyncManager) broadcastExtensionPopupKey(eventType string, vkCode uint32, modifiers int, text string) bool {
	sm.cdpMu.Lock()
	masterCDP := sm.masterCDP
	slavesCDP := make([]*cdpClient, 0, len(sm.slaveCDPs))
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			slavesCDP = append(slavesCDP, cdp)
		}
	}
	sm.cdpMu.Unlock()
	if masterCDP == nil || !masterCDP.isConnected() || len(slavesCDP) == 0 {
		return false
	}

	targetURL := masterCDP.newestVisibleExtensionURL()
	if targetURL == "" {
		masterCDP.ensureVisibleExtensionTargetsAttached()
		targetURL = masterCDP.newestVisibleExtensionURL()
	}
	if targetURL == "" {
		return false
	}
	extensionID := extensionIDFromURL(targetURL)

	var dispatched atomic.Bool
	var wg sync.WaitGroup
	for _, cdp := range slavesCDP {
		wg.Add(1)
		go func(c *cdpClient) {
			defer wg.Done()
			if extensionID != "" {
				c.ensureExtensionTargetsAttached(extensionID)
			}
			sessionID := c.findSessionIDByURL(targetURL)
			if sessionID == "" {
				return
			}
			if err := c.dispatchKeyEventToSession(sessionID, eventType, vkCode, modifiers, text); err == nil {
				dispatched.Store(true)
			}
		}(cdp)
	}
	wg.Wait()
	return dispatched.Load()
}

func (sm *SyncManager) processMouseHook(wParam int, ms MSLLHOOKSTRUCT) {
	if sm.isPaused {
		return
	}
	// Early-filter WM_MOUSEMOVE and other irrelevant events before enqueuing.
	switch wParam {
	case WM_LBUTTONDOWN, WM_LBUTTONUP, WM_RBUTTONDOWN, WM_RBUTTONUP, WM_MOUSEWHEEL:
	default:
		return
	}

	// 终极对齐：实时拉取 PMv2 下 100% 绝对精确的 Monitor 物理原点鼠标坐标，替换逻辑 ms.Pt 污染！
	var curPt struct{ X, Y int32 }
	sm.getCursorPos.Call(uintptr(unsafe.Pointer(&curPt)))
	ms.Pt.X = curPt.X
	ms.Pt.Y = curPt.Y

	sourceWindow := sm.windowFromMousePoint(ms.Pt.X, ms.Pt.Y)
	if isMouseButtonDownMessage(wParam) || isMouseButtonUpMessage(wParam) {
		if sm.isMasterToolbarPoint(sourceWindow, ms.Pt.X, ms.Pt.Y) {
			sm.clearClickRoutes()
			return
		}
	}
	ev := hookEvent{kind: hookKindMouse, wParam: wParam, ms: ms, activeWindow: sourceWindow}
	select {
	case sm.hookEventCh <- ev:
	default:
		// Drop on full channel — better to miss one event than block the hook.
	}
}

func (sm *SyncManager) dispatchMouseEvent(wParam int, ms MSLLHOOKSTRUCT, activeWindow uintptr) {
	switch wParam {
	case WM_LBUTTONDOWN, WM_LBUTTONUP, WM_RBUTTONDOWN, WM_RBUTTONUP:
		if wParam == WM_LBUTTONDOWN {
			sm.lastClickYMu.Lock()
			sm.lastClickY = ms.Pt.Y
			sm.lastClickYMu.Unlock()
		}
		route, ok := sm.resolveClickRoute(wParam, activeWindow, ms.Pt.X, ms.Pt.Y)
		if !ok {
			return
		}
		sm.handleMouseClick(route, wParam)
	case WM_MOUSEWHEEL:
		isMaster := activeWindow == uintptr(sm.masterWindow)
		isRelated := sm.isWindowRelatedToMaster(activeWindow)
		if !isMaster && !isRelated {
			return
		}
		sm.handleMouseWheel(ms.Pt.X, ms.Pt.Y, ms.MouseData, uintptr(activeWindow))
	}
}

func (sm *SyncManager) windowFromMousePoint(x, y int32) uintptr {
	pt := uintptr(uint32(x)) | (uintptr(uint32(y)) << 32)
	hwnd, _, _ := sm.windowFromPoint.Call(pt)
	if hwnd == 0 {
		return 0
	}

	root, _, _ := sm.getAncestor.Call(hwnd, GA_ROOT)
	if root != 0 {
		return root
	}
	return hwnd
}

// eventDispatcher runs on a dedicated goroutine and processes hook events
// that were enqueued by the (intentionally minimal) hook callbacks.
func (sm *SyncManager) eventDispatcher(ch <-chan hookEvent, stop <-chan struct{}) {
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.kind {
			case hookKindKeyboard:
				sm.dispatchKeyboardEvent(ev.wParam, ev.kb, ev.activeWindow)
			case hookKindMouse:
				sm.dispatchMouseEvent(ev.wParam, ev.ms, ev.activeWindow)
			}
		case <-stop:
			return
		}
	}
}

func (sm *SyncManager) handleMouseClick(route clickRouteState, msg int) {
	slaves := sm.getSlaveWindowsSnapshot()

	// Check if popup
	if route.isPopup {
		if sm.isExtensionPopupDomSyncActive(route.sourceWindow) {
			if isMouseButtonUpMessage(msg) {
				sm.scheduleExtensionPopupPhysicalFallback(route, msg, slaves)
			}
			return
		}
		sm.requestPopupSession(popupSessionExtension)
		missingPopups := make([]common.WindowHandle, 0)
		var missingPopupsMu sync.Mutex

		var wg sync.WaitGroup
		for _, slave := range slaves {
			wg.Add(1)
			go func(s common.WindowHandle) {
				defer wg.Done()
				matchingPopup := sm.findMatchingPopup(route.sourceWindow, uintptr(sm.masterWindow), uintptr(s))
				if matchingPopup != 0 {
					sm.syncMouseToWindowWithOptions(matchingPopup, route.relX, route.relY, msg, route.reliable)
				} else {
					missingPopupsMu.Lock()
					missingPopups = append(missingPopups, s)
					missingPopupsMu.Unlock()
				}
			}(slave)
		}
		wg.Wait()

		if len(missingPopups) > 0 && isMouseButtonUpMessage(msg) {
			sm.schedulePopupMatchRetry(route.sourceWindow, route.relX, route.relY, msg, missingPopups)
		}
		return
	}

	// Standard Main Window Logic
	if route.sourceWindow == uintptr(sm.masterWindow) && isMouseButtonDownMessage(msg) && sm.masterHasCachedPopup() {
		sm.closeManagedExtensionPopupsSoon()
	}

	if route.reliable {
		sm.requestPopupSession(popupSessionDuration)
	}

	if route.tabAware != nil {
		// Tab strip actions are synchronized by the CDP target event channel.
		// Do not replay raw coordinates here; a mismatch can hit the slave tab
		// close button and close the whole Chrome window when only one tab exists.
		return
	}

	if sm.isPrivilegedViewportClick(route) {
		if isMouseButtonUpMessage(msg) {
			sm.schedulePrivilegedViewportClickFallback(route, msg, slaves)
		}
		return
	}

	if sm.isMasterViewportClick(route) {
		return
	}

	if route.reliable {
		// Keep legacy hook replay out of Chrome top UI clicks. Concrete extension
		// popups are synchronized after the master popup target appears by invoking
		// chrome.action.openPopup() in each slave extension runtime; the extension
		// list/puzzle menu itself is intentionally not replayed.
		if isMouseButtonUpMessage(msg) {
			sm.rememberExtensionTriggerClick(route)
		}
		return
	}

	var wg sync.WaitGroup
	for _, slave := range slaves {
		wg.Add(1)
		go func(s common.WindowHandle) {
			defer wg.Done()
			if route.synthetic && isMouseButtonUpMessage(msg) {
				sm.postFullClickSequence(uintptr(s), route.relX, route.relY, msg)
				return
			}
			sm.syncMouseToWindowWithOptions(uintptr(s), route.relX, route.relY, msg, route.reliable)
		}(slave)
	}
	wg.Wait()

	if route.reliable && isMouseButtonUpMessage(msg) {
		// Only retry slave windows that did not produce a matching popup.
		// Re-clicking successful popups would close extension menus again.
		sm.schedulePopupTriggerConfirmation(route, msg, slaves)
	}
}

func (sm *SyncManager) isMasterViewportClick(route clickRouteState) bool {
	if route.sourceWindow != uintptr(sm.masterWindow) || route.isPopup {
		return false
	}
	masterW, masterH, uiOff := sm.getViewportMetricsAndUIOffset(route.sourceWindow)
	if masterW <= 0 || masterH <= 0 {
		return false
	}
	x := route.relX
	y := route.relY - uiOff
	return x >= 0 && y >= 0 && x <= masterW && y <= masterH
}

func (sm *SyncManager) masterHasCachedPopup() bool {
	master := uintptr(sm.masterWindow)
	if master == 0 {
		return false
	}
	sm.popupCacheMu.RLock()
	hasCachedPopup := len(sm.popupCache[master]) > 0
	sm.popupCacheMu.RUnlock()
	if hasCachedPopup {
		return true
	}

	sm.cdpMu.Lock()
	masterCDP := sm.masterCDP
	sm.cdpMu.Unlock()
	return masterCDP != nil && masterCDP.hasExtensionPopupSession()
}

func (sm *SyncManager) closeManagedExtensionPopupsSoon() {
	sm.cdpMu.Lock()
	clients := make([]*cdpClient, 0, len(sm.slaveCDPs)+1)
	if sm.masterCDP != nil && sm.masterCDP.isConnected() {
		clients = append(clients, sm.masterCDP)
	}
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			clients = append(clients, cdp)
		}
	}
	sm.cdpMu.Unlock()
	if len(clients) == 0 {
		return
	}

	for _, delay := range []time.Duration{0, 120 * time.Millisecond} {
		delay := delay
		go func() {
			if delay > 0 {
				time.Sleep(delay)
			}
			var wg sync.WaitGroup
			for _, client := range clients {
				wg.Add(1)
				go func(cdp *cdpClient) {
					defer wg.Done()
					cdp.closeExtensionPageTargets()
				}(client)
			}
			wg.Wait()
		}()
	}
}

func (sm *SyncManager) isPrivilegedViewportClick(route clickRouteState) bool {
	if route.isPopup || route.sourceWindow != uintptr(sm.masterWindow) {
		return false
	}
	if !sm.isMasterActiveTabPrivileged() {
		return false
	}

	masterHwnd := uintptr(sm.masterWindow)
	masterRenderHwnd := sm.findRenderWidgetHostHWND(masterHwnd)
	if masterHwnd == 0 || masterRenderHwnd == 0 {
		return false
	}

	var ptClient struct{ X, Y int32 }
	sm.clientToScreen.Call(masterHwnd, uintptr(unsafe.Pointer(&ptClient)))
	var ptRender struct{ X, Y int32 }
	sm.clientToScreen.Call(masterRenderHwnd, uintptr(unsafe.Pointer(&ptRender)))

	scale := sm.getMonitorScale(masterHwnd)
	physY := int32(route.relY * scale)
	return physY > ptRender.Y-ptClient.Y
}

func (sm *SyncManager) isExtensionPopupDomSyncActive(sourcePopup uintptr) bool {
	if sourcePopup == 0 || sm.isChromeNativeDialogWindow(sourcePopup) {
		return false
	}

	sm.cdpMu.Lock()
	masterCDP := sm.masterCDP
	sm.cdpMu.Unlock()
	return masterCDP != nil && masterCDP.hasExtensionPopupSession()
}

func (sm *SyncManager) scheduleExtensionPopupPhysicalFallback(route clickRouteState, upMsg int, slaves []common.WindowHandle) {
	if !isMouseButtonUpMessage(upMsg) || len(slaves) == 0 {
		return
	}

	sm.mu.RLock()
	masterWindow := uintptr(sm.masterWindow)
	isRunning := sm.isRunning
	sm.mu.RUnlock()
	if !isRunning || masterWindow == 0 {
		return
	}

	targets := make([]uintptr, 0, len(slaves))
	for _, slave := range slaves {
		matchingPopup := sm.findMatchingPopup(route.sourceWindow, masterWindow, uintptr(slave))
		if matchingPopup != 0 {
			targets = append(targets, matchingPopup)
		}
	}
	if len(targets) == 0 {
		return
	}

	createdAt := route.createdAt
	relX, relY := route.relX, route.relY

	go func() {
		time.Sleep(privilegedDomClickFallbackDelay)
		if sm.hasRecentDomClickHandledSince(createdAt) {
			return
		}

		sm.mu.RLock()
		stillRunning := sm.isRunning && uintptr(sm.masterWindow) == masterWindow
		sm.mu.RUnlock()
		if !stillRunning {
			return
		}

		fmt.Printf("[ExtensionPopup] DOM click not captured; using popup HWND fallback targets=%d\n", len(targets))
		var wg sync.WaitGroup
		for _, target := range targets {
			wg.Add(1)
			go func(hwnd uintptr) {
				defer wg.Done()
				if !utils.IsWindowValid(hwnd) {
					return
				}
				sm.postPopupRenderClickSequence(hwnd, relX, relY, upMsg)
			}(target)
		}
		wg.Wait()
	}()
}

func (sm *SyncManager) schedulePrivilegedViewportClickFallback(route clickRouteState, upMsg int, slaves []common.WindowHandle) {
	slaveSnapshot := append([]common.WindowHandle(nil), slaves...)
	go func() {
		time.Sleep(privilegedDomClickFallbackDelay)
		if sm.hasRecentDomClickHandledSince(route.createdAt) {
			return
		}

		var wg sync.WaitGroup
		for _, slave := range slaveSnapshot {
			wg.Add(1)
			go func(s common.WindowHandle) {
				defer wg.Done()
				sm.postFullClickSequence(uintptr(s), route.relX, route.relY, upMsg)
			}(slave)
		}
		wg.Wait()
	}()
}

func (sm *SyncManager) handleMouseWheel(x, y int32, data uint32, sourceWindow uintptr) {
	// Check debounce
	if time.Since(sm.lastWheelTime) < sm.config.WheelEventThreshold {
		return
	}
	sm.lastWheelTime = time.Now()

	relX, relY := sm.calcRelativeIdx(sourceWindow, x, y)
	wheelDelta := int16(data >> 16)

	sm.mu.RLock()
	slaves := make([]common.WindowHandle, len(sm.slaveWindows))
	copy(slaves, sm.slaveWindows)
	sm.mu.RUnlock()

	// Check if source is a popup
	if sm.isWindowRelatedToMaster(sourceWindow) {
		sm.requestPopupSession(popupSessionExtension)

		var wg sync.WaitGroup
		for _, slave := range slaves {
			wg.Add(1)
			go func(s common.WindowHandle) {
				defer wg.Done()
				// Find matching popup for this slave
				matchingPopup := sm.findMatchingPopup(sourceWindow, uintptr(sm.masterWindow), uintptr(s))
				if matchingPopup != 0 {
					sm.sendWheelToWindow(matchingPopup, relX, relY, wheelDelta)
				}
			}(slave)
		}
		wg.Wait()
		return
	}

	// Normal behaviors for Master Main Window
	var wg sync.WaitGroup
	for _, slave := range slaves {
		wg.Add(1)
		go func(s common.WindowHandle) {
			defer wg.Done()
			sm.sendWheelToWindow(uintptr(s), relX, relY, wheelDelta)
		}(slave)
	}
	wg.Wait()
}

// --- Helper Functions ---

// calcRelativeIdx converts screen coordinates to client-area-relative CSS pixel
// coordinates for the given hwnd. This matches Mac's (relX, relY) which are
// "logical points relative to the window's client origin".
// The returned values include the Chrome UI area (tabs, address bar, etc.).
func (sm *SyncManager) calcRelativeIdx(hwnd uintptr, screenX, screenY int32) (float64, float64) {
	var pt struct{ X, Y int32 }
	sm.clientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))

	// PMv2 under 100% native coordinate system, pt is already absolute physical pixels.
	physPtX := float64(pt.X)
	physPtY := float64(pt.Y)

	scale := sm.getMonitorScale(hwnd)

	// Compute physical offset and divide by scale to get accurate CSS coords
	relX := (float64(screenX) - physPtX) / scale
	relY := (float64(screenY) - physPtY) / scale
	return relX, relY
}

func (sm *SyncManager) syncMouseToWindowWithOptions(hwnd uintptr, relX, relY float64, msg int, reliable bool) {
	sm.postMouseMessage(hwnd, msg, relX, relY, reliable)
}

func (sm *SyncManager) getWindowScale(hwnd uintptr) float64 {
	if sm.getDpiForWindow.Find() == nil {
		dpi, _, _ := sm.getDpiForWindow.Call(hwnd)
		if dpi != 0 {
			return float64(dpi) / 96.0
		}
	}
	return 1.0
}

func (sm *SyncManager) getMonitorScale(hwnd uintptr) float64 {
	if sm.monitorFromWindow.Find() == nil && sm.getDpiForMonitor.Find() == nil {
		monitor, _, _ := sm.monitorFromWindow.Call(hwnd, uintptr(2)) // MONITOR_DEFAULTTONEAREST
		if monitor != 0 {
			var dpiX, dpiY uint32
			ret, _, _ := sm.getDpiForMonitor.Call(
				monitor,
				uintptr(0), // MDT_EFFECTIVE_DPI
				uintptr(unsafe.Pointer(&dpiX)),
				uintptr(unsafe.Pointer(&dpiY)),
			)
			if ret == 0 && dpiX > 0 {
				return float64(dpiX) / 96.0
			}
		}
	}
	return sm.getWindowScale(hwnd)
}

// postMouseMessage dispatches a mouse event to a slave window.
// relX, relY are master client-area CSS pixel coordinates.
// Webpage clicks (inside viewport) are fully synchronized and intercepted by DOM Action.
// UI clicks (above viewport) are fallback-delivered via traditional Win32 PostMessage.
func (sm *SyncManager) postMouseMessage(hwnd uintptr, msg int, relX, relY float64, reliable bool) {
	// Obtain monitor scale factors
	var scale float64 = 1.0
	if sm.getDpiForWindow.Find() == nil {
		dpi, _, _ := sm.getDpiForWindow.Call(hwnd)
		if dpi != 0 {
			scale = float64(dpi) / 96.0
		}
	}
	physX := int32(relX * scale)
	physY := int32(relY * scale)

	if sm.isChromeNativeDialogWindow(hwnd) {
		lparam := uintptr(uint32(physX)&0xFFFF) | (uintptr(uint32(physY)&0xFFFF) << 16)
		if reliable {
			sm.postMessage.Call(hwnd, WM_MOUSEMOVE, sm.mouseMessageWParam(WM_MOUSEMOVE), lparam)
		}
		sm.postMessage.Call(hwnd, uintptr(msg), sm.mouseMessageWParam(msg), lparam)
		return
	}

	// Obtain true physical UI height by comparing top-level Client Y and render widget Y
	masterHwnd := uintptr(sm.masterWindow)
	masterRenderHwnd := sm.findRenderWidgetHostHWND(masterHwnd)

	var ptClient struct{ X, Y int32 }
	sm.clientToScreen.Call(masterHwnd, uintptr(unsafe.Pointer(&ptClient)))
	var ptRender struct{ X, Y int32 }
	sm.clientToScreen.Call(masterRenderHwnd, uintptr(unsafe.Pointer(&ptRender)))

	physUIOffY := ptRender.Y - ptClient.Y

	// Viewport (Webpage) click guard:
	// If the click falls inside the viewport (below the true physical UI offset),
	// completely block physical PostMessage. DOM action sync will execute it natively.
	// 特权页面豁免：因为 JS 无法执行，必须放行物理点击！
	// Native dialog 豁免：DOM action 被阻塞，必须放行物理点击让 hook 同步生效！
	var target uintptr
	if physY > physUIOffY {
		// 检查 native dialog：如果有 native dialog，DOM action 被阻塞，必须放行 hook
		sm.mu.RLock()
		hasNativeDialog := sm.domActionsBlocked
		sm.mu.RUnlock()
		if hasNativeDialog {
			target = sm.findRenderWidgetHostHWND(hwnd)
		} else if !sm.isMasterActiveTabPrivileged() {
			return
		} else {
			// 特权页面的网页内容区，目标窗口应为渲染子窗口
			target = sm.findRenderWidgetHostHWND(hwnd)
		}
	} else {
		// UI 区域的点击，其接收者应该是顶级窗口 hwnd 本身。
		target = hwnd
	}

	// Fallback UI area traditional PostMessage simulation
	// Re-map to render widget child if needed
	finalX, finalY := physX, physY
	if target != hwnd {
		var pt struct{ X, Y int32 }
		pt.X, pt.Y = physX, physY
		sm.clientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
		sm.screenToClient.Call(target, uintptr(unsafe.Pointer(&pt)))
		finalX, finalY = pt.X, pt.Y
	}

	lparam := uintptr(uint32(finalX)&0xFFFF) | (uintptr(uint32(finalY)&0xFFFF) << 16)
	if reliable {
		sm.postMessage.Call(target, WM_MOUSEMOVE, sm.mouseMessageWParam(WM_MOUSEMOVE), lparam)
	}
	sm.postMessage.Call(target, uintptr(msg), sm.mouseMessageWParam(msg), lparam)
}

func (sm *SyncManager) postFullClickSequence(hwnd uintptr, relX, relY float64, upMsg int) {
	downMsg, ok := pairedMouseDownMessage(upMsg)
	if !ok {
		return
	}

	sm.postMouseMessage(hwnd, downMsg, relX, relY, true)
	time.Sleep(popupReliableRetryGap)
	sm.postMouseMessage(hwnd, upMsg, relX, relY, true)
}

func (sm *SyncManager) postPopupRenderClickSequence(hwnd uintptr, relX, relY float64, upMsg int) {
	downMsg, ok := pairedMouseDownMessage(upMsg)
	if !ok {
		return
	}

	sm.postPopupRenderMouseMessage(hwnd, downMsg, relX, relY)
	time.Sleep(popupReliableRetryGap)
	sm.postPopupRenderMouseMessage(hwnd, upMsg, relX, relY)
}

func (sm *SyncManager) postPopupRenderMouseMessage(hwnd uintptr, msg int, relX, relY float64) {
	if hwnd == 0 {
		return
	}

	scale := sm.getMonitorScale(hwnd)
	physX := int32(relX * scale)
	physY := int32(relY * scale)

	var pt struct{ X, Y int32 }
	pt.X, pt.Y = physX, physY
	sm.clientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt)))

	var target uintptr
	screenPoint := uintptr(uint32(pt.X)) | (uintptr(uint32(pt.Y)) << 32)
	if candidate, _, _ := sm.windowFromPoint.Call(screenPoint); candidate != 0 {
		root, _, _ := sm.getAncestor.Call(candidate, GA_ROOT)
		className, err := utils.GetClassName(candidate)
		if err == nil && root == hwnd && className == "Chrome_RenderWidgetHostHWND" {
			target = candidate
		}
	}
	if target == 0 {
		target = sm.findRenderWidgetHostHWND(hwnd)
	}

	finalX, finalY := physX, physY
	if target != hwnd {
		sm.screenToClient.Call(target, uintptr(unsafe.Pointer(&pt)))
		finalX, finalY = pt.X, pt.Y
	}

	lparam := uintptr(uint32(finalX)&0xFFFF) | (uintptr(uint32(finalY)&0xFFFF) << 16)
	sm.postMessage.Call(target, WM_MOUSEMOVE, sm.mouseMessageWParam(WM_MOUSEMOVE), lparam)
	sm.postMessage.Call(target, uintptr(msg), sm.mouseMessageWParam(msg), lparam)
}

func (sm *SyncManager) mouseMessageWParam(msg int) uintptr {
	var state uintptr
	if sm.isVirtualKeyPressed(VK_SHIFT) {
		state |= MK_SHIFT
	}
	if sm.isVirtualKeyPressed(VK_CONTROL) {
		state |= MK_CONTROL
	}

	switch msg {
	case WM_LBUTTONDOWN:
		state |= MK_LBUTTON
	case WM_RBUTTONDOWN:
		state |= MK_RBUTTON
	}

	return state
}

func (sm *SyncManager) isVirtualKeyPressed(vk uint32) bool {
	switch vk {
	case VK_CONTROL:
		if sm.ctrlPressed.Load() {
			return true
		}
	case VK_SHIFT:
		if sm.shiftPressed.Load() {
			return true
		}
	case VK_MENU:
		if sm.altPressed.Load() {
			return true
		}
	}
	ret, _, _ := sm.getAsyncKeyState.Call(uintptr(vk))
	return uint16(ret)&0x8000 != 0
}

func (sm *SyncManager) normalizeManagedSyncSourceWindow(hwnd uintptr) (uintptr, bool) {
	if hwnd == 0 {
		return 0, false
	}

	candidates := []uintptr{hwnd}
	if sm.getAncestor != nil {
		root, _, _ := sm.getAncestor.Call(hwnd, GA_ROOT)
		if root != 0 && root != hwnd {
			candidates = append(candidates, root)
		}
	}

	sm.mu.RLock()
	master := uintptr(sm.masterWindow)
	slaves := make([]common.WindowHandle, len(sm.slaveWindows))
	copy(slaves, sm.slaveWindows)
	sm.mu.RUnlock()

	for _, candidate := range candidates {
		if candidate == master {
			return master, true
		}
		for _, slave := range slaves {
			if candidate == uintptr(slave) {
				return candidate, true
			}
		}
	}

	if sm.isWindowRelatedToMaster(hwnd) {
		return hwnd, true
	}
	return 0, false
}

func (sm *SyncManager) getSlaveWindowsSnapshot() []common.WindowHandle {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	slaves := make([]common.WindowHandle, len(sm.slaveWindows))
	copy(slaves, sm.slaveWindows)
	return slaves
}

func (sm *SyncManager) clearClickRoutes() {
	sm.clickRouteMu.Lock()
	sm.leftClickRoute = clickRouteState{}
	sm.rightClickRoute = clickRouteState{}
	sm.clickRouteMu.Unlock()
}

func isMouseButtonDownMessage(msg int) bool {
	return msg == WM_LBUTTONDOWN || msg == WM_RBUTTONDOWN
}

func isMouseButtonUpMessage(msg int) bool {
	return msg == WM_LBUTTONUP || msg == WM_RBUTTONUP
}

func isClickRouteFresh(route clickRouteState) bool {
	return route.sourceWindow != 0 && !route.createdAt.IsZero() && time.Since(route.createdAt) <= clickRouteTTL
}

func (sm *SyncManager) storeClickRoute(msg int, route clickRouteState) {
	sm.clickRouteMu.Lock()
	defer sm.clickRouteMu.Unlock()

	switch msg {
	case WM_LBUTTONDOWN:
		sm.leftClickRoute = route
	case WM_RBUTTONDOWN:
		sm.rightClickRoute = route
	}
}

func (sm *SyncManager) takeClickRoute(msg int, activeWindow uintptr) (clickRouteState, bool) {
	sm.clickRouteMu.Lock()
	defer sm.clickRouteMu.Unlock()

	var route clickRouteState
	switch msg {
	case WM_LBUTTONUP:
		route = sm.leftClickRoute
	case WM_RBUTTONUP:
		route = sm.rightClickRoute
	default:
		return clickRouteState{}, false
	}

	if !isClickRouteFresh(route) || route.sourceWindow != activeWindow {
		switch msg {
		case WM_LBUTTONUP:
			sm.leftClickRoute = clickRouteState{}
		case WM_RBUTTONUP:
			sm.rightClickRoute = clickRouteState{}
		}
		return clickRouteState{}, false
	}

	switch msg {
	case WM_LBUTTONUP:
		sm.leftClickRoute = clickRouteState{}
	case WM_RBUTTONUP:
		sm.rightClickRoute = clickRouteState{}
	}

	return route, true
}

func (sm *SyncManager) buildClickRoute(sourceWindow uintptr, x, y int32, msg int) (clickRouteState, bool) {
	isMaster := sourceWindow == uintptr(sm.masterWindow)
	isPopup := sm.isWindowRelatedToMaster(sourceWindow)
	if !isMaster && !isPopup {
		return clickRouteState{}, false
	}

	relX, relY := sm.calcRelativeIdx(sourceWindow, x, y)
	route := clickRouteState{
		sourceWindow: sourceWindow,
		screenX:      x,
		screenY:      y,
		relX:         relX,
		relY:         relY,
		isPopup:      isPopup,
		reliable:     isPopup || sm.isLikelyPopupTriggerClick(sourceWindow, x, y, msg),
		createdAt:    time.Now(),
	}

	if candidate := sm.newTabAwareClickState(sourceWindow, x, y, msg); candidate != nil {
		route.tabAware = candidate
	}

	if route.reliable && isMaster && isMouseButtonDownMessage(msg) {
		if counts, ok := sm.capturePopupBaseline(sourceWindow); ok {
			route.popupBaselineKnown = true
			route.popupBaselineMasterCount = counts[sourceWindow]
			route.popupBaselineCounts = counts
		}
	}

	return route, true
}

func (sm *SyncManager) resolveClickRoute(msg int, activeWindow uintptr, x, y int32) (clickRouteState, bool) {
	if isMouseButtonUpMessage(msg) {
		if route, ok := sm.takeClickRoute(msg, activeWindow); ok {
			return route, true
		}
		// DOWN was dropped (channel was full) or this UP arrived without a paired
		// DOWN. Don't trust the live foreground window — by the time the dispatcher
		// runs, focus may have shifted to a popup. Re-route to the master window
		// using the UP coordinates so the slave still receives a click instead of
		// nothing. Popup-internal UPs without a stored DOWN are extremely rare and
		// not worth the round-trip through buildClickRoute's classification.
		sm.mu.RLock()
		master := uintptr(sm.masterWindow)
		sm.mu.RUnlock()
		if master == 0 || activeWindow != master {
			return clickRouteState{}, false
		}
		if sm.isMasterToolbarPoint(master, x, y) {
			return clickRouteState{}, false
		}
		relX, relY := sm.calcRelativeIdx(master, x, y)
		return clickRouteState{
			sourceWindow: master,
			screenX:      x,
			screenY:      y,
			relX:         relX,
			relY:         relY,
			isPopup:      false,
			reliable:     false,
			synthetic:    true,
			createdAt:    time.Now(),
		}, true
	}

	route, ok := sm.buildClickRoute(activeWindow, x, y, msg)
	if !ok {
		return clickRouteState{}, false
	}

	if isMouseButtonDownMessage(msg) {
		sm.storeClickRoute(msg, route)
	}

	return route, true
}

func pairedMouseDownMessage(upMsg int) (int, bool) {
	switch upMsg {
	case WM_LBUTTONUP:
		return WM_LBUTTONDOWN, true
	case WM_RBUTTONUP:
		return WM_RBUTTONDOWN, true
	default:
		return 0, false
	}
}

func (sm *SyncManager) capturePopupBaseline(masterWindow uintptr) (map[uintptr]int, bool) {
	if masterWindow == 0 {
		return nil, false
	}

	sm.requestPopupSession(popupSessionDuration)
	sm.refreshPopupCache(true)

	slaves := sm.getSlaveWindowsSnapshot()
	counts := make(map[uintptr]int, len(slaves)+1)

	sm.popupCacheMu.RLock()
	counts[masterWindow] = len(sm.popupCache[masterWindow])
	for _, slave := range slaves {
		counts[uintptr(slave)] = len(sm.popupCache[uintptr(slave)])
	}
	sm.popupCacheMu.RUnlock()
	return counts, true
}

func (sm *SyncManager) schedulePopupTriggerConfirmation(route clickRouteState, upMsg int, slaves []common.WindowHandle) {
	if !isMouseButtonUpMessage(upMsg) || len(slaves) == 0 {
		return
	}

	sm.mu.RLock()
	masterWindow := uintptr(sm.masterWindow)
	isRunning := sm.isRunning
	sm.mu.RUnlock()
	if !isRunning || masterWindow == 0 {
		return
	}

	slaveSnapshot := append([]common.WindowHandle(nil), slaves...)

	go func() {
		masterRetrySent := false
		allowMasterRetry := route.popupBaselineKnown && route.popupBaselineMasterCount == 0

		for attempt := 0; attempt < popupReliableConfirmTries; attempt++ {
			if attempt == 0 {
				time.Sleep(popupReliableConfirmDelay)
			} else {
				time.Sleep(popupReliableConfirmStep)
			}

			sm.mu.RLock()
			if !sm.isRunning || uintptr(sm.masterWindow) != masterWindow {
				sm.mu.RUnlock()
				return
			}
			sm.mu.RUnlock()

			sm.requestPopupSession(popupSessionExtension)
			sm.refreshPopupCache(true)

			sm.popupCacheMu.RLock()
			masterPopupCount := len(sm.popupCache[masterWindow])
			missingTargets := make([]uintptr, 0)
			anySlavePopup := false
			for _, slave := range slaveSnapshot {
				slaveHWND := uintptr(slave)
				slavePopupCount := len(sm.popupCache[slaveHWND])
				slaveBaselineCount := 0
				if route.popupBaselineCounts != nil {
					slaveBaselineCount = route.popupBaselineCounts[slaveHWND]
				}
				if slavePopupCount > slaveBaselineCount {
					anySlavePopup = true
				}
				if masterPopupCount > 0 && slavePopupCount < masterPopupCount {
					missingTargets = append(missingTargets, slaveHWND)
				}
			}
			sm.popupCacheMu.RUnlock()

			if masterPopupCount == 0 {
				if allowMasterRetry && anySlavePopup && !masterRetrySent {
					fmt.Printf("[PopupConfirm] master missing while slaves opened; retrying master click\n")
					sm.postFullClickSequence(masterWindow, route.relX, route.relY, upMsg)
					masterRetrySent = true
				}
				continue
			}
			if len(missingTargets) == 0 {
				return
			}

			fmt.Printf("[PopupConfirm] retrying %d missing slave popup click(s)\n", len(missingTargets))
			for _, target := range missingTargets {
				sm.postFullClickSequence(target, route.relX, route.relY, upMsg)
			}
			return
		}
	}()
}

func (sm *SyncManager) newTabAwareClickState(sourceWindow uintptr, screenX int32, screenY int32, msg int) *tabAwareClickState {
	if !sm.isLikelyTabToolbarClick(sourceWindow, screenX, screenY, msg) {
		return nil
	}

	if !sm.hasActiveOrStartingMasterCDP() {
		return nil
	}

	return &tabAwareClickState{}
}

func (sm *SyncManager) schedulePopupMatchRetry(sourcePopup uintptr, rx, ry float64, upMsg int, missingSlaves []common.WindowHandle) {
	if !isMouseButtonUpMessage(upMsg) || len(missingSlaves) == 0 {
		return
	}

	sm.mu.RLock()
	masterWindow := uintptr(sm.masterWindow)
	isRunning := sm.isRunning
	sm.mu.RUnlock()
	if !isRunning || masterWindow == 0 {
		return
	}

	slaveSnapshot := append([]common.WindowHandle(nil), missingSlaves...)

	go func() {
		pending := append([]common.WindowHandle(nil), slaveSnapshot...)
		for attempt := 0; attempt < popupReliableConfirmTries && len(pending) > 0; attempt++ {
			if attempt == 0 {
				time.Sleep(popupReliableConfirmDelay / 2)
			} else {
				time.Sleep(popupReliableConfirmStep)
			}

			sm.mu.RLock()
			if !sm.isRunning || uintptr(sm.masterWindow) != masterWindow {
				sm.mu.RUnlock()
				return
			}
			sm.mu.RUnlock()

			sm.requestPopupSession(popupSessionExtension)
			sm.refreshPopupCache(true)

			nextPending := make([]common.WindowHandle, 0, len(pending))
			for _, slave := range pending {
				matchingPopup := sm.findMatchingPopup(sourcePopup, masterWindow, uintptr(slave))
				if matchingPopup == 0 {
					nextPending = append(nextPending, slave)
					continue
				}
				sm.postFullClickSequence(matchingPopup, rx, ry, upMsg)
			}
			pending = nextPending
		}
	}()
}

func (sm *SyncManager) sendWheelToWindow(hwnd uintptr, relX, relY float64, delta int16) {
	// Obtain monitor scale factors
	var scale float64 = 1.0
	if sm.getDpiForWindow.Find() == nil {
		dpi, _, _ := sm.getDpiForWindow.Call(hwnd)
		if dpi != 0 {
			scale = float64(dpi) / 96.0
		}
	}
	physY := int32(relY * scale)

	// Obtain true physical UI height by comparing top-level Client Y and render widget Y
	masterHwnd := uintptr(sm.masterWindow)
	masterRenderHwnd := sm.findRenderWidgetHostHWND(masterHwnd)

	var ptClient struct{ X, Y int32 }
	sm.clientToScreen.Call(masterHwnd, uintptr(unsafe.Pointer(&ptClient)))
	var ptRender struct{ X, Y int32 }
	sm.clientToScreen.Call(masterRenderHwnd, uintptr(unsafe.Pointer(&ptRender)))

	physUIOffY := ptRender.Y - ptClient.Y

	// 完全放行物理滚轮投射，保证网页与 UI 的绝对流畅和一致性
	var target uintptr
	if physY > physUIOffY {
		// 网页内容区（包括 Ctrl 滚轮缩放和特权页/普通页滚动），路由给网页渲染子窗口本身
		target = sm.findRenderWidgetHostHWND(hwnd)
	} else {
		// 物理 UI 区域，路由给顶级窗口本身
		target = hwnd
	}

	physX := int32(relX * scale)

	var screenPt struct{ X, Y int32 }
	screenPt.X, screenPt.Y = physX, physY
	sm.clientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&screenPt)))

	wparam := uintptr(delta) << 16
	wheelLparam := uintptr(screenPt.Y)<<16 | uintptr(screenPt.X)

	var movePt struct{ X, Y int32 }
	movePt.X, movePt.Y = screenPt.X, screenPt.Y
	// 无论 target 是否为顶级窗口，均需转换至该目标窗口之客户区坐标
	sm.screenToClient.Call(target, uintptr(unsafe.Pointer(&movePt)))

	moveLparam := uintptr(uint32(movePt.Y)&0xFFFF)<<16 | uintptr(uint32(movePt.X)&0xFFFF)
	sm.postMessage.Call(target, WM_MOUSEMOVE, 0, moveLparam)
	sm.postMessage.Call(target, WM_MOUSEWHEEL, wparam, wheelLparam)
}

func shouldSyncKeyboardMessage(message int, vkCode uint32) bool {
	switch message {
	case WM_KEYDOWN, WM_SYSKEYDOWN:
		return true
	case WM_KEYUP, WM_SYSKEYUP:
		// Only sync modifier key releases. Non-modifier KEYUP is not synced
		// because Chrome triggers input on KEYDOWN, and syncing KEYUP for normal
		// keys causes double-input (aa instead of a) in slave windows.
		return isModifierKey(vkCode)
	default:
		return false
	}
}

func isModifierKey(vkCode uint32) bool {
	switch vkCode {
	case VK_SHIFT, VK_CONTROL, VK_MENU, VK_LWIN, VK_RWIN,
		VK_LSHIFT, VK_RSHIFT, VK_LCONTROL, VK_RCONTROL, VK_LMENU, VK_RMENU:
		return true
	default:
		return false
	}
}

func isControlKey(vkCode uint32) bool {
	switch vkCode {
	case VK_CONTROL, VK_LCONTROL, VK_RCONTROL:
		return true
	default:
		return false
	}
}

func isChromeZoomShortcutKey(vkCode uint32) bool {
	switch vkCode {
	case 0xBB, 0xBD, 0x30, 0x6B, 0x6D, 0x60:
		return true
	default:
		return false
	}
}

func (sm *SyncManager) handleZoomShortcutInHook(wParam int, kb KBDLLHOOKSTRUCT) bool {
	if wParam != WM_KEYDOWN && wParam != WM_SYSKEYDOWN {
		return false
	}
	if !sm.ctrlPressed.Load() || !isChromeZoomShortcutKey(kb.VkCode) {
		return false
	}

	activeWindow, _, _ := sm.getForegroundWindow.Call()
	normalizedWindow, ok := sm.normalizeManagedSyncSourceWindow(uintptr(activeWindow))
	if !ok {
		return false
	}

	cdpVerbosef("[SyncZoom] shortcut captured vk=0x%X source=%d normalized=%d mode=win32-hook-sequence\n", kb.VkCode, activeWindow, normalizedWindow)
	sm.broadcastKeyboardMessageToManagedWindows(WM_KEYDOWN, VK_CONTROL, 0)
	sm.broadcastKeyboardMessageToManagedWindows(WM_KEYDOWN, kb.VkCode, 0)
	sm.broadcastKeyboardMessageToManagedWindows(WM_KEYUP, kb.VkCode, 0)
	return true
}

func (sm *SyncManager) broadcastKeyboardMessageToManagedWindows(wParam int, vkCode uint32, exclude uintptr) {
	sm.mu.RLock()
	targets := make([]uintptr, 0, len(sm.slaveWindows)+1)
	if sm.masterWindow != 0 {
		targets = append(targets, uintptr(sm.masterWindow))
	}
	for _, slave := range sm.slaveWindows {
		targets = append(targets, uintptr(slave))
	}
	sm.mu.RUnlock()

	var wg sync.WaitGroup
	for _, target := range targets {
		if target == 0 || target == exclude {
			continue
		}
		wg.Add(1)
		go func(hwnd uintptr) {
			defer wg.Done()
			sm.postMessage.Call(hwnd, uintptr(wParam), uintptr(vkCode), 0)
		}(target)
	}
	wg.Wait()
}

func getRelatedCacheTTL(related bool) time.Duration {
	if related {
		return relatedPositiveCacheTTL
	}
	return relatedNegativeCacheTTL
}

func isRelatedCacheEntryFresh(entry relatedWindowCacheEntry) bool {
	if entry.checkedAt.IsZero() {
		return false
	}
	return time.Since(entry.checkedAt) <= getRelatedCacheTTL(entry.related)
}

func (sm *SyncManager) storeRelatedWindowCache(hwnd uintptr, related bool, checkedAt time.Time) {
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}

	sm.relatedWindowCacheMu.Lock()
	sm.relatedWindowCache[hwnd] = relatedWindowCacheEntry{
		related:   related,
		checkedAt: checkedAt,
	}
	sm.relatedWindowCacheMu.Unlock()
}

func (sm *SyncManager) requestPopupSession(duration time.Duration) {
	if duration <= 0 {
		duration = popupSessionDuration
	}

	sm.popupSessionMu.Lock()
	until := time.Now().Add(duration)
	if until.After(sm.popupSessionUntil) {
		sm.popupSessionUntil = until
	}
	requestCh := sm.popupRefreshRequest
	sm.popupSessionMu.Unlock()

	if requestCh != nil {
		select {
		case requestCh <- struct{}{}:
		default:
		}
	}
}

func (sm *SyncManager) refreshPopupCacheBurst(delays []time.Duration) {
	for _, delay := range delays {
		time.Sleep(delay)
		sm.refreshPopupCache(true)
	}
}

func (sm *SyncManager) popupSessionActive() bool {
	sm.popupSessionMu.RLock()
	defer sm.popupSessionMu.RUnlock()
	return !sm.popupSessionUntil.IsZero() && time.Now().Before(sm.popupSessionUntil)
}

func (sm *SyncManager) nextPopupRefreshDelay() time.Duration {
	if sm.popupSessionActive() {
		return popupActiveRefreshInterval
	}
	return popupBaseRefreshInterval
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

func (sm *SyncManager) refreshPopupCache(force bool) {
	// TryLock: if a refresh is already running, skip rather than block the caller.
	// This is critical for the event dispatcher goroutine — blocking it for
	// hundreds of milliseconds (EnumWindows) starves all incoming hook events.
	if !sm.popupRefreshing.CompareAndSwap(false, true) {
		return
	}
	sm.popupRefreshMu.Lock()
	defer func() {
		sm.popupRefreshMu.Unlock()
		sm.popupRefreshing.Store(false)
	}()

	now := time.Now()

	sm.popupSessionMu.RLock()
	lastRefresh := sm.lastPopupRefresh
	sm.popupSessionMu.RUnlock()

	minInterval := popupBaseRefreshInterval
	if sm.popupSessionActive() {
		minInterval = popupActiveRefreshInterval
	}

	if !force && !lastRefresh.IsZero() && now.Sub(lastRefresh) < minInterval {
		return
	}

	sm.mu.RLock()
	if !sm.isRunning || sm.masterWindow == 0 {
		sm.mu.RUnlock()
		return
	}
	master := sm.masterWindow
	slaves := make([]common.WindowHandle, len(sm.slaveWindows))
	copy(slaves, sm.slaveWindows)
	sm.mu.RUnlock()

	windowHandles := make([]uintptr, 0, len(slaves)+1)
	windowHandles = append(windowHandles, uintptr(master))
	for _, slave := range slaves {
		windowHandles = append(windowHandles, uintptr(slave))
	}

	batchedPopups := utils.GetChromePopupsBatch(windowHandles)
	newPopupCache := make(map[uintptr][]uintptr, len(slaves)+1)
	masterPops := batchedPopups[uintptr(master)]
	newPopupCache[uintptr(master)] = masterPops

	for _, slave := range slaves {
		newPopupCache[uintptr(slave)] = batchedPopups[uintptr(slave)]
	}

	newRelated := make(map[uintptr]relatedWindowCacheEntry, len(masterPops)+1)
	for _, popup := range masterPops {
		newRelated[popup] = relatedWindowCacheEntry{
			related:   true,
			checkedAt: now,
		}
	}

	sm.relatedWindowCacheMu.RLock()
	for hwnd, entry := range sm.relatedWindowCache {
		if entry.related || !isRelatedCacheEntryFresh(entry) {
			continue
		}
		if _, exists := newRelated[hwnd]; !exists {
			newRelated[hwnd] = entry
		}
	}
	sm.relatedWindowCacheMu.RUnlock()

	sm.popupCacheMu.Lock()
	sm.popupCache = newPopupCache
	sm.popupCacheMu.Unlock()

	sm.relatedWindowCacheMu.Lock()
	sm.relatedWindowCache = newRelated
	sm.relatedWindowCacheMu.Unlock()

	sm.arrangePopups(master, slaves, newPopupCache)

	sm.popupSessionMu.Lock()
	sm.lastPopupRefresh = now
	sm.popupSessionMu.Unlock()
}

type popupArrangeOwner struct {
	hwnd   uintptr
	number int
	order  int
	left   int32
	top    int32
	center int32
	popups []uintptr
}

func popupArrangementSame(a, b []uintptr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func popupInsertAfter(insertAfter int) uintptr {
	switch insertAfter {
	case utils.HWND_TOPMOST:
		return ^uintptr(0)
	case utils.HWND_NOTOPMOST:
		return ^uintptr(1)
	default:
		return uintptr(insertAfter)
	}
}

func (sm *SyncManager) setPopupZOrder(popups []uintptr, insertAfter int) {
	insertAfterHandle := popupInsertAfter(insertAfter)
	flags := uintptr(utils.SWP_NOMOVE | utils.SWP_NOSIZE | utils.SWP_NOACTIVATE)
	for _, popup := range popups {
		if popup == 0 {
			continue
		}
		sm.setWindowPos.Call(popup, insertAfterHandle, 0, 0, 0, 0, flags)
	}
}

func (sm *SyncManager) resetArrangedPopups() {
	if len(sm.popupArrangeSet) == 0 {
		return
	}
	sm.setPopupZOrder(sm.popupArrangeSet, utils.HWND_NOTOPMOST)
	sm.popupArrangeSet = nil
}

func (sm *SyncManager) popupWindowNumbers(master common.WindowHandle, slaves []common.WindowHandle) map[uintptr]int {
	numbers := make(map[uintptr]int, len(slaves)+1)
	numbers[uintptr(master)] = 1
	for i, slave := range slaves {
		numbers[uintptr(slave)] = i + 2
	}

	sm.windowMetadataMu.RLock()
	if sm.masterMetadata != nil {
		handle := uintptr(sm.masterMetadata.Handle)
		if handle == 0 {
			handle = sm.masterMetadata.HWND
		}
		if handle != 0 && sm.masterMetadata.Number > 0 {
			numbers[handle] = sm.masterMetadata.Number
		}
	}
	for handle, info := range sm.slaveMetadata {
		if info.Number > 0 {
			numbers[handle] = info.Number
		}
		infoHandle := uintptr(info.Handle)
		if infoHandle == 0 {
			infoHandle = info.HWND
		}
		if infoHandle != 0 && info.Number > 0 {
			numbers[infoHandle] = info.Number
		}
	}
	sm.windowMetadataMu.RUnlock()

	return numbers
}

func (sm *SyncManager) arrangePopups(master common.WindowHandle, slaves []common.WindowHandle, popupCache map[uintptr][]uintptr) {
	numbers := sm.popupWindowNumbers(master, slaves)
	owners := make([]popupArrangeOwner, 0, len(slaves)+1)
	ownerHandles := make([]common.WindowHandle, 0, len(slaves)+1)
	ownerHandles = append(ownerHandles, master)
	ownerHandles = append(ownerHandles, slaves...)

	for order, owner := range ownerHandles {
		ownerHWND := uintptr(owner)
		popups := popupCache[ownerHWND]
		if ownerHWND == 0 || len(popups) == 0 {
			continue
		}
		rect, err := utils.GetWindowRect(ownerHWND)
		if err != nil {
			continue
		}
		number := numbers[ownerHWND]
		if number == 0 {
			number = order + 1
		}
		owners = append(owners, popupArrangeOwner{
			hwnd:   ownerHWND,
			number: number,
			order:  order,
			left:   rect.Left,
			top:    rect.Top,
			center: (rect.Left + rect.Right) / 2,
			popups: append([]uintptr(nil), popups...),
		})
	}

	if len(owners) == 0 {
		sm.resetArrangedPopups()
		return
	}

	sort.SliceStable(owners, func(i, j int) bool {
		if owners[i].number == owners[j].number {
			return owners[i].order > owners[j].order
		}
		return owners[i].number > owners[j].number
	})

	plan := make([]uintptr, 0)
	for _, owner := range owners {
		plan = append(plan, owner.popups...)
	}
	if len(plan) == 0 {
		sm.resetArrangedPopups()
		return
	}
	if popupArrangementSame(sm.popupArrangeSet, plan) {
		return
	}

	sm.resetArrangedPopups()
	sm.offsetUpwardPopups(owners)
	sm.setPopupZOrder(plan, utils.HWND_TOPMOST)
	sm.popupArrangeSet = append(sm.popupArrangeSet[:0], plan...)
}

func (sm *SyncManager) offsetUpwardPopups(owners []popupArrangeOwner) {
	if len(owners) == 0 {
		return
	}

	var centerSum int64
	for _, owner := range owners {
		centerSum += int64(owner.center)
	}
	screenPivot := int32(centerSum / int64(len(owners)))

	for _, owner := range owners {
		rowRank := 0
		for _, candidate := range owners {
			if candidate.hwnd == owner.hwnd {
				continue
			}
			if candidate.top < owner.top && absInt32(candidate.left-owner.left) <= popupColumnGroupingGap {
				rowRank++
			}
		}

		direction := int32(1)
		if owner.center > screenPivot {
			direction = -1
		}
		shift := int32(popupSideShift + rowRank*popupSideShiftStep)

		for _, popup := range owner.popups {
			popupRect, err := utils.GetWindowRect(popup)
			if err != nil {
				continue
			}
			if popupRect.Top >= owner.top-int32(popupUpwardDetectMargin) {
				continue
			}

			sm.setWindowPos.Call(
				popup,
				uintptr(0),
				uintptr(popupRect.Left+direction*shift),
				uintptr(popupRect.Top),
				0,
				0,
				uintptr(utils.SWP_NOSIZE|utils.SWP_NOZORDER|utils.SWP_NOACTIVATE),
			)
		}
	}
}

func (sm *SyncManager) isLikelyPopupTriggerClick(sourceWindow uintptr, _ int32, screenY int32, msg int) bool {
	if sourceWindow != uintptr(sm.masterWindow) {
		return false
	}

	if msg != WM_LBUTTONDOWN && msg != WM_LBUTTONUP && msg != WM_RBUTTONDOWN && msg != WM_RBUTTONUP {
		return false
	}

	rect, err := utils.GetWindowRect(sourceWindow)
	if err != nil {
		return false
	}

	return int(screenY) >= int(rect.Top) && int(screenY) <= int(rect.Top)+popupToolbarProbeHeight
}

func absInt32(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

func (sm *SyncManager) isMasterToolbarPoint(sourceWindow uintptr, _ int32, screenY int32) bool {
	if sourceWindow != uintptr(sm.masterWindow) {
		return false
	}

	rect, err := utils.GetWindowRect(sourceWindow)
	if err != nil {
		return false
	}

	return int(screenY) >= int(rect.Top) && int(screenY) <= int(rect.Top)+popupToolbarProbeHeight
}

func (sm *SyncManager) isLikelyTabToolbarClick(sourceWindow uintptr, screenX int32, screenY int32, msg int) bool {
	if sourceWindow != uintptr(sm.masterWindow) {
		return false
	}

	if msg != WM_LBUTTONDOWN {
		return false
	}

	rect, err := utils.GetWindowRect(sourceWindow)
	if err != nil {
		return false
	}

	localX := int(screenX - rect.Left)
	width := int(rect.Right - rect.Left)
	if localX < 0 || localX >= width {
		return false
	}

	if localX >= width-tabAwareRightControlMargin {
		return false
	}

	return int(screenY) >= int(rect.Top) && int(screenY) <= int(rect.Top)+tabAwareProbeHeight
}

func (sm *SyncManager) isWindowRelatedToMaster(hwnd uintptr) bool {
	sm.mu.RLock()
	masterWindow := sm.masterWindow
	masterPID := sm.masterPID
	sm.mu.RUnlock()

	if masterWindow == 0 {
		return false
	}

	if sm.isManagedTopLevelWindow(hwnd) {
		return false
	}

	// Check cache (fastest)
	sm.relatedWindowCacheMu.RLock()
	cached, hit := sm.relatedWindowCache[hwnd]
	sm.relatedWindowCacheMu.RUnlock()
	if hit && isRelatedCacheEntryFresh(cached) {
		return cached.related
	}

	// Check against cached popups (fast)
	sm.popupCacheMu.RLock()
	popups, ok := sm.popupCache[uintptr(masterWindow)]
	sm.popupCacheMu.RUnlock()

	if ok {
		for _, p := range popups {
			if p == hwnd {
				sm.storeRelatedWindowCache(hwnd, true, time.Now())
				return true
			}
		}
	}

	// Fallback: Check PID and Class (Slow Syscall)
	if masterPID == 0 {
		var err error
		masterPID, err = utils.GetWindowProcessID(uintptr(masterWindow))
		if err != nil {
			return false
		}

		sm.mu.Lock()
		if sm.masterWindow == masterWindow {
			sm.masterPID = masterPID
		}
		sm.mu.Unlock()
	}

	currentPID, err := utils.GetWindowProcessID(hwnd)
	if err != nil {
		return false
	}

	if masterPID != currentPID {
		sm.storeRelatedWindowCache(hwnd, false, time.Now())
		return false
	}

	// Same process, check class
	currentClass, err := utils.GetClassName(hwnd)
	if err != nil {
		return false
	}

	isRelated := false
	if strings.Contains(currentClass, "Chrome") ||
		strings.Contains(currentClass, "Widget") ||
		strings.Contains(currentClass, "Popup") {
		isRelated = true
	}

	sm.storeRelatedWindowCache(hwnd, isRelated, time.Now())

	return isRelated
}

func (sm *SyncManager) isManagedTopLevelWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}

	sm.mu.RLock()
	if hwnd == uintptr(sm.masterWindow) {
		sm.mu.RUnlock()
		return true
	}
	for _, slave := range sm.slaveWindows {
		if hwnd == uintptr(slave) {
			sm.mu.RUnlock()
			return true
		}
	}
	sm.mu.RUnlock()

	return false
}

func (sm *SyncManager) findMatchingPopup(currentPopup uintptr, masterWindow uintptr, targetWindow uintptr) uintptr {
	retry := func() ([]uintptr, []uintptr) {
		sm.requestPopupSession(popupSessionExtension)
		sm.refreshPopupCache(true)
		sm.popupCacheMu.RLock()
		defer sm.popupCacheMu.RUnlock()
		return sm.popupCache[targetWindow], sm.popupCache[masterWindow]
	}

	sm.popupCacheMu.RLock()
	targetPopups := sm.popupCache[targetWindow]
	masterPopups := sm.popupCache[masterWindow]
	sm.popupCacheMu.RUnlock()

	if len(targetPopups) == 0 {
		targetPopups, masterPopups = retry()
	}

	if len(targetPopups) == 0 {
		return 0
	}

	// Find index in master list
	currentIndex := -1
	for i, popup := range masterPopups {
		if popup == currentPopup {
			currentIndex = i
			break
		}
	}

	if currentIndex == -1 {
		targetPopups, masterPopups = retry()

		currentIndex = -1
		for i, popup := range masterPopups {
			if popup == currentPopup {
				currentIndex = i
				break
			}
		}
	}

	if currentIndex >= len(targetPopups) {
		targetPopups, masterPopups = retry()

		currentIndex = -1
		for i, popup := range masterPopups {
			if popup == currentPopup {
				currentIndex = i
				break
			}
		}
	}

	// Map to target list
	if currentIndex >= 0 && currentIndex < len(targetPopups) {
		return targetPopups[currentIndex]
	}

	if matchedPopup := sm.findMatchingPopupByGeometry(currentPopup, masterWindow, targetWindow, masterPopups, targetPopups); matchedPopup != 0 {
		return matchedPopup
	}

	// Fallback: First popup
	if len(targetPopups) > 0 {
		return targetPopups[0]
	}

	return 0
}

func (sm *SyncManager) findMatchingPopupByGeometry(currentPopup uintptr, masterWindow uintptr, targetWindow uintptr, masterPopups []uintptr, targetPopups []uintptr) uintptr {
	if currentPopup == 0 || masterWindow == 0 || targetWindow == 0 || len(targetPopups) == 0 {
		return 0
	}

	currentRect, err := utils.GetWindowRect(currentPopup)
	if err != nil {
		return 0
	}

	masterRect, err := utils.GetWindowRect(masterWindow)
	if err != nil {
		return 0
	}

	targetRect, err := utils.GetWindowRect(targetWindow)
	if err != nil {
		return 0
	}

	baseWidth := currentRect.Right - currentRect.Left
	baseHeight := currentRect.Bottom - currentRect.Top
	baseLeftOffset := currentRect.Left - masterRect.Left
	baseTopOffset := currentRect.Top - masterRect.Top

	bestPopup := uintptr(0)
	bestScore := int64(-1)

	for _, candidate := range targetPopups {
		if candidate == 0 {
			continue
		}

		candidateRect, err := utils.GetWindowRect(candidate)
		if err != nil {
			continue
		}

		width := candidateRect.Right - candidateRect.Left
		height := candidateRect.Bottom - candidateRect.Top
		leftOffset := candidateRect.Left - targetRect.Left
		topOffset := candidateRect.Top - targetRect.Top

		score := int64(absInt32(width-baseWidth))*2 +
			int64(absInt32(height-baseHeight))*2 +
			int64(absInt32(leftOffset-baseLeftOffset))*4 +
			int64(absInt32(topOffset-baseTopOffset))*4

		if bestScore == -1 || score < bestScore {
			bestScore = score
			bestPopup = candidate
		}
	}

	if bestPopup != 0 {
		return bestPopup
	}

	if len(masterPopups) > 0 && len(targetPopups) > 0 {
		return targetPopups[0]
	}

	return 0
}

func (sm *SyncManager) syncKeyToMatchingPopups(masterWindow uintptr, currentPopup uintptr, keyCode int, message int) {
	sm.mu.RLock()
	slaves := make([]common.WindowHandle, len(sm.slaveWindows))
	copy(slaves, sm.slaveWindows)
	sm.mu.RUnlock()

	for _, slave := range slaves {
		matchingPopup := sm.findMatchingPopup(currentPopup, masterWindow, uintptr(slave))
		if matchingPopup != 0 {
			sm.postMessage.Call(matchingPopup, uintptr(message), uintptr(keyCode), 0)
		}
	}
}

func (sm *SyncManager) initializePopupCache() {
	defer func() {
		if r := recover(); r != nil {
			// 从 initializePopupCache 的严重崩溃(Panic)中极速静默恢复
			_ = r
		}
	}()

	stopCh := sm.stopChan
	requestCh := sm.popupRefreshRequest
	timer := time.NewTimer(sm.nextPopupRefreshDelay())
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			if !sm.isRunning {
				return
			}
			sm.refreshPopupCache(false)
			resetTimer(timer, sm.nextPopupRefreshDelay())
		case <-requestCh:
			sm.refreshPopupCache(true)
			resetTimer(timer, sm.nextPopupRefreshDelay())
		case <-stopCh:
			return
		}
	}
}

func (sm *SyncManager) findRenderWidgetHostHWND(hwnd uintptr) uintptr {
	// Allocate the EnumChildWindows callback exactly once for the lifetime of
	// the process. The lParam is used to pass a *uintptr result pointer so the
	// single callback can be reused across concurrent calls safely.
	enumChildForRenderWidgetOnce.Do(func() {
		enumChildForRenderWidget = windows.NewCallback(func(child, lParam uintptr) uintptr {
			buf := make([]uint16, 64)
			ret, _, _ := procGetClassNameW.Call(child, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if ret == 0 {
				return 1 // continue
			}
			cls := windows.UTF16ToString(buf[:ret])
			if cls == "Chrome_RenderWidgetHostHWND" {
				// Write the result through the pointer passed as lParam.
				*(*uintptr)(unsafe.Pointer(lParam)) = child //nolint:unsafeptr // intentional: lParam is a *uintptr
				return 0                                    // stop enumeration
			}
			return 1 // continue
		})
	})

	var found uintptr
	sm.enumChildWindows.Call(hwnd, enumChildForRenderWidget, uintptr(unsafe.Pointer(&found)))

	if found != 0 {
		return found
	}
	return hwnd
}

type ViewportMetrics struct {
	Width  float64
	Height float64
}

func (sm *SyncManager) getViewportMetricsAndUIOffset(hwnd uintptr) (float64, float64, float64) {
	sm.cdpMu.Lock()
	var cdp *cdpClient
	if hwnd == uintptr(sm.masterWindow) {
		cdp = sm.masterCDP
	} else {
		cdp = sm.slaveCDPs[hwnd]
	}
	sm.cdpMu.Unlock()

	uiOff := 80.0
	var w, h float64 = 0, 0

	sm.uiOffsetMu.RLock()
	if val, ok := sm.uiOffsets[hwnd]; ok {
		uiOff = val
	}
	if val, ok := sm.viewportMetrics[hwnd]; ok {
		w = val.Width
		h = val.Height
	}
	sm.uiOffsetMu.RUnlock()

	if cdp != nil && cdp.isConnected() && (w == 0 || h == 0) {
		w, h = cdp.GetViewportMetrics()
		if w > 0 && h > 0 {
			// Calculate UI offset from client area height (NOT outerHeight).
			// outerHeight includes the OS title bar, but client area doesn't.
			// Since our relY coords are relative to client area, we must use
			// client area height to compute the correct UI offset.
			var rect struct{ Left, Top, Right, Bottom int32 }
			sm.getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))

			scale := sm.getWindowScale(hwnd)
			clientHeightCSS := float64(rect.Bottom-rect.Top) / scale
			uiOff = clientHeightCSS - h
			if uiOff < 0 {
				uiOff = 0
			}

			sm.uiOffsetMu.Lock()
			sm.viewportMetrics[hwnd] = ViewportMetrics{Width: w, Height: h}
			sm.uiOffsets[hwnd] = uiOff
			sm.uiOffsetMu.Unlock()
		}
	}

	// 终极物理兜底：如果 CDP 获取失败或尚未连接导致 w, h 仍然为 0，我们使用物理 Client 区域计算出视口 metrics。
	// 这可以 100% 避免 mapToViewportCSS 因为 metrics 为 0 返回 false，导致网页内部点击被误杀阻断！
	if w == 0 || h == 0 {
		var rect struct{ Left, Top, Right, Bottom int32 }
		sm.getClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))

		scale := sm.getWindowScale(hwnd)
		clientWidthCSS := float64(rect.Right-rect.Left) / scale
		clientHeightCSS := float64(rect.Bottom-rect.Top) / scale

		if clientWidthCSS > 0 && clientHeightCSS > 0 {
			w = clientWidthCSS
			h = clientHeightCSS - uiOff
			if h < 0 {
				h = 0
			}
		}
	}

	return w, h, uiOff
}

func (sm *SyncManager) isMasterActiveTabPrivileged() bool {
	sm.cdpMu.Lock()
	cdp := sm.masterCDP
	sm.cdpMu.Unlock()
	if cdp == nil {
		return true // 若 CDP client 尚未就绪，默认采用物理投射安全降级
	}
	u := cdp.getActiveURL()
	if isChromeNewTabURL(u) {
		return false
	}
	return isPrivilegedURL(u)
}

func isChromeNewTabURL(u string) bool {
	return u == "chrome://newtab/" || u == "chrome://new-tab-page/"
}

func isPrivilegedURL(u string) bool {
	if u == "" {
		return false
	}
	return strings.HasPrefix(u, "chrome://") ||
		strings.HasPrefix(u, "edge://") ||
		strings.HasPrefix(u, "brave://") ||
		strings.HasPrefix(u, "opera://") ||
		strings.HasPrefix(u, "chrome-extension://") ||
		strings.HasPrefix(u, "devtools://") ||
		strings.HasPrefix(u, "about:")
}

func isBrowserInternalURL(u string) bool {
	return strings.HasPrefix(u, "chrome://") ||
		strings.HasPrefix(u, "edge://") ||
		strings.HasPrefix(u, "brave://") ||
		strings.HasPrefix(u, "opera://")
}

func (sm *SyncManager) trackLegacyZoomShortcut(keyName string) (float64, int64, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch strings.ToLower(keyName) {
	case "+", "=":
		if sm.syncZoomIndex < len(chromeZoomSteps)-1 {
			sm.syncZoomIndex++
		}
	case "-":
		if sm.syncZoomIndex > 0 {
			sm.syncZoomIndex--
		}
	case "0":
		sm.syncZoomIndex = defaultChromeZoomIndex
	default:
		return 0, 0, false
	}

	sm.syncZoomSeq++
	sm.syncZoomTarget = chromeZoomSteps[sm.syncZoomIndex]
	sm.syncZoomActive = keyName != "0"
	return sm.syncZoomTarget, sm.syncZoomSeq, true
}

func (sm *SyncManager) currentSyncZoomState() (float64, int64, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.syncZoomTarget, sm.syncZoomSeq, sm.syncZoomActive && sm.isRunning && !sm.isPaused
}

func (sm *SyncManager) broadcastSyncZoom(zoom float64, seq int64) {
	sm.mu.RLock()
	if !sm.isRunning || sm.isPaused {
		sm.mu.RUnlock()
		return
	}

	targetCDPs := make(map[uintptr]*cdpClient, len(sm.slaveCDPs)+1)
	if sm.masterCDP != nil && sm.masterCDP.isConnected() {
		targetCDPs[uintptr(sm.masterWindow)] = sm.masterCDP
	}
	for hwnd, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected() {
			targetCDPs[hwnd] = cdp
		}
	}
	sm.mu.RUnlock()

	cdpVerbosef("[SyncZoom] target=%.0f%% seq=%d windows=%d\n", zoom*100, seq, len(targetCDPs))
	for hwnd, cdp := range targetCDPs {
		go func(h uintptr, c *cdpClient) {
			sm.applySyncZoomToPID(h, c, zoom, seq, "shortcut")
		}(hwnd, cdp)
	}
}

func (sm *SyncManager) reapplyCurrentZoomToPID(hwnd uintptr, cdp *cdpClient, reason string) {
	zoom, seq, ok := sm.currentSyncZoomState()
	if !ok || cdp == nil {
		return
	}

	go func() {
		time.Sleep(120 * time.Millisecond)
		sm.applySyncZoomToPID(hwnd, cdp, zoom, seq, reason)
	}()
}

func (sm *SyncManager) applySyncZoomToPID(hwnd uintptr, cdp *cdpClient, zoom float64, seq int64, reason string) {
	mode, err := cdp.SetPageZoom(zoom)
	if err != nil {
		fmt.Printf("[SyncZoom] hwnd=%d seq=%d target=%.0f%% reason=%s failed: %v\n", hwnd, seq, zoom*100, reason, err)
		return
	}
	cdpVerbosef("[SyncZoom] hwnd=%d seq=%d target=%.0f%% applied=%.0f%% mode=%s reason=%s\n",
		hwnd, seq, zoom*100, zoom*100, mode, reason)
}

func isChromeDialogClass(className string) bool {
	return className == "Chrome_Dialog" ||
		className == "Chrome_WidgetWin_1" ||
		className == "Chrome_WidgetWin_0"
}

func hasNativeDialogTitleKeyword(title string) bool {
	lowerTitle := strings.ToLower(title)
	keywords := []string{
		"添加扩展程序", "确认添加", "删除", "移除",
		"add extension", "add to chrome", "remove extension", "remove", "delete",
		"alert", "confirm", "prompt", "javascript",
	}
	for _, keyword := range keywords {
		if strings.Contains(lowerTitle, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func classifyChromeNativeDialog(className string, style, exStyle uintptr, title string, width, height int32) (bool, bool, bool) {
	area := int(width) * int(height)
	if area <= 0 {
		return false, false, false
	}

	hasModalFrame := (exStyle&wsExDlgModalFrame) != 0 || (style&dsModalFrame) != 0
	hasDlgFrame := (style & wsDlgFrame) != 0
	isChild := (style & wsChild) != 0
	isSmallDialog := area < nativeDialogMaxArea &&
		width < nativeDialogMaxWidth &&
		height < nativeDialogMaxHeight

	hasNativeKeyword := title != "" && hasNativeDialogTitleKeyword(title)
	isFramedChromeWidgetDialog := className != "Chrome_Dialog" &&
		(hasModalFrame || hasDlgFrame) &&
		!isChild &&
		isSmallDialog &&
		(title == "" || hasNativeKeyword)

	isDialog := className == "Chrome_Dialog" || isFramedChromeWidgetDialog
	if !isDialog && isSmallDialog && hasNativeKeyword {
		isDialog = true
	}
	return isDialog, hasModalFrame, hasDlgFrame
}

func (sm *SyncManager) isChromeNativeDialogWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if sm.isManagedTopLevelWindow(hwnd) {
		return false
	}

	className, err := utils.GetClassName(hwnd)
	if err != nil || !isChromeDialogClass(className) {
		return false
	}

	visible, _, _ := sm.isWindowVisible.Call(hwnd)
	if visible == 0 {
		return false
	}

	rect, err := utils.GetWindowRect(hwnd)
	if err != nil {
		return false
	}

	style, _, _ := sm.getWindowLong.Call(hwnd, gwlStyleIndex)
	exStyle, _, _ := sm.getWindowLong.Call(hwnd, gwlExStyleIndex)

	title, err := utils.GetWindowText(hwnd)
	if err != nil {
		title = ""
	}

	isDialog, _, _ := classifyChromeNativeDialog(className, style, exStyle, title, rect.Right-rect.Left, rect.Bottom-rect.Top)
	return isDialog
}

// nativeDialogEnumData 用于 EnumWindows 回调的数据传递
type nativeDialogEnumData struct {
	masterPID                uint32
	masterWindow             uintptr
	hasDialog                bool
	isWindowVisible          *windows.LazyProc
	getWindowTextW           *windows.LazyProc
	getWindowThreadProcessId *windows.LazyProc
	getWindowLong            *windows.LazyProc
}

// nativeDialogEnumCallback 是 EnumWindows 的回调函数
// 返回 true 继续枚举，返回 false 停止枚举
func nativeDialogEnumCallback(hwnd windows.HWND, lParam uintptr) uintptr {
	data := (*nativeDialogEnumData)(unsafe.Pointer(lParam))

	// 获取窗口类名
	className := make([]uint16, 256)
	classLen, _ := windows.GetClassName(hwnd, &className[0], 256)
	classNameStr := windows.UTF16ToString(className[:classLen])

	// Chrome 对话框可能是 Chrome_WidgetWin_1、Chrome_WidgetWin_0 或 Chrome_Dialog 类名
	if !isChromeDialogClass(classNameStr) {
		return 1 // 继续枚举
	}

	// 检查窗口是否可见
	visible, _, _ := data.isWindowVisible.Call(uintptr(hwnd))
	if visible == 0 {
		return 1 // 继续枚举
	}

	// 获取窗口所属的进程 PID
	var pid uint32
	data.getWindowThreadProcessId.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
	if uintptr(hwnd) == data.masterWindow {
		return 1
	}
	if pid != data.masterPID {
		return 1 // 不是 master 进程的窗口，继续枚举
	}

	// 获取窗口大小
	rect, err := utils.GetWindowRect(uintptr(hwnd))
	if err != nil {
		return 1
	}
	width := rect.Right - rect.Left
	height := rect.Bottom - rect.Top
	area := int(width) * int(height)

	// 获取窗口样式
	style, _, _ := data.getWindowLong.Call(uintptr(hwnd), gwlStyleIndex)
	exStyle, _, _ := data.getWindowLong.Call(uintptr(hwnd), gwlExStyleIndex)

	// 获取窗口标题（用于日志和关键词检测）
	title := make([]uint16, 512)
	titleLen, _, _ := data.getWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&title[0])), 512)
	titleStr := windows.UTF16ToString(title[:titleLen])

	isDialog, hasModalFrame, hasDlgFrame := classifyChromeNativeDialog(classNameStr, style, exStyle, titleStr, width, height)

	if isDialog {
		data.hasDialog = true
		cdpVerbosef("[NativeDialog] Detected dialog: hwnd=%d class=%s area=%d modal=%v dlgframe=%v title=%s\n",
			hwnd, classNameStr, area, hasModalFrame, hasDlgFrame, titleStr)
		return 0
	}

	return 1 // 继续枚举
}

// isNativeDialogVisible 检测 Chrome 主窗口是否有 native 对话框
// native 对话框（如添加扩展程序确认框）没有窗口标题，可以通过枚举 Chrome_WidgetWin_1 窗口检测
func (sm *SyncManager) isNativeDialogVisible() bool {
	sm.mu.RLock()
	masterPID := sm.masterPID
	masterWindow := uintptr(sm.masterWindow)
	sm.mu.RUnlock()

	if masterPID == 0 {
		return false
	}

	data := &nativeDialogEnumData{
		masterPID:                uint32(masterPID),
		masterWindow:             masterWindow,
		hasDialog:                false,
		isWindowVisible:          sm.isWindowVisible,
		getWindowLong:            sm.getWindowLong,
		getWindowTextW:           sm.getWindowTextW,
		getWindowThreadProcessId: sm.getWindowThreadProcessId,
	}

	// 调用 EnumWindows 枚举所有顶层窗口
	ret, _, _ := sm.enumWindows.Call(
		sm.nativeDialogEnumCB,
		uintptr(unsafe.Pointer(data)),
	)
	_ = ret

	return data.hasDialog
}

// nativeDialogDetector 后台检测 native 对话框状态
// 当检测到 native 对话框时，设置阻塞标志，阻止 DOM action 回调执行
func (sm *SyncManager) nativeDialogDetector() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopChan:
			return
		case <-ticker.C:
			if sm.isNativeDialogVisible() {
				sm.blockDOMActions("native dialog detected")
			} else {
				sm.unblockDOMActions("native dialog closed")
			}
		}
	}
}

// blockDOMActions 阻止 DOM action 回调执行
func (sm *SyncManager) blockDOMActions(reason string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.domActionsBlocked {
		return // 已经阻塞
	}

	sm.domActionsBlocked = true
	fmt.Printf("[NativeDialog] Blocking DOM actions: %s\n", reason)
}

// unblockDOMActions 解除 DOM action 阻塞
func (sm *SyncManager) unblockDOMActions(reason string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.domActionsBlocked {
		return // 没有阻塞
	}

	sm.domActionsBlocked = false
	fmt.Printf("[NativeDialog] Unblocking DOM actions: %s\n", reason)
}

//go:build darwin

package darwin

import (
	"chromemanager/platform/common"
	"chromemanager/utils"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var globalSyncManager *SyncManager
var syncManagerOnce sync.Once
var syncManagerDebugLogs = strings.EqualFold(os.Getenv("CHROMEMANAGER_DEBUG"), "1") || strings.EqualFold(os.Getenv("CHROMEMANAGER_DEBUG"), "true")

func syncDebugf(format string, args ...interface{}) {
	if syncManagerDebugLogs {
		log.Printf(format, args...)
	}
}

// getClipboardContent reads the system clipboard using pbpaste
func getClipboardContent() string {
	cmd := exec.Command("pbpaste")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// Constants for Coordinate Mapping
const (
	// ChromeTopUIHeight approximates the height of TitleBar + AddressBar + Bookmarks
	// This is heuristic. Ideally we'd measure it dynamicall via CDP 'window.outerHeight - window.innerHeight'
	DefaultChromeTopUIHeight = 85

	kCGEventKeyDown         = 10
	kCGEventKeyUp           = 11
	kCGEventFlagMaskShift   = 0x020000
	kCGEventFlagMaskControl = 0x040000
	kCGEventFlagMaskOption  = 0x080000
	kCGEventFlagMaskCommand = 0x100000

	defaultChromeZoomIndex = 7 // 100%

	popupUpwardDetectMargin = 12
	popupSideShift          = 36
	popupSideShiftStep      = 12
	popupColumnGroupingGap  = 48

	popupNativeSettlePoll     = 90 * time.Millisecond
	popupNativeSettleTimeout  = 3500 * time.Millisecond
	popupPointerRouteLifetime = 2 * time.Second
)

var chromeZoomSteps = []float64{0.25, 0.33, 0.50, 0.67, 0.75, 0.80, 0.90, 1.00, 1.10, 1.25, 1.50, 1.75, 2.00, 2.50, 3.00, 4.00, 5.00}

// NativeSyncEvent defines the internal message structure for native events
type NativeSyncEvent struct {
	Type            string
	X               float64
	Y               float64
	Zoom            float64
	ZoomSeq         int64
	ScreenScale     float64
	Value           int
	KeyCode         int
	Modifiers       int
	Chars           string
	MasterTargetID  string
	MasterSessionID string
	ExtensionURL    string
	ExtensionInput  extensionEditableState
	DOMClick        DOMClickAction
	PopupWidth      float64
	PopupHeight     float64
	// ExtensionWindowRoute means X/Y came from a standalone extension Chrome window,
	// not from the extension popup's page viewport.
	ExtensionWindowRoute bool
	// ExtensionContentRoute means X/Y are local to an Accessibility web area.
	ExtensionContentRoute bool
}

type viewportMetricsSnapshot struct {
	Width     float64
	Height    float64
	UpdatedAt time.Time
}

type nativeExtensionPointerRoute struct {
	extensionURL string
	popupRect    common.Rect
	windowRoute  bool
	contentRoute bool
	createdAt    time.Time
}

const nativeScrollCoalesceInterval = 8 * time.Millisecond

// SyncManager macOS implementation
type SyncManager struct {
	mu sync.RWMutex

	// Master/Slave
	masterWindow common.WindowHandle
	slaveWindows []common.WindowHandle

	// Event Queues
	slaveChans []chan NativeSyncEvent
	masterChan chan NativeSyncEvent

	// State
	isRunning bool
	isPaused  bool
	stopChan  chan struct{}
	config    common.SyncConfig

	// CDP Connections (Hybrid Mac Only)
	masterCDP *CDPClient
	slaveCDPs map[int]*CDPClient

	// Target Mapping (Master TargetID -> (Slave PID -> Slave TargetID))
	targetMapMutex sync.RWMutex
	targetMap      map[string]map[int]string

	programmaticTargetMu       sync.Mutex
	programmaticMasterURL      string
	programmaticMasterTargetID string
	programmaticMasterActive   bool

	onProgress func(string)

	// Sync State
	uiOffsets       map[int]float64
	viewportMetrics map[int]viewportMetricsSnapshot
	syncZoomIndex   int
	syncZoomSeq     int64
	syncZoomTarget  float64
	syncZoomActive  bool
	zoomApplyMu     sync.Mutex

	// Cache
	rectCache      map[int]common.Rect
	mainWindowRefs map[int]uintptr
	cacheMutex     sync.RWMutex

	// Modifier State Tracking
	lastModifiers int

	// Last Click Position
	lastClickX int
	lastClickY int

	keyboardExtensionURL string

	// Viewport Interaction Tracking For Sync Deduplication
	lastViewportClickMutex    sync.Mutex
	lastViewportClickTime     time.Time
	lastViewportClickTarget   string
	lastViewportClickReplayed bool

	tabActivationMu         sync.Mutex
	lastTabActivationTarget string
	lastTabActivationAt     time.Time

	popupArrangeMu       sync.Mutex
	popupArrangeBaseLeft map[uintptr]int

	popupLifecycleMu   sync.Mutex
	popupOpenSeq       uint64
	masterPopupTargets map[string]string
	masterPopupByExt   map[string]string

	pointerRouteMu sync.Mutex
	leftRoute      nativeExtensionPointerRoute
	rightRoute     nativeExtensionPointerRoute

	scrollMu          sync.Mutex
	scrollPending     []NativeSyncEvent
	scrollDispatching bool
	scrollRunID       uint64
	syncRunID         uint64
}

// Ensure SyncManager implements common.SyncProvider
var _ common.SyncProvider = (*SyncManager)(nil)

func NewSyncManager() *SyncManager {
	syncManagerOnce.Do(func() {
		globalSyncManager = &SyncManager{
			config: common.SyncConfig{
				MouseMoveInterval:   10 * time.Millisecond,
				KeyboardInterval:    5 * time.Millisecond,
				WheelEventThreshold: 10 * time.Millisecond,
			},
			rectCache:            make(map[int]common.Rect),
			mainWindowRefs:       make(map[int]uintptr),
			slaveCDPs:            make(map[int]*CDPClient),
			targetMap:            make(map[string]map[int]string),
			uiOffsets:            make(map[int]float64),
			viewportMetrics:      make(map[int]viewportMetricsSnapshot),
			syncZoomIndex:        defaultChromeZoomIndex,
			popupArrangeBaseLeft: make(map[uintptr]int),
			masterPopupTargets:   make(map[string]string),
			masterPopupByExt:     make(map[string]string),
		}
	})
	return globalSyncManager
}

func (sm *SyncManager) BeginProgrammaticMasterTarget(url string) {
	sm.programmaticTargetMu.Lock()
	sm.programmaticMasterURL = url
	sm.programmaticMasterTargetID = ""
	sm.programmaticMasterActive = url != ""
	sm.programmaticTargetMu.Unlock()
}

func (sm *SyncManager) ConfirmProgrammaticMasterTarget(targetID string) {
	sm.programmaticTargetMu.Lock()
	if sm.programmaticMasterActive && targetID != "" {
		sm.programmaticMasterTargetID = targetID
	}
	sm.programmaticTargetMu.Unlock()
}

func (sm *SyncManager) shouldSuppressProgrammaticMasterTarget(targetID, url string) bool {
	sm.programmaticTargetMu.Lock()
	defer sm.programmaticTargetMu.Unlock()
	if !sm.programmaticMasterActive {
		return false
	}
	if targetID == "" {
		return sameComparableURL(sm.programmaticMasterURL, url)
	}
	if sm.programmaticMasterTargetID != "" {
		return sm.programmaticMasterTargetID == targetID
	}
	if !sameComparableURL(sm.programmaticMasterURL, url) {
		return false
	}
	sm.programmaticMasterTargetID = targetID
	return true
}

func (sm *SyncManager) finishProgrammaticMasterTarget(targetID, url string) {
	sm.programmaticTargetMu.Lock()
	defer sm.programmaticTargetMu.Unlock()
	if !sm.programmaticMasterActive {
		return
	}
	if sm.programmaticMasterTargetID != "" {
		if sm.programmaticMasterTargetID != targetID {
			return
		}
	} else if targetID == "" || !sameComparableURL(sm.programmaticMasterURL, url) {
		return
	}
	sm.programmaticMasterURL = ""
	sm.programmaticMasterTargetID = ""
	sm.programmaticMasterActive = false
}

func (sm *SyncManager) CancelProgrammaticMasterTarget() {
	sm.programmaticTargetMu.Lock()
	sm.programmaticMasterURL = ""
	sm.programmaticMasterTargetID = ""
	sm.programmaticMasterActive = false
	sm.programmaticTargetMu.Unlock()
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
	sm.syncZoomActive = sm.syncZoomIndex != defaultChromeZoomIndex
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

	targetCDPs := make(map[int]*CDPClient, len(sm.slaveCDPs)+1)
	if sm.masterCDP != nil && sm.masterCDP.isConnected {
		targetCDPs[int(sm.masterWindow)] = sm.masterCDP
	}
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			targetCDPs[pid] = cdp
		}
	}
	sm.mu.RUnlock()

	syncDebugf("[SyncZoom] target=%.0f%% seq=%d windows=%d", zoom*100, seq, len(targetCDPs))
	for pid, cdp := range targetCDPs {
		go func(pid int, cdp *CDPClient) {
			sm.applySyncZoomToPID(pid, cdp, zoom, seq, "shortcut")
		}(pid, cdp)
	}
}

func (sm *SyncManager) reapplyCurrentZoomToPID(pid int, cdp *CDPClient, reason string) {
	zoom, seq, ok := sm.currentSyncZoomState()
	if !ok || cdp == nil {
		return
	}

	go func() {
		time.Sleep(120 * time.Millisecond)
		sm.applySyncZoomToPID(pid, cdp, zoom, seq, reason)
	}()
}

func (sm *SyncManager) applySyncZoomToPID(pid int, cdp *CDPClient, zoom float64, seq int64, reason string) {
	if pid <= 0 || cdp == nil {
		return
	}

	sm.zoomApplyMu.Lock()
	defer sm.zoomApplyMu.Unlock()

	current, currentErr := cdp.GetChromeNativeZoom()
	if currentErr == nil && requiresNativeChromeZoomMenu(current.URL) {
		if err := cdp.resetCDPPageZoomFallback(current.URL); err != nil {
			log.Printf("[SyncZoom] pid=%d seq=%d target=%.0f%% mode=native-menu reset failed: %v", pid, seq, zoom*100, err)
			return
		}
		actions := nativeChromeZoomMenuActions(current.Zoom, zoom)
		for _, direction := range actions {
			if err := NativePressChromeZoomMenu(pid, direction); err != nil {
				log.Printf("[SyncZoom] pid=%d seq=%d target=%.0f%% mode=native-menu failed: %v", pid, seq, zoom*100, err)
				return
			}
		}
		syncDebugf("[SyncZoom] pid=%d seq=%d target=%.0f%% applied=%.0f%% mode=native-menu actions=%d reason=%s url=%s",
			pid, seq, zoom*100, zoom*100, len(actions), reason, current.URL)
		return
	}

	result, err := cdp.SetChromeNativeZoom(zoom)
	if err != nil {
		log.Printf("[SyncZoom] pid=%d seq=%d target=%.0f%% mode=zoom failed: %v", pid, seq, zoom*100, err)
		return
	}
	syncDebugf("[SyncZoom] pid=%d seq=%d target=%.0f%% applied=%.0f%% mode=%s tab=%d reason=%s url=%s",
		pid, seq, zoom*100, result.Zoom*100, result.Mode, result.TabID, reason, result.URL)
}

func requiresNativeChromeZoomMenu(rawURL string) bool {
	u := strings.ToLower(strings.TrimSpace(rawURL))
	return strings.HasPrefix(u, "chrome://") ||
		strings.HasPrefix(u, "chrome-untrusted://") ||
		strings.HasPrefix(u, "devtools://") ||
		strings.HasPrefix(u, "about:")
}

func nativeChromeZoomMenuActions(current, target float64) []int {
	currentIndex := nearestChromeNativeZoomIndex(current)
	targetIndex := nearestChromeNativeZoomIndex(target)
	if currentIndex == targetIndex {
		return nil
	}
	if targetIndex == defaultChromeZoomIndex {
		return []int{0}
	}
	direction := 1
	if targetIndex < currentIndex {
		direction = -1
	}
	actions := make([]int, 0, absInt(targetIndex-currentIndex))
	for currentIndex != targetIndex {
		actions = append(actions, direction)
		currentIndex += direction
	}
	return actions
}

func isChromeNativeZoomShortcut(evtType, modifiers int, keyName string) bool {
	if (evtType != kCGEventKeyDown && evtType != kCGEventKeyUp) || (modifiers&kCGEventFlagMaskCommand) == 0 {
		return false
	}
	switch strings.ToLower(keyName) {
	case "+", "=", "-", "0":
		return true
	default:
		return false
	}
}

func chromeNativeZoomDirection(keyName string) (int, bool) {
	switch strings.ToLower(keyName) {
	case "+", "=":
		return 1, true
	case "-":
		return -1, true
	case "0":
		return 0, true
	default:
		return 0, false
	}
}

func (sm *SyncManager) broadcastNativeChromeZoomShortcut(direction int) {
	sm.mu.RLock()
	if !sm.isRunning || sm.isPaused {
		sm.mu.RUnlock()
		return
	}
	type zoomWindow struct {
		pid    int
		handle uintptr
	}
	targets := make([]zoomWindow, 0, len(sm.slaveCDPs)+1)
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			targets = append(targets, zoomWindow{pid: pid, handle: sm.mainWindowRefs[pid]})
		}
	}
	masterPID := int(sm.masterWindow)
	masterHandle := sm.mainWindowRefs[masterPID]
	sm.mu.RUnlock()
	sort.Slice(targets, func(i, j int) bool { return targets[i].pid < targets[j].pid })
	targets = append(targets, zoomWindow{pid: masterPID, handle: masterHandle})

	for _, target := range targets {
		if target.handle != 0 {
			NativeActivateAndRaiseWindow(target.handle)
		} else if !ActivateProcess(target.pid) {
			log.Printf("[SyncZoom] pid=%d mode=foreground-menu direction=%d activate failed", target.pid, direction)
			continue
		}
		if err := NativePressChromeZoomMenu(target.pid, direction); err != nil {
			log.Printf("[SyncZoom] pid=%d mode=foreground-menu direction=%d failed: %v", target.pid, direction, err)
			continue
		}
		syncDebugf("[SyncZoom] pid=%d mode=foreground-menu direction=%d applied", target.pid, direction)
	}
}

// GetGlobalSyncManager returns the singleton SyncManager for direct access (used for batch input)
func GetGlobalSyncManager() *SyncManager {
	return globalSyncManager
}

// OpenInputMonitoringSettings 打开系统设置中的输入监听页面
func OpenInputMonitoringSettings() {
	cmd := exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent")
	cmd.Run()
}

// touchViewportClick records which master target produced a viewport click.
func (sm *SyncManager) touchViewportClick(targetID string, replayed bool) {
	sm.lastViewportClickMutex.Lock()
	sm.lastViewportClickTime = time.Now()
	sm.lastViewportClickTarget = targetID
	sm.lastViewportClickReplayed = replayed
	sm.lastViewportClickMutex.Unlock()
}

func (sm *SyncManager) markViewportClickReplayed(targetID string) {
	sm.lastViewportClickMutex.Lock()
	if sm.lastViewportClickTarget == targetID && time.Since(sm.lastViewportClickTime) < 2*time.Second {
		sm.lastViewportClickReplayed = true
	}
	sm.lastViewportClickMutex.Unlock()
}

func (sm *SyncManager) hasRecentReplayedViewportClick(targetID string) bool {
	if targetID == "" {
		return false
	}
	sm.lastViewportClickMutex.Lock()
	defer sm.lastViewportClickMutex.Unlock()
	return sm.lastViewportClickTarget == targetID && sm.lastViewportClickReplayed && time.Since(sm.lastViewportClickTime) < 2*time.Second
}

// hasViewportClickOrigin stays set until Chrome's top UI is clicked. When CDP
// supplies an opener, it must be the page that received the viewport click.
func (sm *SyncManager) hasViewportClickOrigin(openerTargetID string) bool {
	sm.mu.RLock()
	lastClickY := sm.lastClickY
	uiOffset := sm.uiOffsets[int(sm.masterWindow)]
	sm.mu.RUnlock()
	if float64(lastClickY) <= effectiveChromeTopUIOffset(uiOffset) {
		return false
	}

	sm.lastViewportClickMutex.Lock()
	defer sm.lastViewportClickMutex.Unlock()
	if sm.lastViewportClickTarget == "" {
		return false
	}
	return openerTargetID == "" || openerTargetID == sm.lastViewportClickTarget
}

func (sm *SyncManager) clearRecentViewportClick() {
	sm.lastViewportClickMutex.Lock()
	sm.lastViewportClickTime = time.Time{}
	sm.lastViewportClickTarget = ""
	sm.lastViewportClickReplayed = false
	sm.lastViewportClickMutex.Unlock()
}

// resetPageInputRoute makes a newly committed page target authoritative for
// subsequent native input. Programmatic tab operations do not produce a page
// click, so extension and pointer routes from the previous target must not
// survive the mapping change.
func (sm *SyncManager) resetPageInputRoute() {
	sm.clearRecentViewportClick()

	sm.mu.Lock()
	sm.keyboardExtensionURL = ""
	sm.lastClickX = 0
	sm.lastClickY = DefaultChromeTopUIHeight + 1
	sm.mu.Unlock()

	sm.pointerRouteMu.Lock()
	sm.leftRoute = nativeExtensionPointerRoute{}
	sm.rightRoute = nativeExtensionPointerRoute{}
	sm.pointerRouteMu.Unlock()

	sm.tabActivationMu.Lock()
	sm.lastTabActivationTarget = ""
	sm.lastTabActivationAt = time.Time{}
	sm.tabActivationMu.Unlock()
}

func (sm *SyncManager) shouldSkipDuplicateTabActivation(targetID string) bool {
	if targetID == "" {
		return false
	}

	sm.tabActivationMu.Lock()
	defer sm.tabActivationMu.Unlock()

	now := time.Now()
	if sm.lastTabActivationTarget == targetID && now.Sub(sm.lastTabActivationAt) < 250*time.Millisecond {
		return true
	}

	sm.lastTabActivationTarget = targetID
	sm.lastTabActivationAt = now
	return false
}

// SetProgressCallback registers a callback for progress updates
func (sm *SyncManager) SetProgressCallback(cb func(string)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onProgress = cb
}

func (sm *SyncManager) uiOffsetForPID(pid int) float64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if off, ok := sm.uiOffsets[pid]; ok && off > 0 {
		return off
	}

	return float64(DefaultChromeTopUIHeight)
}

func effectiveChromeTopUIOffset(uiOffset float64) float64 {
	if uiOffset < float64(DefaultChromeTopUIHeight) {
		return float64(DefaultChromeTopUIHeight)
	}
	return uiOffset
}

func clampFloat64(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func (sm *SyncManager) screenScaleForPID(pid int) float64 {
	sm.mu.RLock()
	rect, hasRect := sm.rectCache[pid]
	sm.mu.RUnlock()

	provider := &Provider{}
	screens, err := provider.GetScreensInfo()
	if err != nil || len(screens) == 0 {
		return 1
	}

	if hasRect {
		centerX := rect.Left + rect.Width/2
		centerY := rect.Top + rect.Height/2
		for _, screen := range screens {
			if centerX >= screen.Left && centerX <= screen.Left+screen.Width &&
				centerY >= screen.Top && centerY <= screen.Top+screen.Height &&
				screen.Scale > 0 {
				return screen.Scale
			}
		}
	}

	for _, screen := range screens {
		if screen.Primary && screen.Scale > 0 {
			return screen.Scale
		}
	}
	if screens[0].Scale > 0 {
		return screens[0].Scale
	}
	return 1
}

func (sm *SyncManager) mapMouseToViewport(targetPID int, relX, relY float64, allowTopUI bool) (float64, float64, bool) {
	sm.mu.RLock()
	sourcePID := int(sm.masterWindow)
	sourceRect, hasSourceRect := sm.rectCache[sourcePID]
	targetRect, hasTargetRect := sm.rectCache[targetPID]
	sourceUI := sm.uiOffsets[sourcePID]
	targetUI := sm.uiOffsets[targetPID]
	sourceViewport := sm.viewportMetrics[sourcePID]
	targetViewport := sm.viewportMetrics[targetPID]
	sm.mu.RUnlock()

	if !hasSourceRect || !hasTargetRect {
		return 0, 0, false
	}

	if sourceUI <= 0 {
		sourceUI = float64(DefaultChromeTopUIHeight)
	}
	if targetUI <= 0 {
		targetUI = float64(DefaultChromeTopUIHeight)
	}

	sourceWidth := sourceViewport.Width
	if sourceWidth <= 1 {
		sourceWidth = float64(sourceRect.Width)
	}

	sourceViewportHeight := sourceViewport.Height
	if sourceViewportHeight <= 1 {
		sourceViewportHeight = float64(sourceRect.Height) - sourceUI
	}
	if sourceWidth <= 1 || sourceViewportHeight <= 1 {
		return 0, 0, false
	}

	rx := clampFloat64(relX/sourceWidth, 0, 1)

	ry := 0.0
	if relY < sourceUI {
		if !allowTopUI {
			return 0, 0, false
		}
	} else {
		ry = clampFloat64((relY-sourceUI)/sourceViewportHeight, 0, 1)
	}

	targetWidth := targetViewport.Width
	if targetWidth <= 1 {
		targetWidth = float64(targetRect.Width)
	}

	targetHeight := targetViewport.Height
	if targetHeight <= 1 {
		targetHeight = float64(targetRect.Height) - targetUI
	}

	if targetWidth <= 1 || targetHeight <= 1 {
		return 0, 0, false
	}

	targetX := clampFloat64(rx*targetWidth, 0, targetWidth-1)
	targetY := clampFloat64(ry*targetHeight, 0, targetHeight-1)
	return targetX, targetY, true
}

func pageNativeUIOffset(rect common.Rect, metrics CDPViewportMetrics, fallback float64) float64 {
	viewportW := metrics.ViewportWidth()
	viewportH := metrics.ViewportHeight()
	if rect.Width > 1 && viewportW > 1 && viewportH > 1 {
		contentScale := float64(rect.Width) / viewportW
		inferred := float64(rect.Height) - viewportH*contentScale
		if inferred >= 0 && inferred < 500 && float64(rect.Height)-inferred > 1 {
			return inferred
		}
	}
	if fallback <= 0 || fallback >= float64(rect.Height)-1 {
		return float64(DefaultChromeTopUIHeight)
	}
	return fallback
}

func usesPageDOMClickRoute(targetURL string, relativeY, uiOffset float64, ready bool) bool {
	return ready && relativeY > uiOffset && supportsDOMClickBinding("page", targetURL)
}

func usesExtensionDOMClickRoute(targetURL string, evtType int) bool {
	return isExtensionURL(targetURL) && (evtType == 1 || evtType == 2) && supportsDOMClickBinding("page", targetURL)
}

func isChromeTopUIEvent(relativeY, uiOffset float64) bool {
	return relativeY <= uiOffset
}

func extensionIDForPageDismissal(evtType int, visibleExtensionURL string, relativeY, uiOffset float64) string {
	if evtType != 1 || relativeY <= uiOffset {
		return ""
	}
	return extensionIDFromURL(visibleExtensionURL)
}

func acceptsMasterDOMClickSourceURL(targetURL string) bool {
	if isExtensionURL(targetURL) {
		return true
	}
	return supportsDOMClickBinding("page", targetURL)
}

func mapPageViewportPoint(
	sourceRect common.Rect,
	targetRect common.Rect,
	sourceMetrics CDPViewportMetrics,
	targetMetrics CDPViewportMetrics,
	sourceUIFallback float64,
	targetUIFallback float64,
	relX float64,
	relY float64,
	allowTopUI bool,
) (float64, float64, bool) {
	if sourceRect.Width <= 1 || sourceRect.Height <= 1 || targetRect.Width <= 1 || targetRect.Height <= 1 {
		return 0, 0, false
	}

	sourceUI := pageNativeUIOffset(sourceRect, sourceMetrics, sourceUIFallback)
	sourceContentHeight := float64(sourceRect.Height) - sourceUI
	if sourceContentHeight <= 1 {
		return 0, 0, false
	}

	rx := clampFloat64(relX/float64(sourceRect.Width), 0, 1)
	ry := 0.0
	if relY < sourceUI {
		if !allowTopUI {
			return 0, 0, false
		}
	} else {
		ry = clampFloat64((relY-sourceUI)/sourceContentHeight, 0, 1)
	}

	targetWidth := targetMetrics.ViewportWidth()
	targetHeight := targetMetrics.ViewportHeight()
	if targetWidth <= 1 || targetHeight <= 1 {
		targetUI := pageNativeUIOffset(targetRect, targetMetrics, targetUIFallback)
		targetWidth = float64(targetRect.Width)
		targetHeight = float64(targetRect.Height) - targetUI
	}
	if targetWidth <= 1 || targetHeight <= 1 {
		return 0, 0, false
	}

	return clampFloat64(rx*targetWidth, 0, targetWidth-1),
		clampFloat64(ry*targetHeight, 0, targetHeight-1), true
}

func (sm *SyncManager) mapPageMouseToViewport(
	targetPID int,
	targetCDP *CDPClient,
	masterSessionID string,
	targetSessionID string,
	relX float64,
	relY float64,
	allowTopUI bool,
) (float64, float64, bool) {
	if targetCDP == nil || masterSessionID == "" || targetSessionID == "" {
		return 0, 0, false
	}

	sm.mu.RLock()
	masterCDP := sm.masterCDP
	sourcePID := int(sm.masterWindow)
	sourceRect, hasSourceRect := sm.rectCache[sourcePID]
	targetRect, hasTargetRect := sm.rectCache[targetPID]
	sourceUI := sm.uiOffsets[sourcePID]
	targetUI := sm.uiOffsets[targetPID]
	sm.mu.RUnlock()
	if masterCDP == nil || !hasSourceRect || !hasTargetRect {
		return 0, 0, false
	}

	sourceMetrics, _ := masterCDP.viewportMetricsForSession(masterSessionID)
	targetMetrics, _ := targetCDP.viewportMetricsForSession(targetSessionID)
	return mapPageViewportPoint(
		sourceRect,
		targetRect,
		sourceMetrics,
		targetMetrics,
		sourceUI,
		targetUI,
		relX,
		relY,
		allowTopUI,
	)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func rectContainsPoint(rect common.Rect, x, y int) bool {
	return x >= rect.Left && x <= rect.Left+rect.Width &&
		y >= rect.Top && y <= rect.Top+rect.Height
}

func roughlySameRect(a, b common.Rect) bool {
	return absInt(a.Left-b.Left) <= 4 &&
		absInt(a.Top-b.Top) <= 4 &&
		absInt(a.Width-b.Width) <= 8 &&
		absInt(a.Height-b.Height) <= 8
}

func isPopupCandidateRect(rect, mainRect common.Rect) bool {
	if rect.Width < 80 || rect.Height < 40 {
		return false
	}
	if roughlySameRect(rect, mainRect) {
		return false
	}
	if mainRect.Width > 0 && mainRect.Height > 0 &&
		rect.Width >= mainRect.Width*9/10 && rect.Height >= mainRect.Height*9/10 {
		return false
	}
	return true
}

func findPopupRectForPoint(pid int, screenX, screenY int, mainRect common.Rect) (common.Rect, bool) {
	if hit, ok := WindowAtPoint(screenX, screenY); ok {
		if int(hit.ProcessID) != pid || roughlySameRect(hit.Position, mainRect) {
			return common.Rect{}, false
		}
		if isPopupCandidateRect(hit.Position, mainRect) {
			return hit.Position, true
		}
		return common.Rect{}, false
	}

	windows, err := EnumWindowsForPID(int32(pid))
	if err != nil || len(windows) == 0 {
		if rect, ok := WindowRectAtPointForPID(int32(pid), screenX, screenY); ok && isPopupCandidateRect(rect, mainRect) {
			return rect, true
		}
		return common.Rect{}, false
	}
	defer releaseWindowInfos(windows)

	var best common.Rect
	bestArea := 0
	for _, info := range windows {
		rect := info.Position
		if !isPopupCandidateRect(rect, mainRect) {
			continue
		}
		area := rect.Width * rect.Height
		if !rectContainsPoint(rect, screenX, screenY) {
			continue
		}
		if bestArea == 0 || area < bestArea {
			best = rect
			bestArea = area
		}
	}
	if bestArea > 0 {
		return best, true
	}
	if rect, ok := WindowRectAtPointForPID(int32(pid), screenX, screenY); ok && isPopupCandidateRect(rect, mainRect) {
		return rect, true
	}
	return common.Rect{}, false
}

func findContainingWindowRectForPoint(pid int, screenX, screenY int, mainRect common.Rect) (common.Rect, bool, bool) {
	windows, err := EnumWindowsForPID(int32(pid))
	if err != nil || len(windows) == 0 {
		if rect, ok := WindowRectAtPointForPID(int32(pid), screenX, screenY); ok && rect.Width >= 80 && rect.Height >= 40 {
			return rect, true, isPopupCandidateRect(rect, mainRect)
		}
		return common.Rect{}, false, false
	}
	defer releaseWindowInfos(windows)

	var best common.Rect
	bestArea := 0
	hasPopupCandidate := false
	for _, info := range windows {
		rect := info.Position
		if isPopupCandidateRect(rect, mainRect) {
			hasPopupCandidate = true
		}
		if rect.Width < 80 || rect.Height < 40 || !rectContainsPoint(rect, screenX, screenY) {
			continue
		}
		area := rect.Width * rect.Height
		if bestArea == 0 || area < bestArea {
			best = rect
			bestArea = area
		}
	}
	if bestArea > 0 {
		return best, true, hasPopupCandidate
	}
	if rect, ok := WindowRectAtPointForPID(int32(pid), screenX, screenY); ok && rect.Width >= 80 && rect.Height >= 40 {
		return rect, true, isPopupCandidateRect(rect, mainRect)
	}
	return common.Rect{}, false, hasPopupCandidate
}

func (sm *SyncManager) beginMasterExtensionPopup(targetID string, extensionID string) (uint64, bool) {
	if targetID == "" || extensionID == "" {
		return 0, false
	}
	sm.popupLifecycleMu.Lock()
	defer sm.popupLifecycleMu.Unlock()

	if sm.masterPopupTargets == nil {
		sm.masterPopupTargets = make(map[string]string)
	}
	if sm.masterPopupByExt == nil {
		sm.masterPopupByExt = make(map[string]string)
	}
	if _, ok := sm.masterPopupTargets[targetID]; ok {
		return sm.popupOpenSeq, false
	}
	sm.masterPopupTargets[targetID] = extensionID
	sm.masterPopupByExt[extensionID] = targetID
	sm.popupOpenSeq++
	return sm.popupOpenSeq, true
}

func (sm *SyncManager) isExtensionPopupOpenCurrent(seq uint64) bool {
	sm.popupLifecycleMu.Lock()
	defer sm.popupLifecycleMu.Unlock()
	return sm.popupOpenSeq == seq
}

func (sm *SyncManager) cancelExtensionPopupOpen() {
	sm.popupLifecycleMu.Lock()
	sm.popupOpenSeq++
	sm.popupLifecycleMu.Unlock()
}

func (sm *SyncManager) consumeMasterPopupTarget(targetID string) (string, bool) {
	if targetID == "" {
		return "", false
	}
	sm.popupLifecycleMu.Lock()
	defer sm.popupLifecycleMu.Unlock()
	extensionID, ok := sm.masterPopupTargets[targetID]
	if ok {
		delete(sm.masterPopupTargets, targetID)
		if sm.masterPopupByExt[extensionID] == targetID {
			delete(sm.masterPopupByExt, extensionID)
		}
		sm.popupOpenSeq++
	}
	return extensionID, ok
}

func (sm *SyncManager) currentMasterPopupTarget(extensionID string) string {
	sm.popupLifecycleMu.Lock()
	defer sm.popupLifecycleMu.Unlock()
	return sm.masterPopupByExt[extensionID]
}

func (sm *SyncManager) mapSlaveExtensionTarget(masterTargetID string, pid int, slaveTargetID string) {
	if masterTargetID == "" || pid <= 0 || slaveTargetID == "" {
		return
	}
	sm.targetMapMutex.Lock()
	if _, ok := sm.targetMap[masterTargetID]; !ok {
		sm.targetMap[masterTargetID] = make(map[int]string)
	}
	sm.targetMap[masterTargetID][pid] = slaveTargetID
	sm.targetMapMutex.Unlock()
}

func (sm *SyncManager) handleSlaveExtensionTargetCreated(pid int, slaveTargetID string, targetURL string) {
	extensionID := extensionIDFromURL(targetURL)
	if extensionID == "" {
		return
	}
	masterTargetID := sm.currentMasterPopupTarget(extensionID)
	if masterTargetID == "" {
		return
	}
	sm.mapSlaveExtensionTarget(masterTargetID, pid, slaveTargetID)
}

func (sm *SyncManager) storeExtensionPointerRoute(evtType int, route nativeExtensionPointerRoute) {
	sm.pointerRouteMu.Lock()
	defer sm.pointerRouteMu.Unlock()
	switch evtType {
	case 1:
		sm.leftRoute = route
	case 3:
		sm.rightRoute = route
	}
}

func (sm *SyncManager) takeExtensionPointerRoute(evtType int) (nativeExtensionPointerRoute, bool) {
	sm.pointerRouteMu.Lock()
	defer sm.pointerRouteMu.Unlock()

	var route nativeExtensionPointerRoute
	switch evtType {
	case 2:
		route = sm.leftRoute
		sm.leftRoute = nativeExtensionPointerRoute{}
	case 4:
		route = sm.rightRoute
		sm.rightRoute = nativeExtensionPointerRoute{}
	default:
		return nativeExtensionPointerRoute{}, false
	}
	if route.extensionURL == "" || route.popupRect.Width <= 0 || route.popupRect.Height <= 0 ||
		time.Since(route.createdAt) > popupPointerRouteLifetime {
		return nativeExtensionPointerRoute{}, false
	}
	return route, true
}

type macPopupArrangeOwner struct {
	pid        int
	number     int
	order      int
	left       int
	top        int
	width      int
	height     int
	center     int
	mainWindow uintptr
	popups     []common.WindowInfo
	retained   []uintptr
}

func mainWindowInfoFromInfos(infos []common.WindowInfo) (common.WindowInfo, bool) {
	if len(infos) == 0 {
		return common.WindowInfo{}, false
	}
	best := infos[0]
	for _, info := range infos[1:] {
		if preferChromeMainWindow(info, best) {
			best = info
		}
	}
	return best, true
}

func releaseWindowInfos(infos []common.WindowInfo) {
	releaseWindowInfosExcept(infos, 0)
}

func releaseWindowInfosExcept(infos []common.WindowInfo, keep uintptr) {
	released := make(map[uintptr]struct{}, len(infos))
	for _, info := range infos {
		if info.HWND == 0 || info.HWND == keep {
			continue
		}
		if _, ok := released[info.HWND]; ok {
			continue
		}
		released[info.HWND] = struct{}{}
		ReleaseAXWindow(info.HWND)
	}
}

func (sm *SyncManager) releaseMainWindowRefsLocked() {
	for pid, handle := range sm.mainWindowRefs {
		if handle != 0 {
			ReleaseAXWindow(handle)
		}
		delete(sm.mainWindowRefs, pid)
	}
}

func sameEnumeratedWindow(a, b common.WindowInfo) bool {
	return a.HWND != 0 && a.HWND == b.HWND || roughlySameRect(a.Position, b.Position)
}

func appendFocusedWindowForPID(pid int32, infos []common.WindowInfo) []common.WindowInfo {
	focused, ok := FocusedWindowForPID(pid)
	if !ok || focused.HWND == 0 {
		return infos
	}
	for _, info := range infos {
		if sameEnumeratedWindow(info, focused) {
			ReleaseAXWindow(focused.HWND)
			return infos
		}
	}
	return append(infos, focused)
}

func (sm *SyncManager) collectExtensionPopupOwners() []macPopupArrangeOwner {
	sm.mu.RLock()
	if !sm.isRunning || sm.masterWindow == 0 {
		sm.mu.RUnlock()
		return nil
	}
	ownerHandles := make([]common.WindowHandle, 0, len(sm.slaveWindows)+1)
	ownerHandles = append(ownerHandles, sm.masterWindow)
	ownerHandles = append(ownerHandles, sm.slaveWindows...)
	sm.mu.RUnlock()

	owners := make([]macPopupArrangeOwner, 0, len(ownerHandles))
	for order, handle := range ownerHandles {
		pid := int(handle)
		if pid <= 0 {
			continue
		}
		infos, err := EnumWindowsForPID(int32(pid))
		if err != nil {
			continue
		}
		infos = appendFocusedWindowForPID(int32(pid), infos)
		if len(infos) == 0 {
			continue
		}
		mainWindow, ok := mainWindowInfoFromInfos(infos)
		if !ok {
			releaseWindowInfos(infos)
			continue
		}
		mainRect := mainWindow.Position
		popups := make([]common.WindowInfo, 0)
		for _, info := range infos {
			if info.HWND == 0 || !isPopupCandidateRect(info.Position, mainRect) {
				continue
			}
			popups = append(popups, info)
		}
		if len(popups) == 0 {
			releaseWindowInfos(infos)
			continue
		}

		retained := make([]uintptr, 0, len(infos))
		seen := make(map[uintptr]struct{}, len(infos))
		for _, info := range infos {
			if info.HWND == 0 {
				continue
			}
			if _, exists := seen[info.HWND]; exists {
				continue
			}
			seen[info.HWND] = struct{}{}
			retained = append(retained, info.HWND)
		}

		owners = append(owners, macPopupArrangeOwner{
			pid:        pid,
			number:     order + 1,
			order:      order,
			left:       mainRect.Left,
			top:        mainRect.Top,
			width:      mainRect.Width,
			height:     mainRect.Height,
			center:     mainRect.Left + mainRect.Width/2,
			mainWindow: mainWindow.HWND,
			popups:     popups,
			retained:   retained,
		})
	}
	return owners
}

func releaseExtensionPopupOwners(owners []macPopupArrangeOwner) {
	released := make(map[uintptr]struct{})
	for _, owner := range owners {
		for _, handle := range owner.retained {
			if handle == 0 {
				continue
			}
			if _, ok := released[handle]; ok {
				continue
			}
			released[handle] = struct{}{}
			ReleaseAXWindow(handle)
		}
	}
}

func sortExtensionPopupOwners(owners []macPopupArrangeOwner) {
	sort.SliceStable(owners, func(i, j int) bool {
		if absInt(owners[i].top-owners[j].top) > popupColumnGroupingGap {
			return owners[i].top > owners[j].top
		}
		if owners[i].number == owners[j].number {
			return owners[i].order > owners[j].order
		}
		return owners[i].number > owners[j].number
	})
}

func extensionPopupPlan(owners []macPopupArrangeOwner) ([]uintptr, map[uintptr]struct{}) {
	plan := make([]uintptr, 0)
	planSet := make(map[uintptr]struct{})
	for _, owner := range owners {
		for _, popup := range owner.popups {
			if popup.HWND == 0 {
				continue
			}
			plan = append(plan, popup.HWND)
			planSet[popup.HWND] = struct{}{}
		}
	}
	return plan, planSet
}

func (sm *SyncManager) arrangeExtensionPopupOwners(owners []macPopupArrangeOwner) {
	if !sm.popupArrangeMu.TryLock() {
		return
	}
	defer sm.popupArrangeMu.Unlock()

	if len(owners) == 0 {
		sm.popupArrangeBaseLeft = make(map[uintptr]int)
		return
	}

	sortExtensionPopupOwners(owners)
	plan, planSet := extensionPopupPlan(owners)
	if len(plan) == 0 {
		sm.popupArrangeBaseLeft = make(map[uintptr]int)
		return
	}
	for handle := range sm.popupArrangeBaseLeft {
		if _, ok := planSet[handle]; !ok {
			delete(sm.popupArrangeBaseLeft, handle)
		}
	}

	sm.offsetUpwardExtensionPopups(owners)
	for _, handle := range plan {
		NativeRaiseWindow(handle)
	}
	syncDebugf("[SyncManager] Arranged extension popups: owners=%d popups=%d mode=position-and-raise-once\n", len(owners), len(plan))
}

func popupOwnersContainExpectedPIDs(owners []macPopupArrangeOwner, expectedPIDs map[int]bool) bool {
	found := make(map[int]bool, len(owners))
	for _, owner := range owners {
		found[owner.pid] = true
	}
	for pid := range expectedPIDs {
		if !found[pid] {
			return false
		}
	}
	return true
}

func (sm *SyncManager) waitAndArrangeExtensionPopups(seq uint64, expectedPIDs map[int]bool) {
	deadline := time.Now().Add(popupNativeSettleTimeout)
	stablePasses := 0

	for {
		if !sm.isExtensionPopupOpenCurrent(seq) {
			return
		}

		owners := sm.collectExtensionPopupOwners()
		ready := popupOwnersContainExpectedPIDs(owners, expectedPIDs)
		if ready {
			stablePasses++
		} else {
			stablePasses = 0
		}

		if stablePasses >= 3 || time.Now().After(deadline) {
			sm.arrangeExtensionPopupOwners(owners)
			releaseExtensionPopupOwners(owners)
			return
		}

		releaseExtensionPopupOwners(owners)
		time.Sleep(popupNativeSettlePoll)
	}
}

func (sm *SyncManager) offsetUpwardExtensionPopups(owners []macPopupArrangeOwner) {
	if len(owners) == 0 {
		return
	}

	centerSum := 0
	for _, owner := range owners {
		centerSum += owner.center
	}
	screenPivot := centerSum / len(owners)

	for _, owner := range owners {
		rowRank := 0
		for _, candidate := range owners {
			if candidate.pid == owner.pid {
				continue
			}
			if candidate.top < owner.top && absInt(candidate.left-owner.left) <= popupColumnGroupingGap {
				rowRank++
			}
		}

		direction := 1
		if owner.center > screenPivot {
			direction = -1
		}
		shift := popupSideShift + rowRank*popupSideShiftStep

		for _, popup := range owner.popups {
			rect := popup.Position
			if rect.Top >= owner.top-popupUpwardDetectMargin {
				continue
			}
			baseLeft, ok := sm.popupArrangeBaseLeft[popup.HWND]
			if !ok {
				baseLeft = rect.Left
				sm.popupArrangeBaseLeft[popup.HWND] = baseLeft
			}
			targetLeft := sm.resolveUpwardPopupLeft(owner, rect, baseLeft, direction, shift, owners)
			NativeSetWindowPosition(popup.HWND, targetLeft, rect.Top, rect.Width, rect.Height)
		}
	}
}

func (sm *SyncManager) resolveUpwardPopupLeft(owner macPopupArrangeOwner, popupRect common.Rect, baseLeft int, direction int, baseShift int, owners []macPopupArrangeOwner) int {
	minLeft := owner.left
	maxLeft := owner.left + owner.width - popupRect.Width
	return clampInt(baseLeft+direction*baseShift, minLeft, maxLeft)
}

func clampInt(value, minValue, maxValue int) int {
	if maxValue < minValue {
		return minValue
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (sm *SyncManager) currentMasterExtensionURL() string {
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if masterCDP == nil || !masterCDP.isConnected {
		return ""
	}
	return masterCDP.cachedVisibleExtensionURL()
}

func (sm *SyncManager) masterExtensionPopupVisible(extensionID string) bool {
	if extensionID == "" {
		return false
	}
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	running := sm.isRunning && !sm.isPaused
	sm.mu.RUnlock()
	if !running || masterCDP == nil || !masterCDP.isConnected {
		return false
	}
	return len(masterCDP.extensionPopupTargets(extensionID)) > 0
}

func (sm *SyncManager) currentMasterActiveExtensionURL() string {
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if masterCDP == nil || !masterCDP.isConnected {
		return ""
	}
	return masterCDP.cachedActiveExtensionURL()
}

func (sm *SyncManager) masterExtensionPopupRoute(screenX, screenY int, masterRect common.Rect) (string, float64, float64, float64, float64, bool) {
	targetURL := sm.currentMasterExtensionURL()
	if targetURL == "" {
		return "", 0, 0, 0, 0, false
	}

	popupRect, ok := findPopupRectForPoint(int(sm.masterWindow), screenX, screenY, masterRect)
	if !ok {
		return "", 0, 0, 0, 0, false
	}
	localX := float64(screenX - popupRect.Left)
	localY := float64(screenY - popupRect.Top)
	return targetURL, localX, localY, float64(popupRect.Width), float64(popupRect.Height), true
}

func extensionMouseTargetURL(areaURL, visibleURL, activeURL string) string {
	extensionID := extensionIDFromURL(areaURL)
	if extensionID == "" {
		return areaURL
	}
	if extensionIDFromURL(visibleURL) == extensionID {
		return visibleURL
	}
	if extensionIDFromURL(activeURL) == extensionID {
		return activeURL
	}
	return areaURL
}

func (sm *SyncManager) masterExtensionWebAreaRoute(screenX, screenY int) (string, float64, float64, float64, float64, bool) {
	visibleURL := sm.currentMasterExtensionURL()
	activeURL := sm.currentMasterActiveExtensionURL()
	if visibleURL == "" && activeURL == "" {
		return "", 0, 0, 0, 0, false
	}
	targetURL, areaRect, ok := WebAreaAtPointForPID(int32(sm.masterWindow), screenX, screenY)
	if !ok || !isExtensionURL(targetURL) || isIgnoredCDPTargetURL(targetURL) || areaRect.Width <= 0 || areaRect.Height <= 0 {
		return "", 0, 0, 0, 0, false
	}
	localX := float64(screenX - areaRect.Left)
	localY := float64(screenY - areaRect.Top)
	if localX < 0 || localY < 0 || localX > float64(areaRect.Width) || localY > float64(areaRect.Height) {
		return "", 0, 0, 0, 0, false
	}
	targetURL = extensionMouseTargetURL(targetURL, visibleURL, activeURL)
	return targetURL, localX, localY, float64(areaRect.Width), float64(areaRect.Height), true
}

func (sm *SyncManager) masterExtensionPageRoute(screenX, screenY int, masterRect common.Rect) (string, float64, float64, float64, float64, bool) {
	targetURL := sm.currentMasterActiveExtensionURL()
	if targetURL == "" {
		return "", 0, 0, 0, 0, false
	}

	eventRect, ok, hasPopupCandidate := findContainingWindowRectForPoint(int(sm.masterWindow), screenX, screenY, masterRect)
	if !ok {
		return "", 0, 0, 0, 0, false
	}
	isMainRect := roughlySameRect(eventRect, masterRect)
	if isMainRect && hasPopupCandidate {
		return "", 0, 0, 0, 0, false
	}

	relX := float64(screenX - eventRect.Left)
	relY := float64(screenY - eventRect.Top)
	if !isMainRect {
		return targetURL, relX, relY, float64(eventRect.Width), float64(eventRect.Height), true
	}

	sm.mu.RLock()
	masterUIOffset := sm.uiOffsets[int(sm.masterWindow)]
	sm.mu.RUnlock()
	if masterUIOffset <= 0 {
		masterUIOffset = float64(DefaultChromeTopUIHeight)
	}
	if relX < 0 || relY <= masterUIOffset || relX > float64(masterRect.Width) || relY > float64(masterRect.Height) {
		return "", 0, 0, 0, 0, false
	}
	return targetURL, relX, relY, 0, 0, true
}

func (sm *SyncManager) closeSlaveVisibleExtensionTargets(extensionID string, reason string) {
	if extensionID == "" {
		return
	}

	type cdpSnapshot struct {
		label string
		pid   int
		cdp   *CDPClient
	}
	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	clients := make([]cdpSnapshot, 0, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			clients = append(clients, cdpSnapshot{label: "Slave", pid: pid, cdp: cdp})
		}
	}
	sm.mu.RUnlock()

	for _, client := range clients {
		go func(label string, p int, c *CDPClient) {
			if closed := c.closeVisibleExtensionTargets(extensionID); closed > 0 {
				syncDebugf("[SyncManager] Closed %s (PID %d) extension targets: extension=%s reason=%s count=%d\n", label, p, extensionID, reason, closed)
			}
		}(client.label, client.pid, client.cdp)
	}
}

func (sm *SyncManager) closeAllVisibleExtensionTargets(extensionID string, reason string) {
	if extensionID == "" {
		return
	}
	sm.cancelExtensionPopupOpen()
	sm.clearKeyboardExtensionURL(extensionID)

	type cdpSnapshot struct {
		label string
		pid   int
		cdp   *CDPClient
	}
	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	clients := make([]cdpSnapshot, 0, len(sm.slaveCDPs)+1)
	if sm.masterCDP != nil && sm.masterCDP.isConnected {
		clients = append(clients, cdpSnapshot{label: "Master", pid: int(sm.masterWindow), cdp: sm.masterCDP})
	}
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			clients = append(clients, cdpSnapshot{label: "Slave", pid: pid, cdp: cdp})
		}
	}
	sm.mu.RUnlock()

	for _, client := range clients {
		go func(label string, p int, c *CDPClient) {
			if closed := c.closeVisibleExtensionTargets(extensionID); closed > 0 {
				syncDebugf("[SyncManager] Closed %s (PID %d) extension targets: extension=%s reason=%s count=%d\n", label, p, extensionID, reason, closed)
			}
		}(client.label, client.pid, client.cdp)
	}
}

func (sm *SyncManager) setKeyboardExtensionURL(targetURL string) {
	sm.mu.Lock()
	sm.keyboardExtensionURL = targetURL
	sm.mu.Unlock()
}

func (sm *SyncManager) clearKeyboardExtensionURL(extensionID string) {
	sm.mu.Lock()
	if extensionID == "" || extensionIDFromURL(sm.keyboardExtensionURL) == extensionID {
		sm.keyboardExtensionURL = ""
	}
	sm.mu.Unlock()
}

func (sm *SyncManager) keyboardTargetURL() string {
	sm.mu.RLock()
	targetURL := sm.keyboardExtensionURL
	sm.mu.RUnlock()
	return targetURL
}

func (sm *SyncManager) handleMasterExtensionInput(state extensionEditableState) {
	if state.TargetURL == "" || (state.Kind != "edit" && state.Kind != "enter") {
		return
	}
	sm.mu.RLock()
	running := sm.isRunning && !sm.isPaused
	sm.mu.RUnlock()
	if !running {
		return
	}
	syncDebugf("[ExtensionInput] master event kind=%s valueLength=%d url=%s\n", state.Kind, len([]rune(state.Value)), state.TargetURL)
	sm.setKeyboardExtensionURL(state.TargetURL)
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if masterCDP == nil {
		return
	}
	masterTargetID, masterSessionID := masterCDP.extensionRouteSnapshot(state.TargetURL)
	if masterTargetID == "" || masterSessionID == "" {
		syncDebugf("[SyncRoute] secure extension input ignored: master target unavailable url=%s\n", state.TargetURL)
		return
	}
	sm.broadcastNativeEvent(NativeSyncEvent{
		Type:            "native_extension_input",
		MasterTargetID:  masterTargetID,
		MasterSessionID: masterSessionID,
		ExtensionURL:    state.TargetURL,
		ExtensionInput:  state,
	}, true)
}

func (sm *SyncManager) handleMasterDOMClick(sessionID string, action DOMClickAction) {
	sm.mu.RLock()
	running := sm.isRunning && !sm.isPaused
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if !running || masterCDP == nil || sessionID == "" {
		return
	}
	if action.Kind == "new_tab" {
		return
	}
	if action.Kind != "click" || action.Selector == "" {
		return
	}
	// A binding installed on an earlier HTTP page can survive a navigation to
	// chrome:// WebUI. Internal pages use the native coordinate route, so an old
	// DOM callback must not replay the same click a second time.
	if !acceptsMasterDOMClickSourceURL(action.SourceURL) {
		return
	}
	masterTargetID := ""
	masterSessionID := ""
	if isExtensionURL(action.SourceURL) {
		masterTargetID, masterSessionID = masterCDP.extensionRouteSnapshot(action.SourceURL)
	} else {
		masterTargetID, masterSessionID = masterCDP.pageRouteForDOMClick(sessionID, action.SourceURL)
	}
	if masterTargetID == "" || masterSessionID == "" {
		return
	}
	if isExtensionURL(action.SourceURL) {
		sm.touchViewportClick(masterTargetID, true)
	} else {
		sm.markViewportClickReplayed(masterTargetID)
	}
	sm.broadcastNativeEvent(NativeSyncEvent{
		Type:            "dom_click",
		MasterTargetID:  masterTargetID,
		MasterSessionID: masterSessionID,
		ExtensionURL:    action.SourceURL,
		DOMClick:        action,
	}, true)
}

func (sm *SyncManager) handleMasterExtensionTargetCreated(masterTargetID string, targetType string, targetURL string, openedByPageClick bool) {
	extensionID := extensionIDFromURL(targetURL)
	if extensionID == "" {
		return
	}
	sm.setKeyboardExtensionURL(targetURL)

	// OKX first exposes popup-init.html, then updates the same target to the real
	// popup URL after creating its password OOPIF. Attach before popup-open
	// deduplication so the later targetInfoChanged event can bind that iframe.
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	running := sm.isRunning && !sm.isPaused
	sm.mu.RUnlock()
	if running && masterCDP != nil {
		go func() {
			if _, err := masterCDP.ensureTargetAttached(masterTargetID, targetType, targetURL); err != nil {
				syncDebugf("[ExtensionInput] master target attach failed target=%s: %v\n", masterTargetID, err)
			}
			masterCDP.ensureExtensionInputFrameTargetsAttached(extensionID)
		}()
	}

	openSeq, shouldOpen := sm.beginMasterExtensionPopup(masterTargetID, extensionID)
	if !shouldOpen {
		syncDebugf("[SyncManager] Deduplicated extension popup open: extension=%s target=%s\n", extensionID, masterTargetID)
		return
	}

	syncDebugf("[SyncManager] Master Extension Popup: %s (TargetID: %s)\n", targetURL, masterTargetID)

	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	type slaveCDPSnapshot struct {
		pid int
		cdp *CDPClient
	}
	slaveSnapshot := make([]slaveCDPSnapshot, 0, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaveSnapshot = append(slaveSnapshot, slaveCDPSnapshot{pid: pid, cdp: cdp})
		}
	}
	sm.mu.RUnlock()
	sort.Slice(slaveSnapshot, func(i, j int) bool {
		return slaveSnapshot[i].pid < slaveSnapshot[j].pid
	})

	sm.targetMapMutex.Lock()
	if _, ok := sm.targetMap[masterTargetID]; !ok {
		sm.targetMap[masterTargetID] = make(map[int]string)
	}
	sm.targetMapMutex.Unlock()

	go func() {
		var wg sync.WaitGroup
		for _, slave := range slaveSnapshot {
			wg.Add(1)
			go func(p int, c *CDPClient) {
				defer wg.Done()
				if openedByPageClick {
					sm.mapExistingSlaveExtensionPopup(p, c, extensionID, masterTargetID)
					return
				}
				sm.openAndMapSlaveExtensionPopup(p, c, extensionID, masterTargetID)
			}(slave.pid, slave.cdp)
		}
		wg.Wait()
		if !sm.isExtensionPopupOpenCurrent(openSeq) {
			return
		}

		if !sm.masterExtensionPopupVisible(extensionID) {
			sm.closeSlaveVisibleExtensionTargets(extensionID, "master-popup-missing")
			return
		}
		expectedPIDs := make(map[int]bool, len(slaveSnapshot)+1)
		sm.targetMapMutex.RLock()
		mappedTargets := sm.targetMap[masterTargetID]
		for _, slave := range slaveSnapshot {
			if slave.cdp == nil || !slave.cdp.isConnected {
				continue
			}
			if mappedTargets != nil && mappedTargets[slave.pid] != "" {
				expectedPIDs[slave.pid] = true
			} else {
				syncDebugf("[ExtensionPopup] open failed without retry extension=%s pid=%d\n", extensionID, slave.pid)
			}
		}
		sm.targetMapMutex.RUnlock()

		sm.mu.RLock()
		expectedPIDs[int(sm.masterWindow)] = true
		sm.mu.RUnlock()
		sm.waitAndArrangeExtensionPopups(openSeq, expectedPIDs)
	}()
}

func (sm *SyncManager) mapExistingSlaveExtensionPopup(pid int, c *CDPClient, extensionID string, masterTargetID string) bool {
	if c == nil || !c.isConnected {
		return false
	}
	c.ensureExtensionTargetsAttached(extensionID)
	target, ok := c.visibleExtensionTarget(extensionID)
	if !ok || target.targetID == "" {
		return false
	}
	sm.mapSlaveExtensionTarget(masterTargetID, pid, target.targetID)
	return true
}

func (sm *SyncManager) openAndMapSlaveExtensionPopup(pid int, c *CDPClient, extensionID string, masterTargetID string) bool {
	if c == nil || !c.isConnected {
		return false
	}
	slaveTargetID, err := c.openExtensionActionPopup(extensionID)
	if err != nil {
		syncDebugf("[SyncManager] Slave CDP (PID %d) open extension popup failed: %v\n", pid, err)
		return false
	}
	c.ensureExtensionTargetsAttached(extensionID)
	c.ensureExtensionInputFrameTargetsAttached(extensionID)

	if slaveTargetID == "" {
		syncDebugf("[SyncManager] Slave CDP (PID %d) extension popup target not found after open\n", pid)
		return false
	}
	sm.mapSlaveExtensionTarget(masterTargetID, pid, slaveTargetID)
	syncDebugf("[SyncManager] Mapped Master Extension %s to Slave (PID %d) Target %s\n", masterTargetID, pid, slaveTargetID)
	return true
}

func (sm *SyncManager) handleMasterExtensionPageTargetCreated(masterTargetID string, targetType string, targetURL string) {
	extensionID := extensionIDFromURL(targetURL)
	if extensionID == "" {
		return
	}
	sm.setKeyboardExtensionURL(targetURL)

	syncDebugf("[SyncManager] Master Extension Page: %s (TargetID: %s)\n", targetURL, masterTargetID)

	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning {
		sm.mu.RUnlock()
		return
	}
	type slaveCDPSnapshot struct {
		pid int
		cdp *CDPClient
	}
	slaveSnapshot := make([]slaveCDPSnapshot, 0, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaveSnapshot = append(slaveSnapshot, slaveCDPSnapshot{pid: pid, cdp: cdp})
		}
	}
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if masterCDP != nil {
		go func() {
			if _, err := masterCDP.ensureTargetAttached(masterTargetID, targetType, targetURL); err != nil {
				syncDebugf("[ExtensionInput] master extension page attach failed target=%s: %v\n", masterTargetID, err)
			}
			masterCDP.ensureExtensionInputFrameTargetsAttached(extensionID)
		}()
	}

	sm.targetMapMutex.Lock()
	if _, ok := sm.targetMap[masterTargetID]; !ok {
		sm.targetMap[masterTargetID] = make(map[int]string)
	}
	sm.targetMapMutex.Unlock()

	// A full-page extension is a real Chrome tab. Reuse the corresponding tab
	// index when it already exists; otherwise create it exactly once on slaves
	// where the extension is installed.
	sm.mapMasterTargetByBrowserIndex(masterTargetID)
	for _, slave := range slaveSnapshot {
		go func(p int, c *CDPClient) {
			sm.targetMapMutex.RLock()
			mappedTargetID := sm.targetMap[masterTargetID][p]
			sm.targetMapMutex.RUnlock()
			if mappedTargetID != "" || !c.hasExtensionTarget(extensionID) {
				return
			}

			slaveTargetID, err := c.CreateTarget(targetURL)
			if err != nil {
				syncDebugf("[SyncManager] Slave CDP (PID %d) create extension page failed: %v\n", p, err)
				return
			}
			if _, err := c.ensureTargetAttached(slaveTargetID, "page", targetURL); err != nil {
				syncDebugf("[SyncManager] Slave CDP (PID %d) attach extension page failed: %v\n", p, err)
				return
			}
			sm.targetMapMutex.Lock()
			if _, ok := sm.targetMap[masterTargetID]; !ok {
				sm.targetMap[masterTargetID] = make(map[int]string)
			}
			sm.targetMap[masterTargetID][p] = slaveTargetID
			sm.targetMapMutex.Unlock()
		}(slave.pid, slave.cdp)
	}
}

func (sm *SyncManager) handleMasterURLReloaded(sessionID string, targetURL string) {
	if targetURL == "" || isBlankTargetURL(targetURL) || isIgnoredCDPTargetURL(targetURL) || isExtensionURL(targetURL) {
		return
	}
	if _, isPopup := extensionPopupID(targetURL); isPopup {
		return
	}

	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning || sm.masterCDP == nil {
		sm.mu.RUnlock()
		return
	}
	masterCDP := sm.masterCDP
	slaveClients := make(map[int]*CDPClient, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaveClients[pid] = cdp
		}
	}
	sm.mu.RUnlock()

	masterTargetID := masterCDP.getTargetIDBySessionID(sessionID)
	if masterTargetID == "" {
		return
	}

	slaveTargets := make(map[int]string)
	sm.targetMapMutex.RLock()
	if mapped := sm.targetMap[masterTargetID]; mapped != nil {
		for pid, targetID := range mapped {
			slaveTargets[pid] = targetID
		}
	}
	sm.targetMapMutex.RUnlock()

	syncDebugf("[SyncManager] Master Reloaded: target=%s url=%s. Reloading matching slaves...", masterTargetID, targetURL)
	sm.reapplyCurrentZoomToPID(int(sm.masterWindow), masterCDP, "master-reload")

	for pid, cdp := range slaveClients {
		go func(p int, c *CDPClient, slaveTargetID string) {
			if slaveTargetID == "" {
				return
			}
			sessionID := c.getSessionIDByTargetID(slaveTargetID)
			if sessionID == "" {
				var err error
				sessionID, err = c.ensureTargetAttached(slaveTargetID, "page", c.getURLForTarget(slaveTargetID))
				if err != nil {
					return
				}
			}

			if _, err := c.callCommand("Page.reload", map[string]interface{}{"ignoreCache": false}, sessionID, 2*time.Second); err != nil {
				log.Printf("[SyncManager] Slave CDP (PID %d) reload failed: %v", p, err)
				return
			}
			syncDebugf("[SyncManager] Reloaded Slave CDP (PID %d) url=%s", p, targetURL)
			sm.reapplyCurrentZoomToPID(p, c, "slave-reload")
		}(pid, cdp, slaveTargets[pid])
	}
}

func (sm *SyncManager) handleMasterTabActivated(masterPID int32, masterTargetID string, url string) {
	if masterTargetID == "" {
		return
	}
	if isExtensionURL(url) {
		sm.setKeyboardExtensionURL(url)
	} else {
		sm.setKeyboardExtensionURL("")
	}
	if sm.shouldSkipDuplicateTabActivation(masterTargetID) {
		return
	}

	sm.mu.RLock()
	if sm.isPaused || !sm.isRunning || sm.masterCDP == nil {
		sm.mu.RUnlock()
		return
	}
	masterCDP := sm.masterCDP
	slaveSnapshot := make(map[int]*CDPClient, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaveSnapshot[pid] = cdp
		}
	}
	sm.mu.RUnlock()

	syncDebugf("[SyncManager] Master Switched to Tab TargetID %s: %s\n", masterTargetID, url)
	sm.reapplyCurrentZoomToPID(int(masterPID), masterCDP, "master-tab-activated")
	sm.mapMasterTargetByBrowserIndex(masterTargetID)
	for pid, cdp := range slaveSnapshot {
		go func(p int, c *CDPClient) {
			sm.targetMapMutex.RLock()
			slaveTargetID := sm.targetMap[masterTargetID][p]
			sm.targetMapMutex.RUnlock()
			if slaveTargetID == "" {
				syncDebugf("[SyncManager] No target-index mapping on Slave (PID %d) for Master %s; skipping activation\n", p, masterTargetID)
				return
			}

			if err := c.ActivateTabByTargetIDInBackground(slaveTargetID); err != nil {
				syncDebugf("[SyncManager] Slave CDP (PID %d) background tab activation failed: %v\n", p, err)
				return
			}
			syncDebugf("[TabRoute] activated slave PID=%d target=%s for master=%s\n", p, slaveTargetID, masterTargetID)
			sm.reapplyCurrentZoomToPID(p, c, "slave-tab-activated")
		}(pid, cdp)
	}
}

func (sm *SyncManager) Start(masterWindow common.WindowHandle, slaveWindows []common.WindowHandle) error {
	if !CheckAccessibilityPermission(false) {
		return fmt.Errorf("同步需要 macOS 辅助功能权限。请在 系统设置 > 隐私与安全性 > 辅助功能 中授权 ChromeManager.app；如果键鼠监听仍无效，请同时在 输入监控 中授权 ChromeManager.app；如果之前添加过权限但重新打包了程序，请先删除旧记录再重新添加新打包的程序")
	}
	sm.CancelProgrammaticMasterTarget()

	sm.mu.Lock()

	if sm.isRunning {
		sm.mu.Unlock()
		return fmt.Errorf("sync is already running")
	}

	sm.masterWindow = masterWindow
	sm.slaveWindows = make([]common.WindowHandle, len(slaveWindows))
	copy(sm.slaveWindows, slaveWindows)
	sm.stopChan = make(chan struct{})
	sm.syncRunID++
	sm.resetScrollDispatch(sm.syncRunID)
	sm.isRunning = true
	sm.isPaused = false
	sm.releaseMainWindowRefsLocked()
	sm.rectCache = make(map[int]common.Rect)
	sm.mainWindowRefs = make(map[int]uintptr)
	sm.viewportMetrics = make(map[int]viewportMetricsSnapshot)
	sm.syncZoomIndex = defaultChromeZoomIndex
	sm.syncZoomSeq = 0
	sm.syncZoomTarget = chromeZoomSteps[defaultChromeZoomIndex]
	sm.syncZoomActive = false
	sm.lastClickX = 0
	sm.lastClickY = 9999 // Initialize out of UI bounds to allow early keyboard sync
	sm.lastTabActivationTarget = ""
	sm.lastTabActivationAt = time.Time{}
	sm.popupArrangeBaseLeft = make(map[uintptr]int)
	sm.keyboardExtensionURL = ""

	sm.popupLifecycleMu.Lock()
	sm.popupOpenSeq++
	sm.masterPopupTargets = make(map[string]string)
	sm.masterPopupByExt = make(map[string]string)
	sm.popupLifecycleMu.Unlock()

	sm.pointerRouteMu.Lock()
	sm.leftRoute = nativeExtensionPointerRoute{}
	sm.rightRoute = nativeExtensionPointerRoute{}
	sm.pointerRouteMu.Unlock()

	// Capture callback for use
	cb := sm.onProgress
	if cb != nil {
		cb("Init Native Event Tap...")
	}

	// --- Initialize CDP Connections ---
	// 1. Master CDP
	masterPID := int32(masterWindow)
	if cmdLine, err := GetProcessCommandLine(masterPID); err == nil {
		if port := utils.ExtractDebugPort(cmdLine); port > 0 {
			sm.masterCDP = NewCDPClient(port)
			sm.masterCDP.setUserDataDir(utils.ExtractUserDataDir(cmdLine))
			sm.masterCDP.OnExtensionInput = sm.handleMasterExtensionInput
			sm.masterCDP.OnDOMClick = sm.handleMasterDOMClick

			// Callback to broadcast navigation to slaves
			sm.masterCDP.OnNavigate = func(sessionID string, url string) {
				targetID := sm.masterCDP.getTargetIDBySessionID(sessionID)
				if sm.shouldSuppressProgrammaticMasterTarget(targetID, url) {
					syncDebugf("[SyncManager] Ignored programmatic master navigation target=%s url=%s\n", targetID, url)
					return
				}
				sm.mu.RLock()
				paused := sm.isPaused || !sm.isRunning
				sm.mu.RUnlock()
				if paused {
					return
				}
				go func() {
					sm.mu.RLock()
					defer sm.mu.RUnlock()
					if sm.isPaused || !sm.isRunning {
						return
					}

					sm.reapplyCurrentZoomToPID(int(masterPID), sm.masterCDP, "master-frame-navigated")

					// Deduplication: If this navigation was caused by an immediate in-page click, skip explicit broadcast
					// as the slave will natively replicate the click and navigate itself.
					if sm.hasRecentReplayedViewportClick(targetID) && !isBrowserInternalSyncURL(url) {
						return
					}

					syncDebugf("[SyncManager] Master Navigated to: %s", url)
					for pid, cdp := range sm.slaveCDPs {
						sm.targetMapMutex.RLock()
						slaveTargetID := ""
						if mapped := sm.targetMap[targetID]; mapped != nil {
							slaveTargetID = mapped[pid]
						}
						sm.targetMapMutex.RUnlock()
						go func(p int, c *CDPClient, targetID string) {
							if targetID == "" {
								syncDebugf("[SyncRoute] Slave CDP (PID %d) navigation skipped: target mapping unavailable\n", p)
								return
							}
							sessionID := c.getSessionIDByTargetID(targetID)
							if sessionID == "" {
								syncDebugf("[SyncRoute] Slave CDP (PID %d) navigation skipped: session unavailable for target %s\n", p, targetID)
								return
							}
							slaveURL := c.getURLForTarget(targetID)
							if slaveURL == url || slaveURL == url+"/" || url == slaveURL+"/" {
								syncDebugf("[SyncManager] Slave CDP (PID %d) already at %s, skipping Navigate\n", p, url)
								sm.reapplyCurrentZoomToPID(p, c, "slave-navigate-skip")
								return
							}
							if err := c.NavigateSession(sessionID, url); err != nil {
								log.Printf("[SyncManager] Slave CDP (PID %d) Navigation failed: %v", p, err)
								return
							}
							sm.reapplyCurrentZoomToPID(p, c, "slave-navigate")
						}(pid, cdp, slaveTargetID)
					}
				}()
			}

			sm.masterCDP.OnURLReloaded = func(sessionID string, url string) {
				sm.handleMasterURLReloaded(sessionID, url)
			}

			// Callback to broadcast new tab creation
			sm.masterCDP.OnTargetCreated = func(masterTargetID string, openerTargetID string, targetType string, url string) {
				if sm.shouldSuppressProgrammaticMasterTarget(masterTargetID, url) {
					syncDebugf("[SyncManager] Ignored programmatic master target target=%s url=%s\n", masterTargetID, url)
					return
				}
				sm.mu.RLock()
				if sm.isPaused || !sm.isRunning {
					sm.mu.RUnlock()
					return
				}

				if isVisibleExtensionTarget(targetType, url) {
					sm.mu.RUnlock()
					openedByPageClick := sm.hasViewportClickOrigin(openerTargetID)
					syncDebugf("[ExtensionPopup] source target=%s opener=%s pageClick=%t url=%s\n", masterTargetID, openerTargetID, openedByPageClick, url)
					sm.handleMasterExtensionTargetCreated(masterTargetID, targetType, url, openedByPageClick)
					return
				}
				if isExtensionFullPageTarget(targetType, url) {
					sm.mu.RUnlock()
					sm.handleMasterExtensionPageTargetCreated(masterTargetID, targetType, url)
					return
				}
				defer sm.mu.RUnlock()

				// Normal new tabs are activated on every slave below. Promote the
				// corresponding master target immediately as well, so its first page
				// click does not wait for the asynchronous visibility binding.
				sm.masterCDP.setActiveTarget(masterTargetID)

				// Same deduplication for TargetCreated
				if sm.hasRecentReplayedViewportClick(openerTargetID) && !isBrowserInternalSyncURL(url) {
					syncDebugf("[SyncManager] Master Created Tab %s, but skipping broadcast (viewport click recent)\n", url)
					return
				}

				syncDebugf("[SyncManager] Master Created Tab: %s (TargetID: %s)\n", url, masterTargetID)

				sm.targetMapMutex.Lock()
				if _, ok := sm.targetMap[masterTargetID]; !ok {
					sm.targetMap[masterTargetID] = make(map[int]string)
				}
				sm.targetMapMutex.Unlock()

				for pid, cdp := range sm.slaveCDPs {
					go func(p int, c *CDPClient) {
						if slaveTargetID, err := c.CreateTarget(url); err != nil {
							syncDebugf("[SyncManager] Slave CDP (PID %d) CreateTarget failed: %v\n", p, err)
						} else {
							if _, err := c.ensureTargetAttached(slaveTargetID, "page", url); err != nil {
								syncDebugf("[SyncManager] Slave CDP (PID %d) attach created target failed: %v\n", p, err)
								return
							}
							sm.targetMapMutex.Lock()
							sm.targetMap[masterTargetID][p] = slaveTargetID
							sm.targetMapMutex.Unlock()
							syncDebugf("[SyncManager] Mapped Master Target %s to Slave (PID %d) Target %s\n", masterTargetID, p, slaveTargetID)

							_ = c.ActivateTabByTargetIDInBackground(slaveTargetID)
							sm.reapplyCurrentZoomToPID(p, c, "slave-target-created")
						}
					}(pid, cdp)
				}
			}

			sm.masterCDP.OnTargetLoaded = func(targetID string, url string) {
				sm.finishProgrammaticMasterTarget(targetID, url)
			}

			// Callback to broadcast tab closing
			sm.masterCDP.OnTargetDestroyed = func(targetID string, targetURL string) {
				sm.finishProgrammaticMasterTarget(targetID, targetURL)
				if extensionID := extensionIDFromURL(targetURL); extensionID != "" {
					sm.clearKeyboardExtensionURL(extensionID)
				}
				sm.mu.RLock()
				defer sm.mu.RUnlock()
				if sm.isPaused || !sm.isRunning {
					return
				}
				if extensionID, ok := sm.consumeMasterPopupTarget(targetID); ok {
					sm.targetMapMutex.Lock()
					delete(sm.targetMap, targetID)
					sm.targetMapMutex.Unlock()
					syncDebugf("[PopupRoute] master popup destroyed; closing slaves extension=%s target=%s\n", extensionID, targetID)
					go sm.clearKeyboardExtensionURL(extensionID)
					go sm.closeSlaveVisibleExtensionTargets(extensionID, "master-popup-destroyed")
					return
				}
				if isExtensionURL(targetURL) {
					sm.targetMapMutex.Lock()
					delete(sm.targetMap, targetID)
					sm.targetMapMutex.Unlock()
					syncDebugf("[SyncManager] Master Extension Page Closed: %s (%s), skipping slave close\n", targetID, targetURL)
					return
				}
				syncDebugf("[SyncManager] Master Closed Tab: %s\n", targetID)

				for pid, cdp := range sm.slaveCDPs {
					go func(p int, c *CDPClient) {
						sm.targetMapMutex.RLock()
						slaveMap, exists := sm.targetMap[targetID]
						sm.targetMapMutex.RUnlock()

						if exists {
							if slaveTargetID, ok := slaveMap[p]; ok {
								if err := c.CloseTargetById(slaveTargetID); err != nil {
									syncDebugf("[SyncManager] Slave CDP (PID %d) CloseTargetById failed: %v\n", p, err)
								} else {
									syncDebugf("[SyncManager] Closed mapped Slave (PID %d) Tab TargetID %s\n", p, slaveTargetID)
								}
								// Remove mapping after close
								sm.targetMapMutex.Lock()
								delete(slaveMap, p)
								if len(slaveMap) == 0 {
									delete(sm.targetMap, targetID)
								}
								sm.targetMapMutex.Unlock()
								return
							}
						}

						// Fallback: If we couldn't find a mapping, it's risky to close the active tab,
						// but to maintain compatibility we can fallback to closing current target,
						// though this causes bugs with 'KeepOnlyCurrentTab'. Let's only log a warning.
						syncDebugf("[SyncManager] Warning: Slave mapping not found for Master Target %s closed. Skipping close to prevent active tab bug.\n", targetID)
					}(pid, cdp)
				}
			}

			// Callback to broadcast tab activation (switching tabs)
			sm.masterCDP.OnTabActivated = func(masterTargetID string, url string) {
				sm.handleMasterTabActivated(masterPID, masterTargetID, url)
			}

			if err := sm.masterCDP.Connect(); err != nil {
				syncDebugf("[SyncManager] Master CDP connect failed: %v\n", err)
			} else {
				syncDebugf("[SyncManager] Master CDP connected on port %d\n", port)
				// Create channel and worker for the master (used for scroll broadcasts)
				sm.masterChan = make(chan NativeSyncEvent, 100)
				go sm.slaveWorker(sm.masterChan, int(masterPID))
			}
		}
	}

	sm.slaveChans = make([]chan NativeSyncEvent, 0)
	for _, slaveHandle := range sm.slaveWindows {
		slavePID := int(slaveHandle)

		// Connect Slave CDP
		if cmdLine, err := GetProcessCommandLine(int32(slavePID)); err == nil {
			if port := utils.ExtractDebugPort(cmdLine); port > 0 {
				cdp := NewCDPClient(port)
				cdp.setUserDataDir(utils.ExtractUserDataDir(cmdLine))
				cdp.OnNavigate = func(_ string, url string) {
					go sm.reapplyCurrentZoomToPID(slavePID, cdp, "slave-frame-navigated")
				}
				cdp.OnTargetCreated = func(targetID string, _ string, targetType string, url string) {
					if isVisibleExtensionTarget(targetType, url) {
						sm.handleSlaveExtensionTargetCreated(slavePID, targetID, url)
					}
				}
				if err := cdp.Connect(); err != nil {
					syncDebugf("[SyncManager] Slave CDP (PID %d) connect failed: %v\n", slavePID, err)
				} else {
					sm.slaveCDPs[slavePID] = cdp
					syncDebugf("[SyncManager] Slave CDP (PID %d) connected on port %d\n", slavePID, port)
				}
			}
		}

		// Create channel and worker for this slave
		ch := make(chan NativeSyncEvent, 100)
		sm.slaveChans = append(sm.slaveChans, ch)
		go sm.slaveWorker(ch, slavePID)
	}

	if sm.masterCDP == nil || !sm.masterCDP.isConnected {
		sm.cleanupStartFailureLocked()
		sm.mu.Unlock()
		return fmt.Errorf("同步启动失败：主控窗口未连接调试端口。请通过 ChromeManager 的“打开窗口”启动窗口，或重新创建/覆盖创建环境后再导入")
	}

	var missingSlaveCDPs []int
	for _, slaveHandle := range sm.slaveWindows {
		slavePID := int(slaveHandle)
		cdp, ok := sm.slaveCDPs[slavePID]
		if !ok || cdp == nil || !cdp.isConnected {
			missingSlaveCDPs = append(missingSlaveCDPs, slavePID)
		}
	}
	if len(missingSlaveCDPs) > 0 {
		sm.cleanupStartFailureLocked()
		sm.mu.Unlock()
		return fmt.Errorf("同步启动失败：以下从控窗口未连接调试端口 %v。请通过 ChromeManager 的“打开窗口”启动窗口，或重新创建/覆盖创建环境后再导入", missingSlaveCDPs)
	}

	sm.mu.Unlock() // Unlock before potentially long-running start

	// Build the initial map before accepting input. A missing active route must
	// fail startup instead of leaving the UI in a misleading "syncing" state.
	if err := sm.mapInitialTargets(nil); err != nil {
		sm.mu.Lock()
		sm.cleanupStartFailureLocked()
		sm.mu.Unlock()
		return fmt.Errorf("同步启动失败：无法建立标签页映射：%w", err)
	}

	// --- ENABLE NATIVE EVENT TAP ---
	sm.updateWindowCache()
	go func() {
		ticker := time.NewTicker(5 * time.Second) // Update rects occasionally
		defer ticker.Stop()
		for {
			select {
			case <-sm.stopChan:
				return
			case <-ticker.C:
				sm.updateWindowCache()
			}
		}
	}()

	errChan := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // Ensure StartEventTap and RunEventLoop are on the SAME fixed OS thread
		if StartEventTap() {
			errChan <- nil
			RunEventLoop() // Blocks here
		} else {
			errChan <- fmt.Errorf("failed to start Native Event Tap")
		}
	}()

	err := <-errChan
	if err == nil {
		syncDebugf("[SyncManager] Native Event Tap Started")
	} else {
		log.Printf("[SyncManager] Failed to start Native Event Tap: %v", err)
		_ = sm.Stop()
		return fmt.Errorf("同步事件监听启动失败：%v。请检查 ChromeManager.app 是否已获得 macOS 辅助功能权限和输入监控权限", err)
	}

	syncDebugf("[SyncManager] Pure Native Sync Started")
	return nil
}

func (sm *SyncManager) cleanupStartFailureLocked() {
	sm.isRunning = false
	sm.syncRunID++
	sm.resetScrollDispatch(sm.syncRunID)
	sm.keyboardExtensionURL = ""
	sm.releaseMainWindowRefsLocked()
	sm.popupLifecycleMu.Lock()
	sm.popupOpenSeq++
	sm.masterPopupTargets = make(map[string]string)
	sm.masterPopupByExt = make(map[string]string)
	sm.popupLifecycleMu.Unlock()
	sm.pointerRouteMu.Lock()
	sm.leftRoute = nativeExtensionPointerRoute{}
	sm.rightRoute = nativeExtensionPointerRoute{}
	sm.pointerRouteMu.Unlock()
	if sm.stopChan != nil {
		close(sm.stopChan)
		sm.stopChan = nil
	}
	if sm.masterCDP != nil {
		sm.masterCDP.Close()
		sm.masterCDP = nil
	}
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil {
			cdp.Close()
		}
		delete(sm.slaveCDPs, pid)
	}
	sm.masterChan = nil
	sm.slaveChans = nil
	sm.keyboardExtensionURL = ""
}

func (sm *SyncManager) masterPageRouteSnapshot() (targetID, sessionID, targetURL string) {
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	running := sm.isRunning && !sm.isPaused
	sm.mu.RUnlock()
	if !running || masterCDP == nil || !masterCDP.isConnected {
		return "", "", ""
	}
	return masterCDP.activeRouteSnapshot()
}

func (sm *SyncManager) mappedSessionForEvent(pid int, cdp *CDPClient, msg NativeSyncEvent) string {
	if cdp == nil {
		return ""
	}
	if msg.ExtensionURL != "" {
		if sessionID := cdp.exactExtensionSessionID(msg.ExtensionURL); sessionID != "" {
			return sessionID
		}
		if extensionID := extensionIDFromURL(msg.ExtensionURL); extensionID != "" {
			cdp.ensureExtensionTargetsAttached(extensionID)
			if sessionID := cdp.exactExtensionSessionID(msg.ExtensionURL); sessionID != "" {
				return sessionID
			}
			_, sessionID := cdp.extensionRouteSnapshot(msg.ExtensionURL)
			if sessionID != "" {
				return sessionID
			}
		}
	}
	if msg.MasterTargetID == "" {
		return ""
	}

	sm.mu.RLock()
	masterPID := int(sm.masterWindow)
	sm.mu.RUnlock()
	if pid == masterPID {
		if msg.MasterSessionID != "" && cdp.getTargetIDBySessionID(msg.MasterSessionID) == msg.MasterTargetID {
			return msg.MasterSessionID
		}
		return cdp.getSessionIDByTargetID(msg.MasterTargetID)
	}

	sm.targetMapMutex.RLock()
	slaveTargetID := ""
	if mapped := sm.targetMap[msg.MasterTargetID]; mapped != nil {
		slaveTargetID = mapped[pid]
	}
	sm.targetMapMutex.RUnlock()
	if slaveTargetID == "" {
		return ""
	}
	return cdp.getSessionIDByTargetID(slaveTargetID)
}

// slaveWorker processes relative native events
func (sm *SyncManager) slaveWorker(ch <-chan NativeSyncEvent, pid int) {
	syncDebugf("[SyncManager] Worker started for PID %d\n", pid)
	for {
		select {
		case <-sm.stopChan:
			return
		case msg := <-ch:
			// --- NATIVE SYNC EVENTS ---
			switch msg.Type {
			case "dom_click":
				sm.mu.RLock()
				cdp := sm.slaveCDPs[pid]
				sm.mu.RUnlock()
				if cdp == nil || !cdp.isConnected {
					continue
				}
				mappedSessionID := sm.mappedSessionForEvent(pid, cdp, msg)
				if mappedSessionID == "" {
					continue
				}
				sessionID := mappedSessionID
				if isExtensionURL(msg.DOMClick.SourceURL) {
					sessionID = cdp.exactExtensionSessionID(msg.DOMClick.SourceURL)
				}
				if sessionID == "" {
					continue
				}
				if err := cdp.DispatchDOMClickToSession(sessionID, msg.DOMClick); err != nil {
					syncDebugf("[DOMClick] slave PID=%d click failed: %v\n", pid, err)
				}
			case "native_mouse":
				cdpEventType := ""
				button := "none"
				clickCount := 0

				switch msg.Value {
				case 1: // kCGEventLeftMouseDown
					cdpEventType = "mousePressed"
					button = "left"
					clickCount = 1
				case 2: // kCGEventLeftMouseUp
					cdpEventType = "mouseReleased"
					button = "left"
					clickCount = 1
				case 3: // kCGEventRightMouseDown
					cdpEventType = "mousePressed"
					button = "right"
					clickCount = 1
				case 4: // kCGEventRightMouseUp
					cdpEventType = "mouseReleased"
					button = "right"
					clickCount = 1
				case 5: // MouseMoved
					cdpEventType = "mouseMoved"
				case 6: // LeftDragged
					cdpEventType = "mouseMoved"
					button = "left"
				case 7: // RightDragged
					cdpEventType = "mouseMoved"
					button = "right"
				case 22: // kCGEventScrollWheel
					cdpEventType = "mouseWheel"
				}

				if cdpEventType != "" {
					sm.mu.RLock()
					var cdp *CDPClient
					var ok bool
					if pid == int(sm.masterWindow) {
						cdp = sm.masterCDP
						ok = cdp != nil
					} else {
						cdp, ok = sm.slaveCDPs[pid]
					}

					_, hasRect := sm.rectCache[pid]
					sm.mu.RUnlock()

					if ok && cdp != nil && hasRect {
						if msg.ExtensionURL != "" {
							mappedSessionID := sm.mappedSessionForEvent(pid, cdp, msg)
							if mappedSessionID == "" {
								continue
							}
							sessionID := mappedSessionID
							if msg.ExtensionContentRoute {
								sessionID = cdp.exactExtensionSessionID(msg.ExtensionURL)
							}
							if sessionID == "" {
								if cdpEventType == "mouseWheel" {
									syncDebugf("[ScrollTrace] drop pid=%d reason=no-extension-session url=%s routeWindow=%t routeContent=%t delta=%d\n",
										pid, msg.ExtensionURL, msg.ExtensionWindowRoute, msg.ExtensionContentRoute, msg.KeyCode)
								}
								continue
							}
							eventX := msg.X
							eventY := msg.Y
							routeName := "extension-viewport"
							if msg.ExtensionContentRoute && msg.PopupWidth > 0 && msg.PopupHeight > 0 {
								// Accessibility web-area coordinates are already local CSS viewport
								// coordinates. Scaling them again through Page.getLayoutMetrics
								// breaks extension pages such as Phantom, which reports a 25x25
								// layout viewport for its transformed popup surface.
								routeName = "extension-content-direct"
							} else if msg.ExtensionWindowRoute && msg.PopupWidth > 0 && msg.PopupHeight > 0 {
								routeName = "extension-window"
								targetX, targetY, mapped := cdp.ExtensionWindowPointToViewport(sessionID, msg.X, msg.Y, msg.PopupWidth, msg.PopupHeight)
								if !mapped && cdpEventType != "mouseWheel" {
									continue
								}
								if mapped {
									eventX = targetX
									eventY = targetY
								}
							} else if msg.PopupWidth <= 0 && msg.PopupHeight <= 0 {
								targetX, viewportY, mapped := sm.mapMouseToViewport(pid, msg.X, msg.Y, cdpEventType == "mouseWheel")
								if !mapped {
									continue
								}
								eventX = targetX
								eventY = viewportY
							}
							deltaY := 0.0
							if cdpEventType == "mouseWheel" {
								deltaY = float64(msg.KeyCode) * -100.0
								syncDebugf("[ScrollTrace] dispatch pid=%d route=%s session=%s input=(%.1f,%.1f %.0fx%.0f) output=(%.1f,%.1f) delta=%.1f url=%s\n",
									pid, routeName, sessionID, msg.X, msg.Y, msg.PopupWidth, msg.PopupHeight, eventX, eventY, deltaY, msg.ExtensionURL)
							}
							err := cdp.DispatchMouseEventToSession(sessionID, cdpEventType, eventX, eventY, button, clickCount, 0, deltaY)
							if err != nil {
								syncDebugf("[SyncManager] Extension CDP (PID %d) DispatchMouseEvent failed: %v\n", pid, err)
							} else if cdpEventType == "mouseWheel" {
								syncDebugf("[ScrollTrace] delivered pid=%d session=%s\n", pid, sessionID)
							}
							continue
						}

						sessionID := sm.mappedSessionForEvent(pid, cdp, msg)
						if sessionID == "" {
							syncDebugf("[SyncRoute] dropped page mouse PID=%d masterTarget=%s: mapped session unavailable\n", pid, msg.MasterTargetID)
							continue
						}

						targetX, viewportY, mapped := sm.mapPageMouseToViewport(
							pid,
							cdp,
							msg.MasterSessionID,
							sessionID,
							msg.X,
							msg.Y,
							cdpEventType == "mouseWheel",
						)
						if !mapped {
							if cdpEventType == "mouseWheel" {
								syncDebugf("[ScrollTrace] drop pid=%d reason=page-viewport-map input=(%.1f,%.1f) delta=%d\n", pid, msg.X, msg.Y, msg.KeyCode)
							}
							continue
						}

						// Only dispatch if it's inside the viewport or it's a scroll event
						if mapped {
							deltaY := 0.0
							if cdpEventType == "mouseWheel" {
								// macOS scroll wheel delta (msg.KeyCode) is typically line-based, 1 line ~ 20px.
								// Notice: macOS native scroll delta is positive for UP, negative for DOWN.
								// CDP 'mouseWheel' expects positive delta for DOWN, negative for UP (like web pixels).
								// We must negate the delta and significantly amplify it to match Chrome's native scroll speed.
								deltaY = float64(msg.KeyCode) * -100.0
							}

							err := cdp.DispatchMouseEventToSession(sessionID, cdpEventType, targetX, viewportY, button, clickCount, 0, deltaY)
							if err != nil {
								syncDebugf("[SyncManager] Slave CDP (PID %d) DispatchMouseEvent failed: %v\n", pid, err)
							} else if cdpEventType == "mouseWheel" {
								syncDebugf("[ScrollTrace] delivered pid=%d route=page output=(%.1f,%.1f) delta=%.1f\n", pid, targetX, viewportY, deltaY)
							}

						}
					}
				}
			case "sync_zoom":
				sm.mu.RLock()
				var cdp *CDPClient
				var ok bool
				if pid == int(sm.masterWindow) {
					cdp = sm.masterCDP
					ok = cdp != nil
				} else {
					cdp, ok = sm.slaveCDPs[pid]
				}
				sm.mu.RUnlock()

				if ok && cdp != nil {
					sm.applySyncZoomToPID(pid, cdp, msg.Zoom, msg.ZoomSeq, "shortcut")
				}
			case "native_key":
				var cdpType string
				switch msg.Value {
				case 10: // kCGEventKeyDown
					cdpType = "keyDown"
				case 11: // kCGEventKeyUp
					cdpType = "keyUp"
				}

				if cdpType != "" {
					code, winKeyCode, keyName := mapKeyCodeToDOMCode(msg.KeyCode)
					sm.mu.RLock()
					var cdp *CDPClient
					var ok bool
					if pid == int(sm.masterWindow) {
						cdp = sm.masterCDP
						ok = cdp != nil
					} else {
						cdp, ok = sm.slaveCDPs[pid]
					}
					sm.mu.RUnlock()

					if ok && cdp != nil {
						text := ""
						if cdpType == "keyDown" {
							text = msg.Chars
							if text == "" && keyName != "" && len(keyName) == 1 {
								text = keyName
							}

							// For control keys like enter, backspace, tab, text should generally be empty or specific control chars
							if len(keyName) > 1 {
								text = "" // Ensure modifiers/control keys don't inject their names as text
								if keyName == "Enter" {
									text = "\r"
								}
								if keyName == "Tab" {
									text = "\t"
								}
								if keyName == "Backspace" {
									text = "\b"
								}
							}
						}

						// Map raw macOS CGEventFlags to the CDP modifier bitmask (1=Alt, 2=Control, 4=Meta/Cmd, 8=Shift).
						cdpModActual := 0
						if (msg.Modifiers & kCGEventFlagMaskShift) != 0 {
							cdpModActual |= 8
						}
						if (msg.Modifiers & kCGEventFlagMaskControl) != 0 {
							cdpModActual |= 2
						}
						if (msg.Modifiers & kCGEventFlagMaskOption) != 0 {
							cdpModActual |= 1
						}
						if (msg.Modifiers & kCGEventFlagMaskCommand) != 0 {
							cdpModActual |= 4
						}

						extensionSessionID := ""
						if msg.ExtensionURL != "" {
							extensionSessionID = sm.mappedSessionForEvent(pid, cdp, msg)
						}
						if msg.ExtensionURL != "" && extensionSessionID == "" {
							syncDebugf("[SyncManager] Extension keyboard session not found PID=%d extension=%s\n", pid, msg.ExtensionURL)
							continue
						}
						targetSessionID := extensionSessionID
						if targetSessionID == "" {
							targetSessionID = sm.mappedSessionForEvent(pid, cdp, msg)
						}
						if targetSessionID == "" {
							syncDebugf("[SyncRoute] dropped page key PID=%d masterTarget=%s: mapped session unavailable\n", pid, msg.MasterTargetID)
							continue
						}

						// Handle browser shortcuts via JS (since CDP DispatchKeyEvent drops them)
						isShortcutTriggered := false
						if cdpType == "keyDown" && (cdpModActual&4) != 0 { // Cmd is down
							switch strings.ToLower(keyName) {
							case "c":
								go cdp.EvaluateOnSession(targetSessionID, `document.execCommand("copy")`)
								isShortcutTriggered = true
							case "v":
								clipTxt := getClipboardContent()
								go cdp.InsertTextToSession(targetSessionID, clipTxt, false)
								isShortcutTriggered = true
							case "x":
								go cdp.EvaluateOnSession(targetSessionID, `document.execCommand("cut")`)
								isShortcutTriggered = true
							case "a":
								go cdp.EvaluateOnSession(targetSessionID, `document.execCommand("selectAll")`)
								isShortcutTriggered = true
							case "z":
								if (cdpModActual & 8) != 0 { // Shift+Cmd+Z
									go cdp.EvaluateOnSession(targetSessionID, `document.execCommand("redo")`)
								} else { // Cmd+Z
									go cdp.EvaluateOnSession(targetSessionID, `document.execCommand("undo")`)
								}
								isShortcutTriggered = true
							}
						}

						if !isShortcutTriggered {
							// Key: If Cmd or Ctrl is pressed, it is a shortcut! Chrome expects text=""
							if (cdpModActual & 6) != 0 {
								text = ""
							}

							var err error
							if extensionSessionID != "" {
								isPrintableText := cdpType == "keyDown" &&
									text != "" &&
									text != "\r" &&
									text != "\t" &&
									text != "\b" &&
									(cdpModActual&6) == 0
								if isPrintableText {
									err = cdp.DispatchKeyEventToSession(extensionSessionID, "rawKeyDown", cdpModActual, keyName, code, winKeyCode, winKeyCode, "", "")
									if err == nil {
										charKey := keyName
										if len([]rune(text)) == 1 {
											charKey = text
										}
										err = cdp.DispatchKeyEventToSession(extensionSessionID, "char", cdpModActual, charKey, code, winKeyCode, winKeyCode, text, text)
									}
								} else {
									err = cdp.DispatchKeyEventToSession(extensionSessionID, cdpType, cdpModActual, keyName, code, winKeyCode, winKeyCode, text, text)
								}
							} else {
								err = cdp.DispatchKeyEventToSession(targetSessionID, cdpType, cdpModActual, keyName, code, winKeyCode, winKeyCode, text, text)
							}
							if err != nil {
								syncDebugf("[SyncManager] Slave CDP (PID %d) DispatchKeyEvent failed: %v\n", pid, err)
							}
						}
					}
				}
			case "native_extension_input":
				sm.mu.RLock()
				cdp := sm.slaveCDPs[pid]
				sm.mu.RUnlock()
				if cdp == nil || !cdp.isConnected {
					continue
				}
				if sm.mappedSessionForEvent(pid, cdp, msg) == "" {
					continue
				}
				sessionID := cdp.exactExtensionSessionID(msg.ExtensionURL)
				if sessionID == "" {
					sessionID = sm.mappedSessionForEvent(pid, cdp, msg)
				}
				if sessionID == "" {
					continue
				}
				switch msg.ExtensionInput.Kind {
				case "edit":
					if err := cdp.ApplyExtensionEditToSession(sessionID, msg.ExtensionInput); err != nil {
						syncDebugf("[SyncManager] Extension secure input apply failed PID=%d: %v\n", pid, err)
					} else {
						syncDebugf("[ExtensionInput] applied PID=%d inputType=%s valueLength=%d session=%s\n", pid, msg.ExtensionInput.InputType, len([]rune(msg.ExtensionInput.Value)), sessionID)
					}
				case "enter":
					if err := cdp.DispatchKeyEventToSession(sessionID, "keyDown", 0, "Enter", "Enter", 13, 13, "\r", "\r"); err == nil {
						_ = cdp.DispatchKeyEventToSession(sessionID, "keyUp", 0, "Enter", "Enter", 13, 13, "", "")
					}
				}
			}
		}
	}
}

// updateWindowCache refreshes rects for all windows
func (sm *SyncManager) updateWindowCache() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	pids := append([]int{int(sm.masterWindow)}, func() []int {
		res := make([]int, len(sm.slaveWindows))
		for i, h := range sm.slaveWindows {
			res[i] = int(h)
		}
		return res
	}()...)

	for _, pid := range pids {
		if handle := sm.mainWindowRefs[pid]; handle != 0 {
			if rect, err := NativeGetWindowRect(handle); err == nil && rect.Width >= 80 && rect.Height >= 40 {
				sm.rectCache[pid] = rect
				continue
			}
			ReleaseAXWindow(handle)
			delete(sm.mainWindowRefs, pid)
		}

		infos, err := EnumWindowsForPID(int32(pid))
		if err == nil && len(infos) > 0 {
			best, ok := mainWindowInfoFromInfos(infos)
			if ok {
				sm.rectCache[pid] = best.Position
				if best.HWND != 0 {
					sm.mainWindowRefs[pid] = best.HWND
				}
				releaseWindowInfosExcept(infos, best.HWND)
			} else {
				releaseWindowInfos(infos)
			}
		}
	}

	// Dynamically calculate accurate UI offset (Address Bar + Bookmarks Bar height) via All CDPs
	cdpMap := make(map[int]*CDPClient)
	if sm.masterCDP != nil && sm.masterCDP.isConnected {
		cdpMap[int(sm.masterWindow)] = sm.masterCDP
	}
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			cdpMap[pid] = cdp
		}
	}

	for pid, cdp := range cdpMap {
		go func(p int, client *CDPClient) {
			if metrics, err := client.GetViewportMetrics(); err == nil {
				width := metrics.ViewportWidth()
				height := metrics.ViewportHeight()
				if width > 0 && height > 0 {
					sm.mu.Lock()
					sm.viewportMetrics[p] = viewportMetricsSnapshot{
						Width:     width,
						Height:    height,
						UpdatedAt: time.Now(),
					}
					sm.mu.Unlock()
				}
			}

			res, err := client.Evaluate("window.outerHeight - window.innerHeight")
			if err == nil {
				if val, ok := res.(float64); ok && val > 0 && val < 500 {
					sm.mu.Lock()
					if sm.uiOffsets[p] != val {
						sm.uiOffsets[p] = val
					}
					sm.mu.Unlock()
				}
			}
		}(pid, cdp)
	}
}

func (sm *SyncManager) broadcastNativeEvent(msg NativeSyncEvent, reliable bool) {
	sm.mu.RLock()
	channels := append([]chan NativeSyncEvent(nil), sm.slaveChans...)
	stop := sm.stopChan
	sm.mu.RUnlock()

	for _, ch := range channels {
		if reliable {
			select {
			case ch <- msg:
			case <-stop:
				return
			}
			continue
		}
		select {
		case ch <- msg:
		default:
		}
	}
}

func isReliableNativeMouseEvent(evtType int) bool {
	switch evtType {
	case 1, 2, 3, 4:
		return true
	default:
		return false
	}
}

func sameNativeScrollRoute(a, b NativeSyncEvent) bool {
	return a.Type == "native_mouse" && b.Type == "native_mouse" &&
		a.Value == 22 && b.Value == 22 &&
		a.MasterTargetID == b.MasterTargetID &&
		a.ExtensionURL == b.ExtensionURL &&
		a.ExtensionWindowRoute == b.ExtensionWindowRoute &&
		a.ExtensionContentRoute == b.ExtensionContentRoute
}

func (sm *SyncManager) resetScrollDispatch(runID uint64) {
	sm.scrollMu.Lock()
	sm.scrollPending = nil
	sm.scrollDispatching = false
	sm.scrollRunID = runID
	sm.scrollMu.Unlock()
}

func (sm *SyncManager) queueScrollEvent(msg NativeSyncEvent) bool {
	sm.mu.RLock()
	runID := sm.syncRunID
	running := sm.isRunning && !sm.isPaused && sm.masterChan != nil && sm.stopChan != nil
	sm.mu.RUnlock()
	if !running {
		return false
	}

	sm.scrollMu.Lock()
	if sm.scrollRunID != runID {
		sm.scrollMu.Unlock()
		return false
	}
	if count := len(sm.scrollPending); count > 0 && sameNativeScrollRoute(sm.scrollPending[count-1], msg) {
		pending := &sm.scrollPending[count-1]
		pending.KeyCode += msg.KeyCode
		pending.X = msg.X
		pending.Y = msg.Y
		pending.PopupWidth = msg.PopupWidth
		pending.PopupHeight = msg.PopupHeight
	} else {
		sm.scrollPending = append(sm.scrollPending, msg)
	}
	if !sm.scrollDispatching {
		sm.scrollDispatching = true
		go sm.drainScrollEvents(runID)
	}
	sm.scrollMu.Unlock()
	return true
}

func (sm *SyncManager) drainScrollEvents(runID uint64) {
	for {
		time.Sleep(nativeScrollCoalesceInterval)

		sm.scrollMu.Lock()
		if sm.scrollRunID != runID {
			sm.scrollMu.Unlock()
			return
		}
		if len(sm.scrollPending) == 0 {
			sm.scrollDispatching = false
			sm.scrollMu.Unlock()
			return
		}
		msg := sm.scrollPending[0]
		sm.scrollPending = sm.scrollPending[1:]
		sm.scrollMu.Unlock()

		if !sm.deliverScrollEvent(runID, msg) {
			sm.scrollMu.Lock()
			if sm.scrollRunID == runID {
				sm.scrollPending = nil
				sm.scrollDispatching = false
			}
			sm.scrollMu.Unlock()
			return
		}
	}
}

func (sm *SyncManager) deliverScrollEvent(runID uint64, msg NativeSyncEvent) bool {
	sm.mu.RLock()
	if sm.syncRunID != runID || !sm.isRunning || sm.isPaused || sm.masterChan == nil || sm.stopChan == nil {
		sm.mu.RUnlock()
		return false
	}
	masterChan := sm.masterChan
	channels := append([]chan NativeSyncEvent(nil), sm.slaveChans...)
	stop := sm.stopChan
	sm.mu.RUnlock()

	select {
	case masterChan <- msg:
	case <-stop:
		return false
	}
	for _, ch := range channels {
		select {
		case ch <- msg:
		case <-stop:
			return false
		}
	}
	return true
}

func (sm *SyncManager) dispatchExtensionMouseEvent(evtType, data int, targetURL string, eventX, eventY, popupW, popupH float64, windowRoute, contentRoute bool) bool {
	if evtType == 1 || evtType == 3 {
		sm.mu.Lock()
		sm.lastClickX = int(eventX)
		if windowRoute || contentRoute {
			sm.lastClickY = DefaultChromeTopUIHeight + 1
		} else {
			sm.lastClickY = int(eventY)
		}
		sm.keyboardExtensionURL = targetURL
		sm.mu.Unlock()
	}

	sm.mu.RLock()
	masterCDP := sm.masterCDP
	sm.mu.RUnlock()
	if masterCDP == nil {
		return false
	}
	masterTargetID, masterSessionID := masterCDP.extensionRouteSnapshot(targetURL)
	if masterTargetID == "" || masterSessionID == "" {
		syncDebugf("[SyncRoute] extension mouse ignored: master target unavailable url=%s\n", targetURL)
		return false
	}
	domSessionID := masterSessionID
	if exactSessionID := masterCDP.exactExtensionSessionID(targetURL); exactSessionID != "" {
		domSessionID = exactSessionID
	}
	domClickReady := masterCDP.domClickReady(domSessionID)
	if evtType == 1 {
		sm.touchViewportClick(masterTargetID, !domClickReady)
	}
	if domClickReady && (evtType == 1 || evtType == 2) {
		return false
	}

	msg := NativeSyncEvent{
		Type:                  "native_mouse",
		X:                     eventX,
		Y:                     eventY,
		Value:                 evtType,
		KeyCode:               data,
		MasterTargetID:        masterTargetID,
		MasterSessionID:       masterSessionID,
		ExtensionURL:          targetURL,
		PopupWidth:            popupW,
		PopupHeight:           popupH,
		ExtensionWindowRoute:  windowRoute,
		ExtensionContentRoute: contentRoute,
	}

	if evtType == 22 {
		queued := sm.queueScrollEvent(msg)
		syncDebugf("[ScrollTrace] enqueue extension queued=%t routeWindow=%t routeContent=%t input=(%.1f,%.1f %.0fx%.0f) delta=%d url=%s\n",
			queued, windowRoute, contentRoute, eventX, eventY, popupW, popupH, data, targetURL)
		return queued
	}
	sm.broadcastNativeEvent(msg, isReliableNativeMouseEvent(evtType))
	return false
}

// HandleNativeMouseEvent called from CGO
func (sm *SyncManager) HandleNativeMouseEvent(evtType, x, y, data, pid int) bool {
	sm.mu.RLock()
	masterPID := int(sm.masterWindow)
	masterCDP := sm.masterCDP
	masterRect, ok := sm.rectCache[masterPID]
	sm.mu.RUnlock()
	if !ok || masterCDP == nil {
		return false
	}
	insideMasterBounds := x >= masterRect.Left && x <= masterRect.Left+masterRect.Width &&
		y >= masterRect.Top && y <= masterRect.Top+masterRect.Height
	onMasterMainWindow := false
	if insideMasterBounds {
		if hit, hitOK := WindowAtPoint(x, y); hitOK {
			onMasterMainWindow = int(hit.ProcessID) == masterPID && roughlySameRect(hit.Position, masterRect)
		} else {
			onMasterMainWindow = pid == masterPID
		}
	}

	// Handle Chrome's toolbar before extension routes. A hidden extension target
	// can outlive its popup and otherwise misclassify the next toolbar click as
	// extension content, leaving the previous page-click origin active.
	if onMasterMainWindow {
		relX := float64(x - masterRect.Left)
		relY := float64(y - masterRect.Top)
		masterUIOffset := effectiveChromeTopUIOffset(sm.uiOffsetForPID(masterPID))
		if isChromeTopUIEvent(relY, masterUIOffset) {
			if evtType == 1 || evtType == 3 {
				sm.clearRecentViewportClick()
				sm.mu.Lock()
				sm.lastClickX = int(relX)
				sm.lastClickY = int(relY)
				sm.mu.Unlock()
			}
			return false
		}
	}

	if pairedRoute, paired := sm.takeExtensionPointerRoute(evtType); paired {
		localX := float64(x - pairedRoute.popupRect.Left)
		localY := float64(y - pairedRoute.popupRect.Top)
		return sm.dispatchExtensionMouseEvent(
			evtType,
			data,
			pairedRoute.extensionURL,
			localX,
			localY,
			float64(pairedRoute.popupRect.Width),
			float64(pairedRoute.popupRect.Height),
			pairedRoute.windowRoute,
			pairedRoute.contentRoute,
		)
	}

	if targetURL, contentX, contentY, contentW, contentH, routed := sm.masterExtensionWebAreaRoute(x, y); routed {
		if usesExtensionDOMClickRoute(targetURL, evtType) {
			return false
		}
		if evtType == 1 || evtType == 3 {
			sm.storeExtensionPointerRoute(evtType, nativeExtensionPointerRoute{
				extensionURL: targetURL,
				popupRect: common.Rect{
					Left:   x - int(contentX),
					Top:    y - int(contentY),
					Width:  int(contentW),
					Height: int(contentH),
				},
				contentRoute: true,
				createdAt:    time.Now(),
			})
			syncDebugf("[PopupRoute] extension web area route screen=(%d,%d) local=(%.1f,%.1f) size=(%.0f,%.0f) url=%s\n",
				x, y, contentX, contentY, contentW, contentH, targetURL)
		}
		return sm.dispatchExtensionMouseEvent(evtType, data, targetURL, contentX, contentY, contentW, contentH, false, true)
	}

	if targetURL, relX, relY, popupW, popupH, routed := sm.masterExtensionPageRoute(x, y, masterRect); routed {
		if usesExtensionDOMClickRoute(targetURL, evtType) {
			return false
		}
		windowRoute := popupW > 0 || popupH > 0
		if evtType == 1 || evtType == 3 {
			if windowRoute {
				sm.storeExtensionPointerRoute(evtType, nativeExtensionPointerRoute{
					extensionURL: targetURL,
					popupRect: common.Rect{
						Left:   x - int(relX),
						Top:    y - int(relY),
						Width:  int(popupW),
						Height: int(popupH),
					},
					windowRoute: true,
					createdAt:   time.Now(),
				})
			}
			syncDebugf("[PopupRoute] extension page route screen=(%d,%d) local=(%.1f,%.1f) size=(%.0f,%.0f) url=%s\n",
				x, y, relX, relY, popupW, popupH, targetURL)
		}
		return sm.dispatchExtensionMouseEvent(
			evtType,
			data,
			targetURL,
			relX,
			relY,
			popupW,
			popupH,
			windowRoute,
			false,
		)
	}

	if targetURL, popupX, popupY, popupW, popupH, routed := sm.masterExtensionPopupRoute(x, y, masterRect); routed {
		if usesExtensionDOMClickRoute(targetURL, evtType) {
			return false
		}
		if evtType == 1 || evtType == 3 {
			sm.storeExtensionPointerRoute(evtType, nativeExtensionPointerRoute{
				extensionURL: targetURL,
				popupRect: common.Rect{
					Left:   x - int(popupX),
					Top:    y - int(popupY),
					Width:  int(popupW),
					Height: int(popupH),
				},
				windowRoute: true,
				createdAt:   time.Now(),
			})
			syncDebugf("[PopupRoute] popup rect route screen=(%d,%d) local=(%.1f,%.1f) size=(%.0f,%.0f) url=%s\n",
				x, y, popupX, popupY, popupW, popupH, targetURL)
		}
		return sm.dispatchExtensionMouseEvent(evtType, data, targetURL, popupX, popupY, popupW, popupH, true, false)
	}

	// Check bounds (Is it inside Master?)
	if onMasterMainWindow {

		// Calculate Relative
		relX := float64(x - masterRect.Left)
		relY := float64(y - masterRect.Top)
		masterUIOffset := effectiveChromeTopUIOffset(sm.uiOffsetForPID(masterPID))
		if extensionID := extensionIDForPageDismissal(evtType, sm.currentMasterExtensionURL(), relY, masterUIOffset); extensionID != "" {
			sm.closeAllVisibleExtensionTargets(extensionID, "master-page-click")
		}

		if evtType == 1 || evtType == 3 { // LeftMouseDown or RightMouseDown
			sm.mu.Lock()
			sm.lastClickX = int(relX)
			sm.lastClickY = int(relY)
			sm.keyboardExtensionURL = ""
			sm.mu.Unlock()
		}

		masterTargetID, masterSessionID, masterTargetURL := sm.masterPageRouteSnapshot()
		if masterTargetID == "" || masterSessionID == "" {
			syncDebugf("[SyncRoute] page mouse ignored: master page route unavailable\n")
			return false
		}
		domClickRoute := usesPageDOMClickRoute(
			masterTargetURL,
			relY,
			masterUIOffset,
			masterCDP.domClickReady(masterSessionID),
		)
		if evtType == 1 && relY > masterUIOffset {
			sm.touchViewportClick(masterTargetID, !domClickRoute)
		}
		if domClickRoute && (evtType == 1 || evtType == 2) {
			return false
		}

		msg := NativeSyncEvent{
			Type:            "native_mouse",
			X:               relX, // Store as absolute logical points
			Y:               relY, // Store as absolute logical points
			Value:           evtType,
			KeyCode:         data,
			MasterTargetID:  masterTargetID,
			MasterSessionID: masterSessionID,
		}

		if evtType == 22 {
			queued := sm.queueScrollEvent(msg)
			syncDebugf("[ScrollTrace] enqueue page queued=%t screen=(%d,%d) input=(%.1f,%.1f) delta=%d visible=%s active=%s\n",
				queued, x, y, relX, relY, data, sm.currentMasterExtensionURL(), sm.currentMasterActiveExtensionURL())
			return queued
		}

		// Button transitions are reliable so a click cannot lose one half of its
		// pressed/released pair under brief load. Pointer motion remains lossy.
		sm.broadcastNativeEvent(msg, isReliableNativeMouseEvent(evtType))
	}
	if evtType == 22 {
		syncDebugf("[ScrollTrace] native pass-through reason=no-route screen=(%d,%d) masterRect=(%d,%d %dx%d) visible=%s active=%s\n",
			x, y, masterRect.Left, masterRect.Top, masterRect.Width, masterRect.Height, sm.currentMasterExtensionURL(), sm.currentMasterActiveExtensionURL())
	}
	return false
}

// HandleNativeKeyboardEvent called from CGO. It returns true when the native
// event should be consumed by the event tap.
func (sm *SyncManager) HandleNativeKeyboardEvent(evtType, keyCode, modifiers, pid int, chars string) bool {
	sm.mu.Lock()
	sm.lastModifiers = modifiers // Track modifiers globally
	sm.mu.Unlock()

	_, _, keyName := mapKeyCodeToDOMCode(keyCode)

	// Filter: Only sync if Master Window is focused
	frontPID := GetFrontmostPID()
	if frontPID != int(sm.masterWindow) {
		return false
	}
	if isChromeNativeZoomShortcut(evtType, modifiers, keyName) {
		if evtType == kCGEventKeyDown {
			if direction, ok := chromeNativeZoomDirection(keyName); ok {
				go sm.broadcastNativeChromeZoomShortcut(direction)
			}
		}
		return true
	}

	extensionURL := sm.keyboardTargetURL()
	masterTargetID := ""
	masterSessionID := ""
	if extensionURL == "" {
		masterTargetID, masterSessionID, _ = sm.masterPageRouteSnapshot()
		if masterTargetID == "" || masterSessionID == "" {
			syncDebugf("[SyncRoute] page key ignored: master page route unavailable\n")
			return false
		}
	} else {
		sm.mu.RLock()
		masterCDP := sm.masterCDP
		sm.mu.RUnlock()
		if masterCDP != nil {
			masterTargetID, masterSessionID = masterCDP.extensionRouteSnapshot(extensionURL)
		}
		if masterTargetID == "" || masterSessionID == "" {
			syncDebugf("[SyncRoute] extension key ignored: master target unavailable url=%s\n", extensionURL)
			return false
		}
	}

	// Filter: Do NOT sync keyboard events if the last click was in the Address Bar / Top UI zone.
	// Extension popups are separate Chrome windows, so their synthetic lastClickY should not be
	// treated as an address bar click.
	sm.mu.RLock()
	masterUIOffset := float64(DefaultChromeTopUIHeight)
	if off, ok := sm.uiOffsets[int(sm.masterWindow)]; ok {
		masterUIOffset = off
	}
	inUIZone := float64(sm.lastClickY) <= masterUIOffset
	sm.mu.RUnlock()

	if inUIZone && extensionURL == "" {
		// User is likely typing a URL, drop the keyboard event so it doesn't corrupt the slave web page.
		// The `Page.frameNavigated` will sync the page once they hit Enter.
		return false
	}
	if extensionURL != "" && IsSecureInputEnabled() {
		return false
	}

	msg := NativeSyncEvent{
		Type:            "native_key",
		Value:           evtType,
		KeyCode:         keyCode,
		Chars:           chars,
		Modifiers:       modifiers,
		MasterTargetID:  masterTargetID,
		MasterSessionID: masterSessionID,
		ExtensionURL:    extensionURL,
	}

	sm.broadcastNativeEvent(msg, true)
	return false
}

// ExecuteBatchInput simulates typing batch text via CDP
func (sm *SyncManager) ExecuteBatchInput(pids []int, text string, delayed bool, overwrite bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Find all CDP clients matching the requested PIDs
	for _, pid := range pids {
		var targetCDP *CDPClient
		if int(sm.masterWindow) == pid {
			targetCDP = sm.masterCDP
		} else {
			for _, slavePID := range sm.slaveWindows {
				if int(slavePID) == pid {
					if cdp, ok := sm.slaveCDPs[pid]; ok {
						targetCDP = cdp
					}
					break
				}
			}
		}

		if targetCDP != nil && targetCDP.isConnected {
			go func(c *CDPClient) {
				// Focus first or rely on existing focus
				_ = c.FocusInputElement()
				time.Sleep(50 * time.Millisecond)

				if delayed {
					// Simulate typing character by character
					runes := []rune(text)
					for i, r := range runes {
						// Only overwrite on the first character
						isFirstChar := i == 0
						err := c.InsertText(string(r), overwrite && isFirstChar)
						if err != nil {
							syncDebugf("[SyncManager] ExecuteBatchInput failed for PID %d: %v\n", pid, err)
							break
						}
						// 30-100ms delay between characters
						time.Sleep(time.Duration(30+rand.Intn(70)) * time.Millisecond)
					}
				} else {
					err := c.InsertText(text, overwrite)
					if err != nil {
						syncDebugf("[SyncManager] ExecuteBatchInput failed for PID %d: %v\n", pid, err)
					}
				}
			}(targetCDP)
		}
	}
}

// EvaluateJSOnPort finds the CDPClient connected on the given debug port
// and evaluates the JavaScript expression through its existing session.
// This avoids creating a new page-level WebSocket connection that would
// conflict with the sync engine's existing browser-level CDP sessions.
func (sm *SyncManager) EvaluateJSOnPort(port int, expression string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Check master CDP
	if sm.masterCDP != nil && sm.masterCDP.isConnected && sm.masterCDP.debugPort == port {
		_, err := sm.masterCDP.Evaluate(expression)
		return err
	}

	// Check slave CDPs
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected && cdp.debugPort == port {
			_, err := cdp.Evaluate(expression)
			return err
		}
	}

	return fmt.Errorf("no active CDP client found for port %d", port)
}

func (sm *SyncManager) SetChromeNativeZoomOnPort(port int, level int) (NativeZoomResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.masterCDP != nil && sm.masterCDP.isConnected && sm.masterCDP.debugPort == port {
		return sm.masterCDP.SetChromeNativeZoomLevel(level)
	}
	for _, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected && cdp.debugPort == port {
			return cdp.SetChromeNativeZoomLevel(level)
		}
	}
	return NativeZoomResult{}, fmt.Errorf("no active CDP client found for port %d", port)
}

func (sm *SyncManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.isRunning
}

func (sm *SyncManager) Stop() error {
	sm.CancelProgrammaticMasterTarget()
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return nil
	}

	sm.isRunning = false
	sm.syncRunID++
	sm.resetScrollDispatch(sm.syncRunID)
	sm.releaseMainWindowRefsLocked()
	if sm.stopChan != nil {
		close(sm.stopChan)
		sm.stopChan = nil
	}

	// Close CDP Connections
	if sm.masterCDP != nil {
		sm.masterCDP.Close()
		sm.masterCDP = nil
	}
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil {
			cdp.Close()
		}
		delete(sm.slaveCDPs, pid)
	}

	StopEventTap()

	return nil
}

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

func (sm *SyncManager) SetConfig(config common.SyncConfig) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.config = config
	return nil
}

func (sm *SyncManager) GetConfig() common.SyncConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.config
}

func (sm *SyncManager) PauseSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.isPaused = true
	return nil
}

func (sm *SyncManager) ResumeSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.isPaused = false
	return nil
}

func (sm *SyncManager) MapProgrammaticPageTargets(masterTargetID, targetURL string, targetIDs map[int]string) error {
	if masterTargetID == "" {
		return fmt.Errorf("master target is empty")
	}

	sm.mu.RLock()
	masterPID := int(sm.masterWindow)
	masterCDP := sm.masterCDP
	slaveCDPs := make(map[int]*CDPClient, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaveCDPs[pid] = cdp
		}
	}
	sm.mu.RUnlock()

	if masterCDP == nil || targetIDs[masterPID] != masterTargetID {
		return fmt.Errorf("programmatic master target does not match the active sync master")
	}
	if _, err := masterCDP.ensureTargetAttached(masterTargetID, "page", targetURL); err != nil {
		return fmt.Errorf("attach programmatic master target: %w", err)
	}
	masterCDP.setActiveTarget(masterTargetID)

	mapped := make(map[int]string, len(slaveCDPs))
	for pid, cdp := range slaveCDPs {
		targetID := targetIDs[pid]
		if targetID == "" {
			continue
		}
		if _, err := cdp.ensureTargetAttached(targetID, "page", targetURL); err != nil {
			return fmt.Errorf("attach programmatic slave target for PID %d: %w", pid, err)
		}
		cdp.setActiveTarget(targetID)
		mapped[pid] = targetID
	}

	sm.targetMapMutex.Lock()
	sm.targetMap[masterTargetID] = mapped
	sm.targetMapMutex.Unlock()
	sm.resetPageInputRoute()
	sm.CancelProgrammaticMasterTarget()
	syncDebugf("[TabRoute] mapped programmatic target master=%s slaves=%d\n", masterTargetID, len(mapped))
	return nil
}

// RefreshPageTargetMappings rebinds the currently visible page in every
// synchronized browser after an external tab-management operation.
func (sm *SyncManager) RefreshPageTargetMappings(preferredTargetIDs map[int]string) error {
	sm.mu.RLock()
	running := sm.isRunning
	sm.mu.RUnlock()
	if !running {
		return nil
	}
	if err := sm.mapInitialTargets(preferredTargetIDs); err != nil {
		return err
	}
	sm.CancelProgrammaticMasterTarget()
	return nil
}

func browserTabByTargetID(tabs []browserTabDescriptor, targetID string) (browserTabDescriptor, bool) {
	for _, tab := range tabs {
		if tab.TargetID == targetID {
			return tab, true
		}
	}
	return browserTabDescriptor{}, false
}

func selectActiveBrowserTab(tabs []browserTabDescriptor, preferredTargetID string) (browserTabDescriptor, error) {
	if preferredTargetID != "" {
		if tab, ok := browserTabByTargetID(tabs, preferredTargetID); ok {
			return tab, nil
		}
		return browserTabDescriptor{}, fmt.Errorf("preferred target %s was not found", preferredTargetID)
	}
	for _, tab := range tabs {
		if tab.Active {
			return tab, nil
		}
	}
	return browserTabDescriptor{}, fmt.Errorf("active target was not reported")
}

func browserTabByIndex(tabs []browserTabDescriptor, index int) (browserTabDescriptor, bool) {
	for _, tab := range tabs {
		if tab.Index == index {
			return tab, true
		}
	}
	return browserTabDescriptor{}, false
}

func (sm *SyncManager) mapMasterTargetByBrowserIndex(masterTargetID string) {
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	slaves := make(map[int]*CDPClient, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaves[pid] = cdp
		}
	}
	sm.mu.RUnlock()
	if masterCDP == nil || masterTargetID == "" {
		return
	}

	// Startup and target creation establish authoritative pairs. A normal tab
	// switch must use them directly instead of rescanning and overwriting them.
	sm.targetMapMutex.RLock()
	mapped := sm.targetMap[masterTargetID]
	for pid := range slaves {
		if mapped != nil && mapped[pid] != "" {
			delete(slaves, pid)
		}
	}
	sm.targetMapMutex.RUnlock()
	if len(slaves) == 0 {
		return
	}

	masterTabs, err := masterCDP.browserTabsSnapshot()
	if err != nil {
		syncDebugf("[TabRoute] master tab snapshot failed: %v\n", err)
		return
	}
	masterTab, ok := browserTabByTargetID(masterTabs, masterTargetID)
	if !ok {
		return
	}

	for pid, slaveCDP := range slaves {
		slaveTabs, err := slaveCDP.browserTabsSnapshot()
		if err != nil {
			syncDebugf("[TabRoute] slave tab snapshot failed PID=%d: %v\n", pid, err)
			continue
		}
		slaveTab, ok := browserTabByIndex(slaveTabs, masterTab.Index)
		if !ok {
			continue
		}
		if slaveCDP.getSessionIDByTargetID(slaveTab.TargetID) == "" {
			if _, err := slaveCDP.ensureTargetAttached(slaveTab.TargetID, "page", slaveTab.URL); err != nil {
				continue
			}
		}
		sm.targetMapMutex.Lock()
		if _, ok := sm.targetMap[masterTargetID]; !ok {
			sm.targetMap[masterTargetID] = make(map[int]string)
		}
		sm.targetMap[masterTargetID][pid] = slaveTab.TargetID
		sm.targetMapMutex.Unlock()
	}
}

// mapInitialTargets pairs pre-existing targets in Chrome's DevTools list order.
// Targets returned by an external tab operation take precedence over Chrome's
// asynchronously updated visibility state.
func (sm *SyncManager) mapInitialTargets(preferredTargetIDs map[int]string) error {
	sm.mu.RLock()
	masterCDP := sm.masterCDP
	masterPID := int(sm.masterWindow)
	slaves := make(map[int]*CDPClient, len(sm.slaveCDPs))
	for pid, cdp := range sm.slaveCDPs {
		if cdp != nil && cdp.isConnected {
			slaves[pid] = cdp
		}
	}
	sm.mu.RUnlock()
	if masterCDP == nil {
		return fmt.Errorf("master CDP is unavailable")
	}
	masterTabs, err := masterCDP.browserTabsSnapshot()
	if err != nil {
		return fmt.Errorf("master tab snapshot failed: %w", err)
	}
	if len(masterTabs) == 0 {
		return fmt.Errorf("master has no comparable page target")
	}
	for _, tab := range masterTabs {
		if masterCDP.getSessionIDByTargetID(tab.TargetID) == "" {
			_, _ = masterCDP.ensureTargetAttached(tab.TargetID, "page", tab.URL)
		}
	}
	masterActive, err := selectActiveBrowserTab(masterTabs, preferredTargetIDs[masterPID])
	if err != nil {
		return fmt.Errorf("master active target selection failed: %w", err)
	}

	type selectedSlaveTab struct {
		cdp *CDPClient
		tab browserTabDescriptor
	}
	selectedSlaves := make(map[int]selectedSlaveTab, len(slaves))
	refreshedMap := make(map[string]map[int]string, len(masterTabs))
	for _, masterTab := range masterTabs {
		refreshedMap[masterTab.TargetID] = make(map[int]string)
	}

	for slavePID, slaveCDP := range slaves {
		slaveTabs, err := slaveCDP.browserTabsSnapshot()
		if err != nil {
			return fmt.Errorf("slave PID %d tab snapshot failed: %w", slavePID, err)
		}
		if len(slaveTabs) == 0 {
			return fmt.Errorf("slave PID %d has no comparable page target", slavePID)
		}
		for _, slaveTab := range slaveTabs {
			if slaveCDP.getSessionIDByTargetID(slaveTab.TargetID) == "" {
				_, _ = slaveCDP.ensureTargetAttached(slaveTab.TargetID, "page", slaveTab.URL)
			}
		}
		slaveActive, err := selectActiveBrowserTab(slaveTabs, preferredTargetIDs[slavePID])
		if err != nil {
			return fmt.Errorf("slave PID %d active target selection failed: %w", slavePID, err)
		}
		selectedSlaves[slavePID] = selectedSlaveTab{cdp: slaveCDP, tab: slaveActive}
		for _, masterTab := range masterTabs {
			slaveTab, ok := browserTabByIndex(slaveTabs, masterTab.Index)
			if !ok {
				continue
			}
			refreshedMap[masterTab.TargetID][slavePID] = slaveTab.TargetID
			syncDebugf("[TabRoute] mapped initial index=%d master=%s slavePID=%d slave=%s\n", masterTab.Index, masterTab.TargetID, slavePID, slaveTab.TargetID)
		}
		// The visible pair is authoritative even when the windows started with
		// different background-tab counts.
		refreshedMap[masterActive.TargetID][slavePID] = slaveActive.TargetID
	}

	// Publish current page mappings before active-session callbacks can observe
	// them. Other entries may belong to a still-open extension target and must
	// survive a page-only tab operation.
	sm.targetMapMutex.Lock()
	for masterTargetID, slaveMap := range refreshedMap {
		sm.targetMap[masterTargetID] = slaveMap
	}
	sm.targetMapMutex.Unlock()

	masterCDP.applyBrowserTab(masterActive)
	for _, selected := range selectedSlaves {
		selected.cdp.applyBrowserTab(selected.tab)
	}
	sm.resetPageInputRoute()
	return nil
}

// GetActiveTargetID returns the active TargetID for a given process ID
func (sm *SyncManager) GetActiveTargetID(pid int) string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Get master's active target
	var masterTarget string
	if sm.masterCDP != nil {
		sm.masterCDP.mu.Lock()
		masterTarget = sm.masterCDP.sessionTargets[sm.masterCDP.activeSessionID]
		sm.masterCDP.mu.Unlock()
	}

	// If asking for master's pid
	if int(sm.masterWindow) == pid {
		return masterTarget
	}

	// If asking for slave's pid
	if _, ok := sm.slaveCDPs[pid]; ok {
		if masterTarget != "" {
			sm.targetMapMutex.RLock()
			defer sm.targetMapMutex.RUnlock()
			if slaveMap, ok := sm.targetMap[masterTarget]; ok {
				if mappedTarget, exists := slaveMap[pid]; exists {
					return mappedTarget
				}
			}
		}

		return ""
	}

	return ""
}

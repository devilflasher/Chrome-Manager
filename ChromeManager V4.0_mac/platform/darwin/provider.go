//go:build darwin

package darwin

import (
	"chromemanager/platform/common"
	"chromemanager/utils"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

typedef struct {
    double x;
    double y;
    double w;
    double h;
    double wx;
    double wy;
    double ww;
    double wh;
    double scale;
    int isPrimary;
} CDuoScreenData;

static int GetScreenCount() {
    return (int)[[NSScreen screens] count];
}

static CDuoScreenData GetScreenData(int index) {
    NSArray<NSScreen*> *screens = [NSScreen screens];
    CDuoScreenData data = {0};

    if (index < [screens count]) {
        NSScreen *screen = screens[index];
        NSRect frame = [screen frame];
        NSRect visibleFrame = [screen visibleFrame];
        NSScreen *primaryScreen = screens[0];
        CGFloat primaryHeight = [primaryScreen frame].size.height;

        // Convert to Top-Left coordinate system
        data.x = frame.origin.x;
        data.y = primaryHeight - (frame.origin.y + frame.size.height);
        data.w = frame.size.width;
        data.h = frame.size.height;

        data.wx = visibleFrame.origin.x;
        data.wy = primaryHeight - (visibleFrame.origin.y + visibleFrame.size.height);
        data.ww = visibleFrame.size.width;
        data.wh = visibleFrame.size.height;
        data.scale = [screen backingScaleFactor];
        data.isPrimary = (index == 0) ? 1 : 0;
    }
    return data;
}

static const char* GetScreenName(int index) {
    NSArray<NSScreen*> *screens = [NSScreen screens];
    if (index < [screens count]) {
        return [[screens[index] localizedName] UTF8String];
    }
    return "";
}

// KeyCode Constants mapping for CGO usage if needed (or do it in Go)
*/
import "C"

var clipboardMutex sync.Mutex

// Provider implements standard PlatformProvider for macOS
type Provider struct {
	syncManager *SyncManager
}

// NewProvider creates a new macOS provider
func NewProvider() (common.PlatformProvider, error) {
	provider := &Provider{
		syncManager: NewSyncManager(),
	}

	// Check permissions on startup
	if !CheckAccessibilityPermission(false) {
		fmt.Println("⚠️ 警告: ChromeManager 缺少辅助功能权限！键盘同步将无法工作。请在 系统设置 > 隐私与安全性 > 辅助功能 中授权 ChromeManager (或终端)。")
	}

	return provider, nil
}

// SetAppWindowPosition implements PlatformProvider
// Android: 使用 AX API (Accessibility) 设置自身窗口位置
// 忽略 hwnd 参数，直接使用当前进程 PID
func (p *Provider) SetAppWindowPosition(hwnd uintptr, rect common.Rect) error {
	pid := int32(os.Getpid())
	SetWindowPosition(pid, rect.Left, rect.Top, rect.Width, rect.Height)
	return nil
}

// GetAppWindowPosition implements PlatformProvider
// 使用 Native Cocoa API 获取自身窗口位置
func (p *Provider) GetAppWindowPosition(hwnd uintptr) (common.Rect, error) {
	// pid := int32(os.Getpid()) // PID ignored for GetMainWindowPosition
	rect, err := GetMainWindowPosition()
	if err != nil {
		return common.Rect{}, err
	}

	return rect, nil
}

// NativeMinimize minimizes the main window using Native Cocoa API
func (p *Provider) NativeMinimize() {
	NativeMinimize()
}

// NativeTerminate terminates the application using Native Cocoa API
func (p *Provider) NativeTerminate() {
	NativeTerminate()
}

// NativeTerminatePID terminates a specific process ID using Native Cocoa API (Graceful)
func (p *Provider) NativeTerminatePID(pid int32) {
	NativeTerminatePID(pid)
}

var _ common.PlatformProvider = (*Provider)(nil)

// =============================================================================
// WindowProvider Implementation
// =============================================================================

func (p *Provider) FindWindow(className, windowName string) (common.WindowHandle, error) {
	// Not easily supported by "Class Name" on macOS.
	// Plan B relies on PIDs managed by us.
	return 0, common.ErrNotImplemented
}

func (p *Provider) EnumWindows(callback func(common.WindowHandle) bool) error {
	// Not implemented generically. Use EnumChromeWindows for specific needs.
	return common.ErrNotImplemented
}

func (p *Provider) GetWindowInfo(handle common.WindowHandle) (*common.WindowInfo, error) {
	val := int(handle)
	if val > 100000 {
		rect, err := NativeGetWindowRect(uintptr(handle))
		if err != nil {
			return nil, err
		}
		return &common.WindowInfo{
			Handle:    handle,
			HWND:      uintptr(handle),
			Position:  rect,
			IsVisible: true,
		}, nil
	}

	pid := int32(handle)
	info, err := mainWindowForPID(pid)
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (p *Provider) GetWindowTitle(handle common.WindowHandle) (string, error) {
	info, err := p.GetWindowInfo(handle)
	if err != nil {
		return "", err
	}
	return info.Title, nil
}

func (p *Provider) SetWindowTitle(handle common.WindowHandle, title string) error {
	return common.ErrNotImplemented
}

func (p *Provider) GetWindowRect(handle common.WindowHandle) (*common.Rect, error) {
	info, err := p.GetWindowInfo(handle)
	if err != nil {
		return nil, err
	}
	return &info.Position, nil
}

func (p *Provider) SetForegroundWindow(handle common.WindowHandle) error {
	return p.BringWindowToTop(handle)
}

func (p *Provider) SetChromeNativeZoomOnPort(port int, level int) (map[string]interface{}, error) {
	var result NativeZoomResult
	var err error

	if p.syncManager != nil && p.syncManager.IsRunning() {
		result, err = p.syncManager.SetChromeNativeZoomOnPort(port, level)
		if err == nil {
			return nativeZoomResultMap(result), nil
		}
	}

	cdp := NewCDPClient(port)
	if err := cdp.Connect(); err != nil {
		return nil, err
	}
	defer cdp.Close()

	result, err = cdp.SetChromeNativeZoomLevel(level)
	if err != nil {
		return nil, err
	}
	return nativeZoomResultMap(result), nil
}

func nativeZoomResultMap(result NativeZoomResult) map[string]interface{} {
	return map[string]interface{}{
		"zoom":     result.Zoom,
		"mode":     result.Mode,
		"tabId":    result.TabID,
		"windowId": result.WindowID,
		"url":      result.URL,
	}
}

func (p *Provider) GetForegroundWindow() (common.WindowHandle, error) {
	pid := GetFrontmostPID() // From sync_cgo.go
	if pid == 0 {
		return 0, fmt.Errorf("no foreground window found")
	}
	return common.WindowHandle(pid), nil
}

func (p *Provider) IsWindowVisible(handle common.WindowHandle) (bool, error) {
	// Valid check: assume yes if valid pid and running
	info, err := p.GetWindowInfo(handle)
	if err != nil {
		return false, err
	}
	return info.IsVisible, nil
}

func (p *Provider) IsWindowValid(handle common.WindowHandle) (bool, error) {
	_, err := p.GetWindowInfo(handle)
	return err == nil, nil
}

func (p *Provider) GetWindowProcessID(handle common.WindowHandle) (int32, error) {
	return int32(handle), nil
}

func (p *Provider) GetWindowClassName(handle common.WindowHandle) (string, error) {
	return "Chrome_WidgetWin_1", nil // Fake it for compatibility if needed
}

func (p *Provider) GetScreensInfo() ([]common.ScreenInfo, error) {
	count := int(C.GetScreenCount())
	var screens []common.ScreenInfo

	for i := 0; i < count; i++ {
		data := C.GetScreenData(C.int(i))
		name := C.GoString(C.GetScreenName(C.int(i)))
		if name == "" {
			name = fmt.Sprintf("Display %d", i+1)
		}
		if data.isPrimary == 1 {
			name += " (Primary)"
		}

		screen := common.ScreenInfo{
			Name:       name,
			DeviceName: name,
			Primary:    data.isPrimary == 1,
			Bounds: common.Rect{
				Left:   int(data.x),
				Top:    int(data.y),
				Width:  int(data.w),
				Height: int(data.h),
			},
			WorkArea: common.Rect{
				Left:   int(data.wx),
				Top:    int(data.wy),
				Width:  int(data.ww),
				Height: int(data.wh),
			},
			Left:   int(data.x),
			Top:    int(data.y),
			Width:  int(data.w),
			Height: int(data.h),
			Scale:  float64(data.scale),
		}
		screens = append(screens, screen)
	}
	return screens, nil
}

func (p *Provider) GetPrimaryScreen() (*common.ScreenInfo, error) {
	screens, err := p.GetScreensInfo()
	if err != nil {
		return nil, err
	}
	for _, screen := range screens {
		if screen.Primary {
			return &screen, nil
		}
	}
	if len(screens) > 0 {
		return &screens[0], nil
	}
	return nil, fmt.Errorf("no screens found")
}

func (p *Provider) PostMessage(handle common.WindowHandle, msg uint32, wParam, lParam uintptr) error {
	return common.ErrFeatureNotSupported
}

func (p *Provider) EnumChromeWindows() ([]common.WindowInfo, error) {
	// 0. Check Permissions
	if !CheckAccessibilityPermission(false) { // From accessibility_utils.go
		return nil, fmt.Errorf("辅助功能权限被拒绝。如果您已经在系统设置中勾选了 ChromeManager 但仍然看到此提示，请尝试以下操作：\n1. 在 '系统设置 > 隐私与安全性 > 辅助功能' 中，点击右下角的 '-' 号移除所有 ChromeManager 记录，然后重启软件重新授权。\n2. 或者在终端运行：tccutil reset Accessibility com.chromemanager.app")
	}

	// 1. Get All Processes
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}

	selfPID := int32(os.Getpid())
	var results []common.WindowInfo

	// 2. Iterate
	for _, proc := range procs {
		if proc.Pid == selfPID {
			continue
		}
		name, err := proc.Name()
		if err != nil {
			continue
		}

		if strings.Contains(name, "Chrome") {
			pid := proc.Pid
			cmdLine, err := GetProcessCommandLine(pid) // From process_utils.go
			if err != nil || strings.Contains(cmdLine, "--type=") {
				continue
			}

			userDataDir := utils.ExtractUserDataDir(cmdLine)
			debugPort := utils.ExtractDebugPort(cmdLine)

			windows, err := EnumWindowsForPID(pid) // From accessibility_utils.go
			if err != nil {
				continue
			}

			var bestWindow *common.WindowInfo
			for _, w := range windows {
				w.UserDataDir = userDataDir
				w.DebugPort = debugPort

				baseDir := filepath.Base(userDataDir)
				if strings.HasPrefix(baseDir, "ChromeProfile_") {
					numStr := strings.TrimPrefix(baseDir, "ChromeProfile_")
					if n, err := strconv.Atoi(numStr); err == nil {
						w.Number = n
					}
				} else if n, err := strconv.Atoi(baseDir); err == nil {
					w.Number = n
				}

				if w.Number > 0 {
					if bestWindow == nil || preferChromeMainWindow(w, *bestWindow) {
						copyWindow := w
						bestWindow = &copyWindow
					}
				}
			}
			if bestWindow != nil {
				results = append(results, *bestWindow)
			}
		}
	}
	return results, nil
}

func preferChromeMainWindow(candidate, current common.WindowInfo) bool {
	candidateTitle := strings.TrimSpace(candidate.Title)
	currentTitle := strings.TrimSpace(current.Title)
	candidateBrowserTitle := isChromeBrowserWindowTitle(candidateTitle)
	currentBrowserTitle := isChromeBrowserWindowTitle(currentTitle)
	if candidateBrowserTitle != currentBrowserTitle {
		return candidateBrowserTitle
	}
	if candidateTitle != "" && currentTitle == "" {
		return true
	}
	if candidateTitle == "" && currentTitle != "" {
		return false
	}
	candidateArea := candidate.Position.Width * candidate.Position.Height
	currentArea := current.Position.Width * current.Position.Height
	return candidateArea > currentArea
}

func isChromeBrowserWindowTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	return strings.Contains(title, "google chrome") || strings.Contains(title, "chromium")
}

func mainWindowForPID(pid int32) (*common.WindowInfo, error) {
	windows, err := EnumWindowsForPID(pid)
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("window not found for pid %d", pid)
	}

	best := windows[0]
	for _, w := range windows[1:] {
		if preferChromeMainWindow(w, best) {
			best = w
		}
	}
	return &best, nil
}

func (p *Provider) IsValidChromeWindow(handle common.WindowHandle) (bool, error) {
	return p.IsWindowValid(handle)
}

func (p *Provider) GetChromePopups(chromeHandle common.WindowHandle) ([]common.WindowHandle, error) {
	pid := int32(chromeHandle)
	windows, err := EnumWindowsForPID(pid)
	if err != nil {
		return nil, err
	}
	if len(windows) == 0 {
		return nil, nil
	}

	mainRect := windows[0].Position
	mainArea := mainRect.Width * mainRect.Height
	for _, info := range windows[1:] {
		area := info.Position.Width * info.Position.Height
		if area > mainArea {
			mainRect = info.Position
			mainArea = area
		}
	}

	popups := make([]common.WindowHandle, 0)
	for _, info := range windows {
		if info.HWND == 0 || !isPopupCandidateRect(info.Position, mainRect) {
			continue
		}
		popups = append(popups, common.WindowHandle(info.HWND))
	}
	return popups, nil
}

func (p *Provider) BuildChromeCommand(targetPath, arguments string, number int, userDataDir string, debugPort int) []string {
	cmd := []string{targetPath}
	if userDataDir != "" {
		cmd = append(cmd, "--user-data-dir="+userDataDir)
	}
	if debugPort > 0 {
		cmd = append(cmd, fmt.Sprintf("--remote-debugging-port=%d", debugPort))
		cmd = append(cmd, "--remote-allow-origins=*")
	}
	if arguments != "" {
		args := strings.Split(arguments, " ")
		cmd = append(cmd, args...)
	}
	return cmd
}

func (p *Provider) IsProcessRunning(pid int32) bool {
	exists, _ := process.PidExists(pid)
	return exists
}

func (p *Provider) FindWindowByPID(pid int32) (common.WindowHandle, error) {
	if p.IsProcessRunning(pid) {
		return common.WindowHandle(pid), nil
	}
	return 0, common.ErrWindowNotFound
}

func (p *Provider) SHChangeNotify() error {
	return nil
}

func (p *Provider) IsChromeDebugPortReady(debugPort int) bool {
	// Plan B: Debug Port not strictly required for Sync, but maybe for Login Check.
	// We return true immediately.
	return true
}

func (p *Provider) WaitForChromeWindowReady(handle common.WindowHandle, pid int32, debugPort int, maxWaitTime time.Duration) error {
	start := time.Now()
	for {
		if time.Since(start) > maxWaitTime {
			return fmt.Errorf("timeout waiting for window")
		}
		if _, err := p.GetWindowInfo(handle); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *Provider) TitleSimilarity(title1, title2 string) float64 {
	// Simplify: Exact match or simple prefix
	if title1 == title2 {
		return 1.0
	}
	if strings.Contains(title1, title2) || strings.Contains(title2, title1) {
		return 0.8
	}
	return 0.0
}

// ----- SyncProvider 代理方法结束 -----

// ExecuteBatchInput simulates typing batch text via CDP (macOS) or Native (Windows fallback).
func (p *Provider) ExecuteBatchInput(pids []int, text string, delayed bool, overwrite bool) {
	if p.syncManager != nil {
		p.syncManager.ExecuteBatchInput(pids, text, delayed, overwrite)
	}
}

// GetActiveTargetID 获取指定进程的活动 TargetID
func (p *Provider) GetActiveTargetID(pid int) string {
	if p.syncManager == nil {
		return ""
	}
	return p.syncManager.GetActiveTargetID(pid)
}

func (p *Provider) BeginProgrammaticMasterTarget(url string) {
	if p.syncManager != nil {
		p.syncManager.BeginProgrammaticMasterTarget(url)
	}
}

func (p *Provider) ConfirmProgrammaticMasterTarget(targetID string) {
	if p.syncManager != nil {
		p.syncManager.ConfirmProgrammaticMasterTarget(targetID)
	}
}

func (p *Provider) CancelProgrammaticMasterTarget() {
	if p.syncManager != nil {
		p.syncManager.CancelProgrammaticMasterTarget()
	}
}

func (p *Provider) MapProgrammaticPageTargets(masterTargetID, targetURL string, targetIDs map[int]string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager is unavailable")
	}
	return p.syncManager.MapProgrammaticPageTargets(masterTargetID, targetURL, targetIDs)
}

func (p *Provider) RefreshPageTargetMappings(preferredTargetIDs map[int]string) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager is unavailable")
	}
	return p.syncManager.RefreshPageTargetMappings(preferredTargetIDs)
}

// =============================================================================
// SyncProvider Implementation
// =============================================================================

func (p *Provider) Start(masterWindow common.WindowHandle, slaveWindows []common.WindowHandle) error {
	return p.syncManager.Start(masterWindow, slaveWindows)
}

func (p *Provider) Stop() error {
	return p.syncManager.Stop()
}

func (p *Provider) IsRunning() bool {
	return p.syncManager.IsRunning()
}

func (p *Provider) GetState() common.SyncState {
	return p.syncManager.GetState()
}

func (p *Provider) SetConfig(config common.SyncConfig) error {
	return p.syncManager.SetConfig(config)
}

func (p *Provider) GetConfig() common.SyncConfig {
	return p.syncManager.GetConfig()
}

func (p *Provider) PauseSync() error {
	return p.syncManager.PauseSync()
}

func (p *Provider) ResumeSync() error {
	return p.syncManager.ResumeSync()
}

// =============================================================================
// HotkeyProvider Implementation
// =============================================================================

func (p *Provider) Register(id int, spec common.HotkeySpec, callback func()) error {
	return registerMacHotkey(id, spec, callback)
}

func (p *Provider) Unregister(id int) error {
	return unregisterMacHotkey(id)
}

func (p *Provider) IsRegistered(id int) bool {
	return isMacHotkeyRegistered(id)
}

func (p *Provider) Shutdown() error {
	shutdownMacHotkeys()
	return p.Stop()
}

func (p *Provider) ParseHotkeySpec(hotkeyStr string) (common.HotkeySpec, error) {
	trimmed := strings.TrimSpace(hotkeyStr)
	if trimmed == "" {
		return common.HotkeySpec{}, fmt.Errorf("hotkey is empty")
	}

	parts := strings.Split(trimmed, "+")
	var modifiers common.KeyModifier
	var key common.KeyCode

	for _, raw := range parts {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}

		upper := strings.ToUpper(token)
		switch upper {
		case "CTRL", "CONTROL", "CTL":
			modifiers |= common.ModControl
			continue
		case "ALT", "MENU", "OPTION":
			modifiers |= common.ModAlt
			continue
		case "SHIFT":
			modifiers |= common.ModShift
			continue
		case "WIN", "WINDOWS", "SUPER", "CMD", "COMMAND", "META":
			modifiers |= common.ModWin
			continue
		case "SPACE":
			key = common.KeySpace
			continue
		case "ENTER", "RETURN":
			key = common.KeyEnter
			continue
		case "ESC", "ESCAPE":
			key = common.KeyEsc
			continue
		case "TAB":
			key = common.KeyTab
			continue
		case "BACKSPACE", "BACK":
			key = common.KeyBack
			continue
		}

		if len(upper) == 1 {
			key = common.KeyCode(upper[0])
		} else {
			// Fallback
			key = common.KeyCode(0)
		}
	}

	if key == 0 {
		return common.HotkeySpec{}, fmt.Errorf("missing or unsupported main key in: %s", hotkeyStr)
	}

	return common.HotkeySpec{
		Modifiers: modifiers,
		Key:       key,
	}, nil
}

// =============================================================================
// InputProvider Implementation
// =============================================================================

func (p *Provider) SendKeyInput(keyCode common.KeyCode, keyDown bool) error {
	pid := GetFrontmostPID()
	if pid == 0 {
		return fmt.Errorf("no application is focused")
	}

	// Map common.KeyCode to CGKeyCode (Mac)
	macKeyCode := mapCommonToMacKey(keyCode)

	// PostEventToProcess (wraps CGEventPostToPid)
	eventType := 10 // kCGEventKeyDown
	if !keyDown {
		eventType = 11 // kCGEventKeyUp
	}

	PostEventToProcess(pid, eventType, 0, 0, int(macKeyCode), 0)
	return nil
}

func (p *Provider) SendKeyPress(keyCode common.KeyCode) error {
	p.SendKeyInput(keyCode, true)
	time.Sleep(10 * time.Millisecond)
	p.SendKeyInput(keyCode, false)
	return nil
}

func (p *Provider) SendTextInput(text string) error {
	// Fallback to Clipboard paste
	p.SetClipboard(text)

	// Wait for Clipboard to update (approximated by sleep)
	time.Sleep(100 * time.Millisecond)

	// Send Cmd+V
	// Meta=Cmd
	// V=9
	pid := GetFrontmostPID()
	if pid == 0 {
		return fmt.Errorf("no frontmost application found")
	}

	// Cmd Down
	PostEventToProcess(pid, 12, 0, 0, 55, 0x100108) // FlagsChanged (Cmd) - 55 is Cmd, Mask 0x100000 or similar
	// Actually, PostEventToProcess wrapper in sync_cgo takes (type, x, y, keyCode, modifiers).
	// For KeyDown (10), we pass modifiers.

	// Correct sequence for Cmd+V using our wrapper:
	// PostEventToProcess(pid, type, x, y, keyCode, modifiers)

	// Cmd+V Down
	PostEventToProcess(pid, 10, 0, 0, 9, 0x100000)
	time.Sleep(50 * time.Millisecond)
	// Cmd+V Up
	PostEventToProcess(pid, 11, 0, 0, 9, 0x100000)

	return nil
}

// SendTextInputToPID sends text input to a specific process ID (for batch input)
func (p *Provider) SendTextInputToPID(pid int, text string) error {
	if pid == 0 {
		return fmt.Errorf("invalid pid")
	}

	// CRITICAL: On macOS, keyboard events only work on the FOREGROUND window.
	// We MUST activate the target window first.
	p.SetForegroundWindow(common.WindowHandle(pid))
	time.Sleep(150 * time.Millisecond) // Wait for window activation

	// Set Clipboard
	p.SetClipboard(text)
	time.Sleep(50 * time.Millisecond) // Wait for clipboard

	// Cmd+V Down (keyCode 9 = 'V', modifiers 0x100000 = Cmd)
	PostEventToProcess(pid, 10, 0, 0, 9, 0x100000)
	time.Sleep(30 * time.Millisecond)
	// Cmd+V Up
	PostEventToProcess(pid, 11, 0, 0, 9, 0x100000)

	return nil
}

func (p *Provider) SendMouseClick(x, y int, button common.MouseButton) error {
	pid := GetFrontmostPID()

	// 1: LeftMouseDown, 2: LeftMouseUp
	// 3: RightMouseDown, 4: RightMouseUp

	downType := 1
	upType := 2

	if button == common.MouseRight {
		downType = 3
		upType = 4
	}

	PostEventToProcess(pid, downType, x, y, 0, 0)
	time.Sleep(50 * time.Millisecond)
	PostEventToProcess(pid, upType, x, y, 0, 0)
	return nil
}

func (p *Provider) MoveMouse(x, y int) error {
	pid := GetFrontmostPID()
	// 5 = kCGEventMouseMoved
	PostEventToProcess(pid, 5, x, y, 0, 0)
	return nil
}

func (p *Provider) SendMouseWheel(delta int) error {
	pid := GetFrontmostPID()
	// 22 = kCGEventScrollWheel
	PostEventToProcess(pid, 22, 0, 0, delta, 0)
	return nil
}

func (p *Provider) SendCapturedTextToPID(pid int, text string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid")
	}
	if err := p.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		return err
	}
	if err := p.SetClipboard(text); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	PostEventToProcessCaptured(pid, 10, 0, 0, 9, 0x100000) // Cmd+V down
	time.Sleep(30 * time.Millisecond)
	PostEventToProcessCaptured(pid, 11, 0, 0, 9, 0x100000) // Cmd+V up
	return nil
}

func (p *Provider) SendCapturedKeystrokeToPID(pid int, keys string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid")
	}
	keyCode, modifiers, err := parseCapturedMacKeystroke(keys)
	if err != nil {
		return err
	}
	if err := p.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	PostEventToProcessCaptured(pid, 10, 0, 0, keyCode, modifiers)
	time.Sleep(30 * time.Millisecond)
	PostEventToProcessCaptured(pid, 11, 0, 0, keyCode, modifiers)
	return nil
}

func (p *Provider) SendCapturedMouseClickToPID(pid int, x, y int, button string) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid")
	}
	if err := p.SetForegroundWindow(common.WindowHandle(pid)); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)

	downType, upType := 1, 2
	switch strings.ToLower(strings.TrimSpace(button)) {
	case "", "left":
	case "right":
		downType, upType = 3, 4
	case "middle":
		return fmt.Errorf("middle click is not supported on macOS API")
	default:
		return fmt.Errorf("unsupported mouse button: %s", button)
	}

	PostEventToProcessCaptured(pid, downType, x, y, 0, 0)
	time.Sleep(40 * time.Millisecond)
	PostEventToProcessCaptured(pid, upType, x, y, 0, 0)
	return nil
}

func (p *Provider) SetClipboard(text string) error {
	clipboardMutex.Lock()
	defer clipboardMutex.Unlock()

	// Use echo | pbcopy
	cmd := exec.Command("pbcopy")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	go func() {
		defer in.Close()
		in.Write([]byte(text))
	}()
	return cmd.Run()
}

func (p *Provider) GetClipboard() (string, error) {
	clipboardMutex.Lock()
	defer clipboardMutex.Unlock()

	cmd := exec.Command("pbpaste")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (p *Provider) InputRandomNumber(handle common.WindowHandle, config common.InputConfig) error {
	return common.ErrNotImplemented
}

// =============================================================================
// Helper Functions
// =============================================================================

func mapCommonToMacKey(k common.KeyCode) int {
	// Simple mapping for common keys
	// 0x00 = A, 0x01 = S, ...
	// 0x31 = Space
	// 0x24 = Return
	// This needs a full table, here is a tiny subset
	switch k {
	case common.KeyEnter:
		return 0x24
	case common.KeySpace:
		return 0x31
	case common.KeyEsc:
		return 0x35
	case common.KeyTab:
		return 0x30
	case common.KeyBack:
		return 0x33
	}
	return 0 // Default 'A' or error
}

func parseCapturedMacKeystroke(keys string) (int, int, error) {
	keys = strings.TrimSpace(keys)
	if keys == "" {
		return 0, 0, fmt.Errorf("keys is empty")
	}

	modifiers := 0
	keyCode := -1
	for _, rawPart := range strings.Split(keys, "+") {
		part := strings.ToLower(strings.TrimSpace(rawPart))
		if part == "" {
			continue
		}
		switch part {
		case "cmd", "command", "meta", "win", "super":
			modifiers |= 0x100000
			continue
		case "ctrl", "control", "ctl":
			modifiers |= 0x040000
			continue
		case "shift":
			modifiers |= 0x020000
			continue
		case "alt", "option":
			modifiers |= 0x080000
			continue
		}
		code, ok := macVirtualKeyCodeFromName(part)
		if !ok {
			return 0, 0, fmt.Errorf("unsupported key: %s", rawPart)
		}
		keyCode = code
	}
	if keyCode < 0 {
		return 0, 0, fmt.Errorf("missing key in: %s", keys)
	}
	return keyCode, modifiers, nil
}

func macVirtualKeyCodeFromName(key string) (int, bool) {
	switch key {
	case "a":
		return 0, true
	case "s":
		return 1, true
	case "d":
		return 2, true
	case "f":
		return 3, true
	case "h":
		return 4, true
	case "g":
		return 5, true
	case "z":
		return 6, true
	case "x":
		return 7, true
	case "c":
		return 8, true
	case "v":
		return 9, true
	case "b":
		return 11, true
	case "q":
		return 12, true
	case "w":
		return 13, true
	case "e":
		return 14, true
	case "r":
		return 15, true
	case "y":
		return 16, true
	case "t":
		return 17, true
	case "1":
		return 18, true
	case "2":
		return 19, true
	case "3":
		return 20, true
	case "4":
		return 21, true
	case "6":
		return 22, true
	case "5":
		return 23, true
	case "=", "equal", "equals":
		return 24, true
	case "9":
		return 25, true
	case "7":
		return 26, true
	case "-", "minus":
		return 27, true
	case "8":
		return 28, true
	case "0":
		return 29, true
	case "]", "bracketright":
		return 30, true
	case "o":
		return 31, true
	case "u":
		return 32, true
	case "[", "bracketleft":
		return 33, true
	case "i":
		return 34, true
	case "p":
		return 35, true
	case "enter", "return":
		return 36, true
	case "l":
		return 37, true
	case "j":
		return 38, true
	case "'", "quote":
		return 39, true
	case "k":
		return 40, true
	case ";", "semicolon":
		return 41, true
	case "\\", "backslash":
		return 42, true
	case ",", "comma":
		return 43, true
	case "/", "slash":
		return 44, true
	case "n":
		return 45, true
	case "m":
		return 46, true
	case ".", "period":
		return 47, true
	case "tab":
		return 48, true
	case "space":
		return 49, true
	case "`", "backquote":
		return 50, true
	case "backspace", "back":
		return 51, true
	case "esc", "escape":
		return 53, true
	case "delete", "del":
		return 117, true
	case "left", "arrowleft":
		return 123, true
	case "right", "arrowright":
		return 124, true
	case "down", "arrowdown":
		return 125, true
	case "up", "arrowup":
		return 126, true
	default:
		return 0, false
	}
}

// FindBrowserPath 查找浏览器的安装路径
func (p *Provider) FindBrowserPath() (string, error) {
	// 常见安装路径
	commonPaths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		filepath.Join(os.Getenv("HOME"), "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
	}

	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("Google Chrome not found in common paths")
}

func (p *Provider) ParseWindowNumbers(input string) ([]int, error) {
	return utils.ParseWindowNumbers(input)
}

func (p *Provider) ParseShortcutsFromDirectory(dirPath string) ([]common.ShortcutInfo, error) {
	var shortcuts []common.ShortcutInfo
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".app") {
			baseName := strings.TrimSuffix(name, ".app")
			num, err := strconv.Atoi(baseName) // Expect "1.app"
			if err != nil && strings.HasPrefix(baseName, "ChromeProfile_") {
				// Handle "ChromeProfile_1.app"
				numStr := strings.TrimPrefix(baseName, "ChromeProfile_")
				num, err = strconv.Atoi(numStr)
			}

			if err == nil {
				targetPath := filepath.Join(dirPath, name, "Contents", "MacOS", "run")
				shortcuts = append(shortcuts, common.ShortcutInfo{
					Number:     num,
					FilePath:   filepath.Join(dirPath, name),
					TargetPath: targetPath,
					Arguments:  "", // Usually baked in
					WorkingDir: dirPath,
				})
			}
		}
	}
	return shortcuts, nil
}

func (p *Provider) CloseWindowQuickly(handle common.WindowHandle) error {
	val := int(handle)
	if val <= 0 {
		return nil
	}

	// Heuristic: PIDs are usually small (< 100000), Pointers are huge.
	// If it's a large value, treat as AXUIElementRef (uintptr)
	if val > 100000 {
		NativeCloseWindow(uintptr(handle))
		return nil
	}

	// Otherwise treat as PID
	// Use NativeTerminatePID for graceful quit (like Cmd+Q)
	NativeTerminatePID(int32(val))
	return nil
}

func (p *Provider) SetWindowPosition(handle common.WindowHandle, rect common.Rect) error {
	val := int(handle)
	if val <= 0 {
		return common.ErrInvalidHandle
	}

	// Heuristic: If large value, it's an external window handle (AXUIElementRef)
	if val > 100000 {
		NativeSetWindowPosition(uintptr(handle), rect.Left, rect.Top, rect.Width, rect.Height)
		return nil
	}

	pid := int32(val)
	if pid == int32(os.Getpid()) {
		SetWindowPosition(pid, rect.Left, rect.Top, rect.Width, rect.Height)
		return nil
	}

	info, err := mainWindowForPID(pid)
	if err != nil {
		return err
	}
	if info.HWND == 0 {
		return fmt.Errorf("window handle not found for pid %d", pid)
	}
	NativeSetWindowPosition(info.HWND, rect.Left, rect.Top, rect.Width, rect.Height)
	return nil
}

func (p *Provider) BringWindowToTop(handle common.WindowHandle) error {
	val := int(handle)
	if val <= 0 {
		return common.ErrInvalidHandle
	}
	if val > 100000 {
		NativeActivateAndRaiseWindow(uintptr(handle))
		return nil
	}

	pid := int32(val)
	if pid == int32(os.Getpid()) {
		BringWindowToTop(pid)
		return nil
	}

	info, err := mainWindowForPID(pid)
	if err != nil {
		return err
	}
	if info.HWND == 0 {
		return fmt.Errorf("window handle not found for pid %d", pid)
	}
	NativeActivateAndRaiseWindow(info.HWND)
	return nil
}

func (p *Provider) ShowWindow(handle common.WindowHandle, show bool) error {
	if show {
		return p.BringWindowToTop(handle)
	}
	return nil // Cannot hide easily on macOS without Minimized
}

func (p *Provider) CloseWindowGracefully(handle common.WindowHandle, timeout int) error {
	val := int(handle)
	if val <= 0 {
		return nil
	}

	if val > 100000 {
		NativeCloseWindow(uintptr(handle))
		return nil
	}

	pid := int32(val)
	NativeTerminatePID(pid)

	deadline := time.Now().Add(time.Duration(timeout) * 100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !p.IsProcessRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if p.IsProcessRunning(pid) {
		return fmt.Errorf("process %d is still shutting down", pid)
	}
	return nil
}

// Notification/Admin/Dialog Stubs
func (p *Provider) ShowNotification(options common.NotificationOptions) error { return nil }
func (p *Provider) CloseNotification(notificationID string) error             { return nil }
func (p *Provider) IsSupported() bool                                         { return true }
func (p *Provider) CheckAdminPrivileges()                                     {}
func (p *Provider) IsRunningAsAdmin() bool                                    { return true }
func (p *Provider) RequestAdminPrivileges() error                             { return nil }
func (p *Provider) SelectFolderDialog(title string) (string, error)           { return "", nil }
func (p *Provider) SaveFileDialog(title, defaultFileName, fileFilter string) (string, error) {
	return "", nil
}
func (p *Provider) SelectFileDialog(title, fileFilter string) (string, error) {
	// Use AppleScript for native file picker
	script := fmt.Sprintf(`set selectedFile to POSIX path of (choose file with prompt "%s" of type {"public.plain-text", "public.text", "txt", "text"})
return selectedFile`, title)

	cmd := exec.Command("osascript", "-e", script)
	output, err := cmd.Output()
	if err != nil {
		// User cancelled or error
		return "", nil
	}
	return strings.TrimSpace(string(output)), nil
}

// Removed local definitions of redelcared functions.

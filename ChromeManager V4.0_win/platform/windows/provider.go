//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Provider 是Windows平台的PlatformProvider实现。
type Provider struct {
	hotkeyMgr      *hotkeyManager
	hotkeyRegistry map[int]bool // 跟踪已注册的快捷键ID
	hotkeyMu       sync.RWMutex

	// Sync Manager
	syncManager *SyncManager
}

// NewProvider 创建Windows平台的Provider实例。
func NewProvider() (common.PlatformProvider, error) {
	provider := &Provider{
		hotkeyRegistry: make(map[int]bool),
		syncManager:    NewSyncManager(),
	}

	return provider, nil
}

// 编译时检查是否实现了接口
var _ common.PlatformProvider = (*Provider)(nil)

// =============================================================================
// WindowProvider 接口实现
// =============================================================================

// FindWindow 根据类名和窗口名查找窗口。
// 从 utils/ 迁移完成 ✅
func (p *Provider) FindWindow(className, windowName string) (common.WindowHandle, error) {
	hwnd := findWindowW(className, windowName)
	if hwnd == 0 {
		return 0, common.ErrWindowNotFound
	}
	return common.WindowHandle(hwnd), nil
}

// EnumWindows 枚举所有窗口。
// 从 utils/ 迁移完成 ✅
func (p *Provider) EnumWindows(callback func(common.WindowHandle) bool) error {
	return enumWindows(func(hwnd uintptr) bool {
		return callback(common.WindowHandle(hwnd))
	})
}

// GetWindowInfo 获取窗口详细信息。
// 从 utils/ 迁移完成 ✅
func (p *Provider) GetWindowInfo(handle common.WindowHandle) (*common.WindowInfo, error) {
	if handle == 0 {
		return nil, common.ErrInvalidHandle
	}

	hwnd := uintptr(handle)

	// 获取标题
	title := getWindowTextW(hwnd)

	// 获取类名
	className := getClassNameW(hwnd)

	// 获取进程ID
	processID := getWindowThreadProcessID(hwnd)

	// 获取位置
	rect, err := getWindowRect(hwnd)
	if err != nil {
		return nil, fmt.Errorf("failed to get window rect: %w", err)
	}

	// 获取可见性
	visible := isWindowVisible(hwnd)

	return &common.WindowInfo{
		Handle:    handle,
		HWND:      hwnd, // 兼容旧代码
		Title:     title,
		ClassName: className,
		ProcessID: int32(processID),
		Position: common.Rect{
			Left:   int(rect.Left),
			Top:    int(rect.Top),
			Width:  int(rect.Right - rect.Left),
			Height: int(rect.Bottom - rect.Top),
		},
		IsVisible: visible,
	}, nil
}

// GetWindowTitle 获取窗口标题。
// 从 utils.GetWindowText 迁移完成 ✅
func (p *Provider) GetWindowTitle(handle common.WindowHandle) (string, error) {
	if handle == 0 {
		return "", common.ErrInvalidHandle
	}
	title := getWindowTextW(uintptr(handle))
	return title, nil
}

// SetWindowTitle 设置窗口标题。
// 从 utils.SetWindowText 迁移完成 ✅
func (p *Provider) SetWindowTitle(handle common.WindowHandle, title string) error {
	if handle == 0 {
		return common.ErrInvalidHandle
	}
	return setWindowTextW(uintptr(handle), title)
}

// GetWindowRect 获取窗口位置和大小。
// 从 utils.GetWindowRect 迁移完成 ✅
func (p *Provider) GetWindowRect(handle common.WindowHandle) (*common.Rect, error) {
	if handle == 0 {
		return nil, common.ErrInvalidHandle
	}

	rect, err := getWindowRect(uintptr(handle))
	if err != nil {
		return nil, err
	}

	return &common.Rect{
		Left:   int(rect.Left),
		Top:    int(rect.Top),
		Width:  int(rect.Right - rect.Left),
		Height: int(rect.Bottom - rect.Top),
	}, nil
}

// SetWindowPosition 设置窗口位置和大小。
// 从 utils.SetWindowPos 迁移完成 ✅
func (p *Provider) SetWindowPosition(handle common.WindowHandle, rect common.Rect) error {
	if handle == 0 {
		return common.ErrInvalidHandle
	}

	return setWindowPos(
		uintptr(handle),
		rect.Left,
		rect.Top,
		rect.Width,
		rect.Height,
	)
}

// ShowWindow 显示或隐藏窗口。
// 从 utils.ShowWindow 迁移完成 ✅
func (p *Provider) ShowWindow(handle common.WindowHandle, show bool) error {
	if handle == 0 {
		return common.ErrInvalidHandle
	}

	cmdShow := SW_HIDE
	if show {
		cmdShow = SW_SHOW
	}

	return showWindow(uintptr(handle), cmdShow)
}

// SetForegroundWindow 激活窗口。
// 从 utils.SetForegroundWindow 迁移完成 ✅
func (p *Provider) SetForegroundWindow(handle common.WindowHandle) error {
	if handle == 0 {
		return common.ErrInvalidHandle
	}
	return setForegroundWindow(uintptr(handle))
}

// GetForegroundWindow 获取当前前台窗口。
// 新实现 ✅
func (p *Provider) GetForegroundWindow() (common.WindowHandle, error) {
	hwnd := getForegroundWindow()
	if hwnd == 0 {
		return 0, common.ErrWindowNotFound
	}
	return common.WindowHandle(hwnd), nil
}

// IsWindowVisible 检查窗口是否可见。
// 从 utils.IsWindowVisible 迁移完成 ✅
func (p *Provider) IsWindowVisible(handle common.WindowHandle) (bool, error) {
	if handle == 0 {
		return false, common.ErrInvalidHandle
	}
	return isWindowVisible(uintptr(handle)), nil
}

// IsWindowValid 检查窗口句柄是否有效。
// 从 utils.IsWindowValid 迁移完成 ✅
func (p *Provider) IsWindowValid(handle common.WindowHandle) (bool, error) {
	if handle == 0 {
		return false, nil
	}
	return isWindow(uintptr(handle)), nil
}

// GetWindowProcessID 获取窗口所属进程ID。
// 从 utils.GetWindowProcessID 迁移完成 ✅
func (p *Provider) GetWindowProcessID(handle common.WindowHandle) (int32, error) {
	if handle == 0 {
		return 0, common.ErrInvalidHandle
	}
	pid := getWindowThreadProcessID(uintptr(handle))
	if pid == 0 {
		return 0, fmt.Errorf("failed to get process ID for window")
	}
	return int32(pid), nil
}

// GetWindowClassName 获取窗口类名。
// 从 utils.GetClassName 迁移完成 ✅
func (p *Provider) GetWindowClassName(handle common.WindowHandle) (string, error) {
	if handle == 0 {
		return "", common.ErrInvalidHandle
	}
	className := getClassNameW(uintptr(handle))
	return className, nil
}

// GetScreensInfo 获取所有显示器信息
// 从 utils.WindowArranger.GetScreensInfo 迁移完成 ✅
func (p *Provider) GetScreensInfo() ([]common.ScreenInfo, error) {
	var screens []common.ScreenInfo

	// 定义枚举回调函数
	callback := func(hMonitor, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr {
		mi, err := getMonitorInfo(hMonitor)
		if err != nil {
			return 1 // 继续枚举
		}

		isPrimary := (mi.dwFlags & MONITORINFOF_PRIMARY) != 0
		screenName := fmt.Sprintf("屏幕 %d", len(screens)+1)
		if isPrimary {
			screenName += " (主)"
		}

		// 计算分辨率并添加到名称
		width := int(mi.rcMonitor.Right - mi.rcMonitor.Left)
		height := int(mi.rcMonitor.Bottom - mi.rcMonitor.Top)
		screenName += fmt.Sprintf(" - %dx%d", width, height)

		screen := common.ScreenInfo{
			Name:    screenName,
			Primary: isPrimary,
			Bounds: common.Rect{
				Left:   int(mi.rcMonitor.Left),
				Top:    int(mi.rcMonitor.Top),
				Width:  width,
				Height: height,
			},
			WorkArea: common.Rect{
				Left:   int(mi.rcWork.Left),
				Top:    int(mi.rcWork.Top),
				Width:  int(mi.rcWork.Right - mi.rcWork.Left),
				Height: int(mi.rcWork.Bottom - mi.rcWork.Top),
			},
			// 兼容旧代码的字段
			Left:       int(mi.rcMonitor.Left),
			Top:        int(mi.rcMonitor.Top),
			Width:      width,
			Height:     height,
			WorkLeft:   int(mi.rcWork.Left),
			WorkTop:    int(mi.rcWork.Top),
			WorkWidth:  int(mi.rcWork.Right - mi.rcWork.Left),
			WorkHeight: int(mi.rcWork.Bottom - mi.rcWork.Top),
		}

		screens = append(screens, screen)
		return 1 // 继续枚举
	}

	// 枚举所有显示器
	err := enumDisplayMonitors(callback)

	// 如果枚举失败或没有找到显示器，使用备用方法获取主屏幕
	if err != nil || len(screens) == 0 {
		screenWidth := int(getSystemMetrics(SM_CXSCREEN))
		screenHeight := int(getSystemMetrics(SM_CYSCREEN))

		screen := common.ScreenInfo{
			Name:    fmt.Sprintf("主屏幕 - %dx%d", screenWidth, screenHeight),
			Primary: true,
			Bounds: common.Rect{
				Left:   0,
				Top:    0,
				Width:  screenWidth,
				Height: screenHeight,
			},
			WorkArea: common.Rect{
				Left:   0,
				Top:    0,
				Width:  screenWidth,
				Height: screenHeight,
			},
			// 兼容旧代码的字段
			Left:       0,
			Top:        0,
			Width:      screenWidth,
			Height:     screenHeight,
			WorkLeft:   0,
			WorkTop:    0,
			WorkWidth:  screenWidth,
			WorkHeight: screenHeight,
		}
		screens = []common.ScreenInfo{screen}
	}

	return screens, nil
}

// GetPrimaryScreen 获取主显示器信息
// 从 GetScreensInfo 派生实现 ✅
func (p *Provider) GetPrimaryScreen() (*common.ScreenInfo, error) {
	screens, err := p.GetScreensInfo()
	if err != nil {
		return nil, err
	}

	for i := range screens {
		if screens[i].Primary {
			return &screens[i], nil
		}
	}

	// 如果没有找到主屏幕，返回第一个
	if len(screens) > 0 {
		return &screens[0], nil
	}

	return nil, fmt.Errorf("no screens found")
}

// PostMessage 向窗口发送消息。
// 从 utils.PostMessage 迁移完成 ✅
func (p *Provider) PostMessage(handle common.WindowHandle, msg uint32, wParam, lParam uintptr) error {
	if handle == 0 {
		return common.ErrInvalidHandle
	}
	return postMessage(uintptr(handle), msg, wParam, lParam)
}

// EnumChromeWindows 实现在 chrome.go 中 ✅

// IsValidChromeWindow 检查是否为有效的Chrome窗口。
// 部分实现 ✅
func (p *Provider) IsValidChromeWindow(handle common.WindowHandle) (bool, error) {
	if handle == 0 {
		return false, common.ErrInvalidHandle
	}

	className := getClassNameW(uintptr(handle))

	// 检查是否为 Chrome 主窗口类名
	isChrome := strings.Contains(className, "Chrome_WidgetWin") ||
		className == "Chrome_WidgetWin_0" ||
		className == "Chrome_WidgetWin_1"

	return isChrome, nil
}

// =============================================================================
// SyncProvider 接口实现 - TODO: 从syncmanager迁移
// =============================================================================

// Start 启动同步。
// Start 启动同步。
func (p *Provider) Start(masterWindow common.WindowHandle, slaveWindows []common.WindowHandle) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}
	return p.syncManager.Start(masterWindow, slaveWindows)
}

// Stop 停止同步。
func (p *Provider) Stop() error {
	if p.syncManager == nil {
		return nil
	}
	return p.syncManager.Stop()
}

// IsRunning 检查同步是否正在运行。
func (p *Provider) IsRunning() bool {
	if p.syncManager == nil {
		return false
	}
	return p.syncManager.IsRunning()
}

// GetState 获取同步状态信息。
func (p *Provider) GetState() common.SyncState {
	if p.syncManager == nil {
		return common.SyncState{Status: common.SyncStatusError}
	}
	return p.syncManager.GetState()
}

// SetConfig 设置同步配置。
func (p *Provider) SetConfig(config common.SyncConfig) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}
	return p.syncManager.SetConfig(config)
}

// GetConfig 获取当前同步配置。
func (p *Provider) GetConfig() common.SyncConfig {
	if p.syncManager == nil {
		return common.DefaultSyncConfig
	}
	return p.syncManager.GetConfig()
}

// PauseSync 暂停同步。
func (p *Provider) PauseSync() error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}
	return p.syncManager.PauseSync()
}

// ResumeSync 恢复同步。
func (p *Provider) ResumeSync() error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}
	return p.syncManager.ResumeSync()
}

func (p *Provider) SetSyncWindowMetadata(master *common.WindowInfo, slaves []common.WindowInfo) error {
	if p.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}
	return p.syncManager.SetWindowMetadata(master, slaves)
}

func (p *Provider) StartCDPTabSyncAsync(delay time.Duration) {
	if p.syncManager == nil {
		return
	}
	p.syncManager.StartCDPTabSyncAsync(delay)
}

func (p *Provider) WaitForCDPTabSyncReady(timeout time.Duration) bool {
	if p.syncManager == nil {
		return false
	}
	return p.syncManager.WaitForCDPTabSyncReady(timeout)
}

// =============================================================================
// HotkeyProvider 接口实现 - TODO: 从hotkey_manager迁移
// =============================================================================

// Register 注册全局快捷键。
// 从 utils/hotkey_manager.go 迁移完成 ✅
func (p *Provider) Register(id int, spec common.HotkeySpec, callback func()) error {
	// 懒加载快捷键管理器
	if p.hotkeyMgr == nil {
		mgr, err := newHotkeyManager()
		if err != nil {
			return fmt.Errorf("failed to create hotkey manager: %w", err)
		}
		p.hotkeyMgr = mgr
	}

	// 转换为内部格式
	internalSpec := convertFromCommonSpec(spec)

	// 注册快捷键
	if err := p.hotkeyMgr.register(id, internalSpec, callback); err != nil {
		return err
	}

	// 记录已注册的快捷键
	p.hotkeyMu.Lock()
	p.hotkeyRegistry[id] = true
	p.hotkeyMu.Unlock()

	return nil
}

// Unregister 注销快捷键。
// 从 utils/hotkey_manager.go 迁移完成 ✅
func (p *Provider) Unregister(id int) error {
	if p.hotkeyMgr == nil {
		return common.ErrHotkeyNotRegistered
	}

	// 检查是否已注册
	p.hotkeyMu.RLock()
	registered := p.hotkeyRegistry[id]
	p.hotkeyMu.RUnlock()

	if !registered {
		return common.ErrHotkeyNotRegistered
	}

	// 注销快捷键
	if err := p.hotkeyMgr.unregister(id); err != nil {
		return err
	}

	// 从注册表中移除
	p.hotkeyMu.Lock()
	delete(p.hotkeyRegistry, id)
	p.hotkeyMu.Unlock()

	return nil
}

// IsRegistered 检查快捷键是否已注册。
// 从 utils/hotkey_manager.go 迁移完成 ✅
func (p *Provider) IsRegistered(id int) bool {
	p.hotkeyMu.RLock()
	defer p.hotkeyMu.RUnlock()
	return p.hotkeyRegistry[id]
}

// ParseHotkeySpec 解析快捷键字符串。
// 从 utils/hotkey_manager.go 迁移完成 ✅
func (p *Provider) ParseHotkeySpec(hotkeyStr string) (common.HotkeySpec, error) {
	// 使用内部解析函数
	spec, err := parseHotkeySpec(hotkeyStr)
	if err != nil {
		return common.HotkeySpec{}, err
	}

	// 转换为通用格式
	return convertToCommonSpec(spec), nil
}

// =============================================================================
// InputProvider 接口实现 - 实现在 input.go 中 ✅
// =============================================================================

// =============================================================================
// NotificationProvider 接口实现 - 实现在 notification.go 中 ✅
// =============================================================================

// =============================================================================
// Provider生命周期管理
// =============================================================================

// Shutdown 关闭Provider,释放所有资源。
// 部分实现 ✅ (快捷键清理)
func (p *Provider) Shutdown() error {
	// 清理快捷键管理器
	if p.hotkeyMgr != nil {
		p.hotkeyMgr.stop()
		p.hotkeyMgr = nil
	}

	// 清理快捷键注册表
	p.hotkeyMu.Lock()
	p.hotkeyRegistry = make(map[int]bool)
	p.hotkeyMu.Unlock()

	// 停止同步
	if p.syncManager != nil {
		p.syncManager.Stop()
	}

	return nil
}

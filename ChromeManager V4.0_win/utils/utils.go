//go:build windows

package utils

import (
	"chromemanager/config"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// 本文件保留syncmanager等底层模块需要的基础Windows API包装函数
// 大部分功能已迁移到 platform/windows 包

func browserExecutableFromCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if strings.HasPrefix(command, `"`) {
		rest := strings.TrimPrefix(command, `"`)
		if idx := strings.Index(rest, `"`); idx >= 0 {
			return rest[:idx]
		}
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func existingBrowserPathFromRegistry(root registry.Key, keyPath string) (string, bool) {
	k, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()

	value, _, err := k.GetStringValue("")
	if err != nil || value == "" {
		return "", false
	}
	path := browserExecutableFromCommand(value)
	if path == "" {
		path = value
	}
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return "", false
}

func FindBrowserPath(browserType string) (string, error) {
	if browserType == "" {
		browserType = config.BrowserTypeChrome
	}

	var appPathsKey, clientKey, exeName string
	var commonPaths []string
	homeDir, _ := os.UserHomeDir()
	localAppData := os.Getenv("LOCALAPPDATA")

	switch browserType {
	case config.BrowserTypeChrome:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Google Chrome\shell\open\command`
		exeName = "chrome.exe"
		commonPaths = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(localAppData, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(homeDir, `AppData\Local\Google\Chrome\Application\chrome.exe`),
		}
	case config.BrowserTypeEdge:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Microsoft Edge\shell\open\command`
		exeName = "msedge.exe"
		commonPaths = []string{
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			filepath.Join(localAppData, `Microsoft\Edge\Application\msedge.exe`),
		}
	case config.BrowserTypeOpera:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\opera.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Opera\shell\open\command`
		exeName = "opera.exe"
		commonPaths = []string{
			filepath.Join(localAppData, `Programs\Opera\opera.exe`),
			filepath.Join(homeDir, `AppData\Local\Programs\Opera\opera.exe`),
			`C:\Program Files\Opera\opera.exe`,
			`C:\Program Files (x86)\Opera\opera.exe`,
		}
	case config.BrowserTypeBrave:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\brave.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Brave\shell\open\command`
		exeName = "brave.exe"
		commonPaths = []string{
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
			`C:\Program Files (x86)\BraveSoftware\Brave-Browser\Application\brave.exe`,
			filepath.Join(localAppData, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		}
	default:
		return FindBrowserPath(config.BrowserTypeChrome)
	}

	for _, keyPath := range []string{appPathsKey, clientKey} {
		if keyPath == "" {
			continue
		}
		if path, ok := existingBrowserPathFromRegistry(registry.LOCAL_MACHINE, keyPath); ok {
			return path, nil
		}
		if path, ok := existingBrowserPathFromRegistry(registry.CURRENT_USER, keyPath); ok {
			return path, nil
		}
	}

	for _, path := range commonPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s not found in registry or common installation paths", exeName)
}

const (
	WM_CLOSE           = 0x0010
	WM_COMMAND         = 0x0111
	HWND_TOP           = 0
	HWND_BOTTOM        = 1
	HWND_TOPMOST       = -1
	HWND_NOTOPMOST     = -2
	SWP_NOSIZE         = 0x0001
	SWP_NOMOVE         = 0x0002
	SWP_NOZORDER       = 0x0004
	SWP_NOREDRAW       = 0x0008
	SWP_NOACTIVATE     = 0x0010
	SWP_FRAMECHANGED   = 0x0020
	SWP_SHOWWINDOW     = 0x0040
	SWP_HIDEWINDOW     = 0x0080
	SW_HIDE            = 0
	SW_SHOWNORMAL      = 1
	SW_SHOWMINIMIZED   = 2
	SW_SHOWMAXIMIZED   = 3
	SW_MAXIMIZE        = 3
	SW_SHOWNOACTIVATE  = 4
	SW_SHOW            = 5
	SW_MINIMIZE        = 6
	SW_SHOWMINNOACTIVE = 7
	SW_SHOWNA          = 8
	SW_RESTORE         = 9
	GWL_STYLE          = -16
	WS_VISIBLE         = 0x10000000
	WS_SIZEBOX         = 0x00040000
	WS_SYSMENU         = 0x00080000
	RDW_INVALIDATE     = 0x0001
	RDW_ERASE          = 0x0004
	RDW_ALLCHILDREN    = 0x0080
	RDW_FRAME          = 0x0400
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procGetWindowThreadProcessId     = user32.NewProc("GetWindowThreadProcessId")
	procGetClassNameW                = user32.NewProc("GetClassNameW")
	procGetWindowTextW               = user32.NewProc("GetWindowTextW")
	procGetWindowLongW               = user32.NewProc("GetWindowLongW")
	procIsWindow                     = user32.NewProc("IsWindow")
	procGetWindowRect                = user32.NewProc("GetWindowRect")
	procIsWindowVisible              = user32.NewProc("IsWindowVisible")
	procEnumWindows                  = user32.NewProc("EnumWindows")
	procSetWindowPos                 = user32.NewProc("SetWindowPos")
	procSetWindowLongW               = user32.NewProc("SetWindowLongW")
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	procShowWindow                   = user32.NewProc("ShowWindow")
	procMoveWindow                   = user32.NewProc("MoveWindow")
	procUpdateWindow                 = user32.NewProc("UpdateWindow")
	procSetForegroundWindow          = user32.NewProc("SetForegroundWindow")
	procRedrawWindow                 = user32.NewProc("RedrawWindow")
	procDwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
)

// RECT Windows 矩形结构
type RECT struct {
	Left, Top, Right, Bottom int32
}

// LogError 记录错误日志（保留以兼容旧代码）
func LogError(operation string, err error) {
	if err != nil {
		log.Printf("[ERROR] %s: %v", operation, err)
	}
}

// NormalizePath 规范化路径
func NormalizePath(path string) string {
	return filepath.Clean(strings.ReplaceAll(path, "\\", "/"))
}

// GetWindowProcessID 获取窗口所属进程ID
func GetWindowProcessID(hwnd uintptr) (uint32, error) {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return 0, fmt.Errorf("failed to get process ID for window")
	}
	return pid, nil
}

// GetClassName 获取窗口类名
func GetClassName(hwnd uintptr) (string, error) {
	buf := make([]uint16, 256)
	ret, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return "", fmt.Errorf("failed to get class name")
	}
	return windows.UTF16ToString(buf), nil
}

// GetWindowText 获取窗口标题
func GetWindowText(hwnd uintptr) (string, error) {
	buf := make([]uint16, 512)
	ret, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return "", fmt.Errorf("failed to get window text")
	}
	return windows.UTF16ToString(buf), nil
}

// IsWindowValid 检查窗口句柄是否有效
func IsWindowValid(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}

	ret, _, _ := procIsWindow.Call(hwnd)
	return ret != 0
}

// GetWindowRect 获取窗口矩形区域
func GetWindowRect(hwnd uintptr) (*RECT, error) {
	var rect RECT
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return nil, fmt.Errorf("failed to get window rect")
	}
	return &rect, nil
}

func isChromePopupCandidateClass(className string) bool {
	return strings.Contains(className, "Chrome_WidgetWin") ||
		strings.Contains(className, "Chrome_RenderWidgetHostHWND") ||
		className == "Chrome_Dialog" ||
		className == "Chrome_WidgetWin_0" ||
		className == "Chrome_WidgetWin_1" ||
		strings.Contains(className, "Chrome_Widget") ||
		strings.Contains(className, "MenuHost") ||
		strings.Contains(className, "PopupHost")
}

func shouldIncludeChromePopupByTitle(title string, err error) bool {
	if err != nil {
		return true
	}
	return len(title) <= 100
}

var (
	enumPopupsMu         sync.Mutex
	enumPopupsParentPID  uint32
	enumPopupsChromeHWND uintptr
	enumPopupsResult     []uintptr
	enumPopupsCallback   uintptr
	enumPopupsOnce       sync.Once
)

// GetChromePopups 获取Chrome窗口的弹出窗口列表
func GetChromePopups(chromeHWND uintptr) []uintptr {
	enumPopupsMu.Lock()
	defer enumPopupsMu.Unlock()

	enumPopupsResult = make([]uintptr, 0)
	enumPopupsParentPID = 0
	procGetWindowThreadProcessId.Call(chromeHWND, uintptr(unsafe.Pointer(&enumPopupsParentPID)))
	enumPopupsChromeHWND = chromeHWND

	enumPopupsOnce.Do(func() {
		enumPopupsCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
			ret, _, _ := procIsWindowVisible.Call(hwnd)
			if ret != 0 {
				className, err := GetClassName(hwnd)
				if err == nil {
					// 检查是否是Chrome相关窗口
					isChromeWindow := strings.Contains(className, "Chrome_WidgetWin") ||
						strings.Contains(className, "Chrome_RenderWidgetHostHWND") ||
						className == "Chrome_Dialog" ||
						className == "Chrome_WidgetWin_0" ||
						className == "Chrome_WidgetWin_1" ||
						strings.Contains(className, "Chrome_Widget") ||
						strings.Contains(className, "MenuHost") ||
						strings.Contains(className, "PopupHost")

					if isChromeWindow {
						var pid uint32
						procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

						// 只收集同进程且非主窗口的窗口
						if pid == enumPopupsParentPID && hwnd != enumPopupsChromeHWND {
							title, _ := GetWindowText(hwnd)

							// 过滤掉标题过长的窗口（通常不是弹出窗口）
							if len(title) <= 100 {
								enumPopupsResult = append(enumPopupsResult, hwnd)
							}
						}
					}
				}
			}
			return 1 // 继续枚举
		})
	})

	procEnumWindows.Call(enumPopupsCallback, 0)

	// 安全复制结果返回
	res := make([]uintptr, len(enumPopupsResult))
	copy(res, enumPopupsResult)
	return res
}

// ChromeProcessInfo Chrome进程信息（保留以兼容旧代码）
type ChromeProcessInfo struct {
	PID         int32
	HWND        uintptr
	Title       string
	CommandLine string
	UserDataDir string
	Number      int
	DebugPort   int
}

// FindChromePath 查找Chrome安装路径（保留以兼容旧代码）
// 注意：此函数已迁移到 platform/windows，建议使用 provider.FindChromePath()
// FindChromePath 查找Chrome安装路径（保留以兼容旧代码）
// 注意：此函数已迁移到 platform/windows，建议使用 provider.FindChromePath()
// FindChromePath 查找Chrome安装路径（保留以兼容旧代码）
// 注意：此函数已迁移到 platform/windows，建议使用 provider.FindBrowserPath()
func FindChromePath() (string, error) {
	// 1. 尝试从注册表获取 (App Paths)
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		path, _, err := k.GetStringValue("")
		if err == nil && path != "" {
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}

	// 2. 尝试从注册表获取 (Clients - Standard Install)
	k, err = registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Clients\StartMenuInternet\Google Chrome\shell\open\command`, registry.QUERY_VALUE)
	if err == nil {
		defer k.Close()
		path, _, err := k.GetStringValue("")
		if err == nil && path != "" {
			// 清理引号
			path = strings.Trim(path, `"`)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}

	// 3. 尝试从常见路径获取
	commonPaths := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		`C:\Users\` + os.Getenv("USERNAME") + `\AppData\Local\Google\Chrome\Application\chrome.exe`,
	}

	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("chrome not found in registry or common installation paths")
}

// FindChromeProcesses 查找所有Chrome进程（保留以兼容旧代码）
// 注意：此函数已迁移到 platform/windows，建议使用 provider.EnumChromeWindows()
func FindChromeProcesses() ([]ChromeProcessInfo, error) {
	var chromeProcesses []ChromeProcessInfo
	var mu sync.Mutex
	var wg sync.WaitGroup

	processes, err := process.Processes()
	if err != nil {
		return nil, fmt.Errorf("failed to get processes: %v", err)
	}

	const maxConcurrency = 20
	semaphore := make(chan struct{}, maxConcurrency)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 并发处理进程
	for _, p := range processes {
		wg.Add(1)
		go func(proc *process.Process) {
			defer wg.Done()

			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}

			select {
			case <-ctx.Done():
				return
			default:
			}

			if chromeInfo := analyzeChromeProcess(proc); chromeInfo != nil {
				mu.Lock()
				chromeProcesses = append(chromeProcesses, *chromeInfo)
				mu.Unlock()
			}
		}(p)
	}

	// 启动超时监控
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return chromeProcesses, fmt.Errorf("timeout while analyzing Chrome processes")
	}

	if len(chromeProcesses) == 0 {
		return chromeProcesses, fmt.Errorf("no Chrome processes found")
	}

	// 按编号排序
	sort.Slice(chromeProcesses, func(i, j int) bool {
		if chromeProcesses[i].Number == chromeProcesses[j].Number {
			return chromeProcesses[i].PID < chromeProcesses[j].PID
		}
		return chromeProcesses[i].Number < chromeProcesses[j].Number
	})

	return chromeProcesses, nil
}

func analyzeChromeProcess(p *process.Process) *ChromeProcessInfo {
	name, err := p.Name()
	if err != nil {
		return nil
	}
	supportedBrowsers := map[string]bool{
		"chrome.exe": true,
		"msedge.exe": true,
		"opera.exe":  true,
		"brave.exe":  true,
	}
	if !supportedBrowsers[strings.ToLower(name)] {
		return nil
	}

	cmdline, err := p.Cmdline()
	if err != nil {
		return nil
	}

	userDataDir, debugPort, number := parseChromeCmdLine(cmdline)
	if userDataDir == "" {
		return nil
	}

	hwnd := findWindowByPID(p.Pid)
	if hwnd == 0 {
		return nil
	}

	// 验证窗口是否仍然有效且可见
	if !IsWindowValid(hwnd) {
		return nil
	}

	title, _ := GetWindowText(hwnd)

	return &ChromeProcessInfo{
		PID:         p.Pid,
		HWND:        hwnd,
		Title:       title,
		CommandLine: cmdline,
		UserDataDir: userDataDir,
		Number:      number,
		DebugPort:   debugPort,
	}
}

func parseChromeCmdLine(cmdline string) (userDataDir string, debugPort int, number int) {
	// 提取用户数据目录
	userDataRegex := regexp.MustCompile(`--user-data-dir[=\s]+"?([^"]+)"?`)
	if matches := userDataRegex.FindStringSubmatch(cmdline); len(matches) > 1 {
		userDataDir = strings.Trim(matches[1], `"`)
		number = extractNumberFromPath(userDataDir)
	}

	// 提取调试端口
	portRegex := regexp.MustCompile(`--remote-debugging-port[=\s]+(\d+)`)
	if matches := portRegex.FindStringSubmatch(cmdline); len(matches) > 1 {
		debugPort, _ = strconv.Atoi(matches[1])
	}

	return
}

func extractNumberFromPath(path string) int {
	// 优先检查是否有 chrome_env_N 格式的编号
	chromeEnvRegex := regexp.MustCompile(`chrome_env_(\d+)`)
	if matches := chromeEnvRegex.FindStringSubmatch(path); len(matches) > 1 {
		if num, err := strconv.Atoi(matches[1]); err == nil && num > 0 {
			return num
		}
	}

	// 检查路径末尾是否直接是数字
	pathParts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := len(pathParts) - 1; i >= 0; i-- {
		part := pathParts[i]
		if num, err := strconv.Atoi(part); err == nil && num > 0 {
			if i == len(pathParts)-1 {
				return num
			}
		}
	}

	// 最后尝试查找路径中的所有数字
	re := regexp.MustCompile(`\d+`)
	matches := re.FindAllString(path, -1)

	// 倒序查找，优先使用后面的数字
	for i := len(matches) - 1; i >= 0; i-- {
		match := matches[i]
		if num, err := strconv.Atoi(match); err == nil && num > 0 {
			return num
		}
	}

	return 0
}

var (
	findWinMu        sync.Mutex
	findWinTargetPID uint32
	findWinResult    uintptr
	findWinCallback  uintptr
	findWinOnce      sync.Once
)

func findWindowByPID(pid int32) uintptr {
	findWinMu.Lock()
	defer findWinMu.Unlock()

	findWinTargetPID = uint32(pid)
	findWinResult = 0

	findWinOnce.Do(func() {
		findWinCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
			var windowPID uint32
			procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))

			if findWinTargetPID == windowPID {
				ret, _, _ := procIsWindowVisible.Call(hwnd)
				if ret != 0 {
					className, _ := GetClassName(hwnd)
					if strings.Contains(className, "Chrome") {
						findWinResult = hwnd
						return 0 // 停止枚举
					}
				}
			}
			return 1 // 继续枚举
		})
	})

	procEnumWindows.Call(findWinCallback, 0)
	return findWinResult
}

// SetWindowPos 设置窗口位置和大小（保留以兼容旧代码）
func SetWindowPos(hwnd uintptr, x, y, width, height int) error {
	ret, _, _ := procSetWindowPos.Call(
		hwnd,
		HWND_TOP,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		SWP_NOZORDER|SWP_NOACTIVATE,
	)
	if ret == 0 {
		return fmt.Errorf("failed to set window position")
	}
	return nil
}

// ShowWindow 显示或隐藏窗口（保留以兼容旧代码）
func ShowWindow(hwnd uintptr, cmdShow int) error {
	ret, _, _ := procShowWindow.Call(hwnd, uintptr(cmdShow))
	if ret == 0 {
		return fmt.Errorf("failed to show window")
	}
	return nil
}

// SetForegroundWindow 激活窗口（保留以兼容旧代码）
func SetForegroundWindow(hwnd uintptr) error {
	ret, _, _ := procSetForegroundWindow.Call(hwnd)
	if ret == 0 {
		return fmt.Errorf("failed to set foreground window")
	}
	return nil
}

// BringWindowToTop 将窗口置顶并激活（保留以兼容旧代码）
func BringWindowToTop(hwnd uintptr) error {
	// 先恢复窗口（如果最小化）
	ShowWindow(hwnd, SW_RESTORE)

	// 先尝试激活窗口。Windows 前台锁定可能拒绝这一步，但后面的短暂置顶仍然可以修正 Z 顺序。
	_ = SetForegroundWindow(hwnd)

	// 设置为置顶
	ret, _, _ := procSetWindowPos.Call(
		hwnd,
		^uintptr(0), // HWND_TOPMOST
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_SHOWWINDOW,
	)
	if ret == 0 {
		return fmt.Errorf("failed to bring window to top")
	}

	// 立即取消置顶状态
	procSetWindowPos.Call(
		hwnd,
		^uintptr(1), // HWND_NOTOPMOST
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_SHOWWINDOW,
	)

	_ = SetForegroundWindow(hwnd)
	return nil
}

// GetChromePopupsBatch 单次枚举全系统窗口，为多个 Chrome 主窗口归类 popup。
func GetChromePopupsBatch(chromeHWNDs []uintptr) map[uintptr][]uintptr {
	results := make(map[uintptr][]uintptr, len(chromeHWNDs))
	if len(chromeHWNDs) == 0 {
		return results
	}

	targetPIDs := make(map[uint32]struct{}, len(chromeHWNDs))
	handleToPID := make(map[uintptr]uint32, len(chromeHWNDs))
	excludedHandles := make(map[uintptr]struct{}, len(chromeHWNDs))

	for _, chromeHWND := range chromeHWNDs {
		results[chromeHWND] = []uintptr{}
		if chromeHWND == 0 {
			continue
		}

		excludedHandles[chromeHWND] = struct{}{}

		pid, err := GetWindowProcessID(chromeHWND)
		if err != nil {
			continue
		}

		handleToPID[chromeHWND] = pid
		targetPIDs[pid] = struct{}{}
	}

	if len(targetPIDs) == 0 {
		return results
	}

	pidPopups := make(map[uint32][]uintptr, len(targetPIDs))
	enumCallback := syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
		if hwnd == 0 {
			return 1
		}

		if _, excluded := excludedHandles[hwnd]; excluded {
			return 1
		}

		ret, _, _ := procIsWindowVisible.Call(hwnd)
		if ret == 0 {
			return 1
		}

		className, err := GetClassName(hwnd)
		if err != nil || !isChromePopupCandidateClass(className) {
			return 1
		}

		pid, err := GetWindowProcessID(hwnd)
		if err != nil {
			return 1
		}

		if _, tracked := targetPIDs[pid]; !tracked {
			return 1
		}

		title, titleErr := GetWindowText(hwnd)
		if !shouldIncludeChromePopupByTitle(title, titleErr) {
			return 1
		}

		pidPopups[pid] = append(pidPopups[pid], hwnd)
		return 1
	})

	procEnumWindows.Call(enumCallback, 0)

	for chromeHWND, pid := range handleToPID {
		popups := pidPopups[pid]
		if len(popups) == 0 {
			continue
		}

		results[chromeHWND] = make([]uintptr, len(popups))
		copy(results[chromeHWND], popups)
	}

	return results
}

//go:build windows

package windows

import (
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows API 常量定义
const (
	WM_CLOSE          = 0x0010
	WM_COMMAND        = 0x0111
	HWND_TOP          = 0
	HWND_BOTTOM       = 1
	HWND_TOPMOST      = -1
	HWND_NOTOPMOST    = -2
	SWP_NOSIZE        = 0x0001
	SWP_NOMOVE        = 0x0002
	SWP_NOZORDER      = 0x0004
	SWP_NOREDRAW      = 0x0008
	SWP_NOACTIVATE    = 0x0010
	SWP_FRAMECHANGED  = 0x0020
	SWP_SHOWWINDOW    = 0x0040
	SWP_HIDEWINDOW    = 0x0080
	SW_HIDE           = 0
	SW_SHOWNORMAL     = 1
	SW_SHOWMINIMIZED  = 2
	SW_SHOWMAXIMIZED  = 3
	SW_MAXIMIZE       = 3
	SW_SHOWNOACTIVATE = 4
	SW_SHOW           = 5
	SW_MINIMIZE       = 6
	SW_SHOWMINNOACTIVE = 7
	SW_SHOWNA         = 8
	SW_RESTORE        = 9
	GWL_STYLE         = -16
	WS_VISIBLE        = 0x10000000

	// Monitor constants
	MONITORINFOF_PRIMARY = 0x00000001
	SM_CXSCREEN          = 0
	SM_CYSCREEN          = 1
)

// Windows API DLL 和函数句柄
var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	shcore   = syscall.NewLazyDLL("shcore.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")

	procFindWindowW              = user32.NewProc("FindWindowW")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procSetWindowTextW           = user32.NewProc("SetWindowTextW")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsWindow                 = user32.NewProc("IsWindow")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procShowWindow               = user32.NewProc("ShowWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procEnumDisplayMonitors      = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")

	procSHChangeNotify = shell32.NewProc("SHChangeNotify")
)

// RECT Windows 矩形结构
type RECT struct {
	Left, Top, Right, Bottom int32
}

// POINT Windows 点结构
type POINT struct {
	X, Y int32
}

// MONITORINFO Windows 显示器信息结构
type MONITORINFO struct {
	cbSize    uint32
	rcMonitor RECT
	rcWork    RECT
	dwFlags   uint32
}

// --- 底层 Windows API 封装函数 ---

// findWindowW 调用 Windows API FindWindowW 查找窗口
func findWindowW(className, windowName string) uintptr {
	var classNamePtr, windowNamePtr uintptr

	if className != "" {
		classNameUTF16, _ := windows.UTF16PtrFromString(className)
		classNamePtr = uintptr(unsafe.Pointer(classNameUTF16))
	}

	if windowName != "" {
		windowNameUTF16, _ := windows.UTF16PtrFromString(windowName)
		windowNamePtr = uintptr(unsafe.Pointer(windowNameUTF16))
	}

	hwnd, _, _ := procFindWindowW.Call(classNamePtr, windowNamePtr)
	return hwnd
}

var (
	enumWindowsMu       sync.Mutex
	enumWindowsTarget   func(hwnd uintptr) bool
	enumWindowsCallback uintptr
	enumWindowsOnce     sync.Once
)

// enumWindows 枚举所有顶级窗口
func enumWindows(callback func(hwnd uintptr) bool) error {
	enumWindowsMu.Lock()
	defer enumWindowsMu.Unlock()

	enumWindowsTarget = callback

	enumWindowsOnce.Do(func() {
		enumWindowsCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
			if enumWindowsTarget != nil && enumWindowsTarget(hwnd) {
				return 1 // 继续枚举
			}
			return 0 // 停止枚举
		})
	})

	procEnumWindows.Call(enumWindowsCallback, 0)
	return nil
}

// getWindowThreadProcessID 获取窗口所属的进程ID
func getWindowThreadProcessID(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// getWindowTextW 获取窗口标题文本
func getWindowTextW(hwnd uintptr) string {
	buf := make([]uint16, 512)
	ret, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// setWindowTextW 设置窗口标题文本
func setWindowTextW(hwnd uintptr, text string) error {
	textPtr, _ := windows.UTF16PtrFromString(text)
	ret, _, _ := procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(textPtr)))
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

// getClassNameW 获取窗口类名
func getClassNameW(hwnd uintptr) string {
	buf := make([]uint16, 256)
	ret, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if ret == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}

// isWindowVisible 检查窗口是否可见
func isWindowVisible(hwnd uintptr) bool {
	ret, _, _ := procIsWindowVisible.Call(hwnd)
	return ret != 0
}

// isWindow 检查窗口句柄是否有效
func isWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	ret, _, _ := procIsWindow.Call(hwnd)
	return ret != 0
}

// getWindowRect 获取窗口的矩形区域（屏幕坐标）
func getWindowRect(hwnd uintptr) (*RECT, error) {
	var rect RECT
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return nil, syscall.GetLastError()
	}
	return &rect, nil
}

// setWindowPos 设置窗口位置和大小
func setWindowPos(hwnd uintptr, x, y, width, height int) error {
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
		return syscall.GetLastError()
	}
	return nil
}

// showWindow 显示或隐藏窗口
func showWindow(hwnd uintptr, cmdShow int) error {
	ret, _, _ := procShowWindow.Call(hwnd, uintptr(cmdShow))
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

// setForegroundWindow 将窗口设置为前台窗口
func setForegroundWindow(hwnd uintptr) error {
	ret, _, _ := procSetForegroundWindow.Call(hwnd)
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

// getForegroundWindow 获取当前前台窗口
func getForegroundWindow() uintptr {
	hwnd, _, _ := procGetForegroundWindow.Call()
	return hwnd
}

// postMessage 向窗口发送消息
func postMessage(hwnd uintptr, msg uint32, wParam, lParam uintptr) error {
	ret, _, _ := procPostMessageW.Call(hwnd, uintptr(msg), wParam, lParam)
	if ret == 0 {
		return syscall.GetLastError()
	}
	return nil
}

var (
	enumMonitorsMu       sync.Mutex
	enumMonitorsTarget   func(hMonitor, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr
	enumMonitorsCallback uintptr
	enumMonitorsOnce     sync.Once
)

// enumDisplayMonitors 枚举所有显示器
// 从 utils 迁移完成 ✅
func enumDisplayMonitors(callback func(hMonitor, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr) error {
	enumMonitorsMu.Lock()
	defer enumMonitorsMu.Unlock()

	enumMonitorsTarget = callback

	enumMonitorsOnce.Do(func() {
		enumMonitorsCallback = syscall.NewCallback(func(hMonitor, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr {
			if enumMonitorsTarget != nil {
				return enumMonitorsTarget(hMonitor, hdcMonitor, lprcMonitor, dwData)
			}
			return 0
		})
	})

	ret, _, err := procEnumDisplayMonitors.Call(0, 0, enumMonitorsCallback, 0)
	if ret == 0 {
		return err
	}
	return nil
}

// getMonitorInfo 获取显示器信息
// 从 utils 迁移完成 ✅
func getMonitorInfo(hMonitor uintptr) (*MONITORINFO, error) {
	var mi MONITORINFO
	mi.cbSize = uint32(unsafe.Sizeof(mi))
	
	ret, _, err := procGetMonitorInfoW.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
	if ret == 0 {
		return nil, err
	}
	return &mi, nil
}

func init() {
	initializeDPIAwareness()
}

func initializeDPIAwareness() {
	// 1. Try SetProcessDpiAwarenessContext (Win10 1703+)
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	if procSetProcessDpiAwarenessContext.Find() == nil {
		ret, _, _ := procSetProcessDpiAwarenessContext.Call(uintptr(0xFFFFFFFFFFFFFFFC)) // -4
		if ret != 0 {
			return
		}
	}

	// 2. Try SetProcessDpiAwareness (Win8.1+)
	// PROCESS_PER_MONITOR_DPI_AWARE = 2
	if procSetProcessDpiAwareness.Find() == nil {
		ret, _, _ := procSetProcessDpiAwareness.Call(2)
		if ret != 0 {
			return
		}
	}

	// 3. Fallback to SetProcessDPIAware (Vista+)
	if procSetProcessDPIAware.Find() == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

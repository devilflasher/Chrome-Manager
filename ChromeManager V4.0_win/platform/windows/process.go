//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"
)

// IsProcessRunning 检查进程是否正在运行
// 从 utils.IsProcessRunning 迁移 ✅
func (p *Provider) IsProcessRunning(pid int32) bool {
	// 首先使用 gopsutil 库尝试检查
	proc, err := process.NewProcess(pid)
	if err != nil {
		// 如果创建进程对象失败，说明进程不存在
		return false
	}

	running, err := proc.IsRunning()
	if err != nil {
		// 如果检查失败，再尝试 Windows API 方法
		return p.isProcessRunningWindows(pid)
	}

	return running
}

// isProcessRunningWindows 使用 Windows API 检查进程是否运行
// 从 utils.isProcessRunningWindows 迁移 ✅
func (p *Provider) isProcessRunningWindows(pid int32) bool {
	if runtime.GOOS != "windows" {
		return false
	}

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess := kernel32.NewProc("OpenProcess")
	procCloseHandle := kernel32.NewProc("CloseHandle")

	// PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	// 尝试打开进程句柄
	handle, _, _ := procOpenProcess.Call(
		0x1000, // PROCESS_QUERY_LIMITED_INFORMATION
		0,      // bInheritHandle = FALSE
		uintptr(pid),
	)

	if handle == 0 {
		// 无法打开进程句柄，说明进程不存在
		return false
	}

	// 关闭句柄
	procCloseHandle.Call(handle)

	return true
}

// CloseWindowGracefully 优雅地关闭窗口（等待关闭完成）
//
// 通过发送 WM_CLOSE 请求 Chrome 正常退出，并等待进程完全结束。
// 这样可以确保 Chrome 有足够时间将 Preferences / Secure Preferences 写回磁盘，
// 防止插件配置损坏（表现为插件突然全部消失或被标记为"损坏/受损"）。
//
// 超时策略：等待 timeout*100ms。若进程仍未退出，则保持安全关闭状态，不自动强制终止。
func (p *Provider) CloseWindowGracefully(handle common.WindowHandle, timeout int) error {
	pid, pidErr := p.GetWindowProcessID(handle)

	// 发送 WM_CLOSE，让 Chrome 走正常退出流程
	if err := p.PostMessage(handle, WM_CLOSE, 0, 0); err != nil {
		return nil // 窗口可能已经不存在
	}

	if pidErr != nil {
		return nil
	}

	// 等待进程自然退出（每 100ms 检查一次，最多等待 timeout*100ms）
	deadline := time.Now().Add(time.Duration(timeout) * 100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !p.IsProcessRunning(pid) {
			return nil
		}
		p.sleep(100)
	}

	// 超时后仍未退出：保持安全关闭策略，不自动强杀进程。
	if p.IsProcessRunning(pid) {
		return fmt.Errorf("process %d is still shutting down", pid)
	}
	return nil
}

// BringWindowToTop 将窗口置顶并激活
// 从 utils.BringWindowToTop 迁移 ✅
func (p *Provider) BringWindowToTop(handle common.WindowHandle) error {
	hwnd := uintptr(handle)

	// 先恢复窗口（如果最小化）
	p.ShowWindow(handle, true)

	// 设置为前台窗口
	if err := p.SetForegroundWindow(handle); err != nil {
		return err
	}

	// 设置为置顶
	ret, _, _ := procSetWindowPos.Call(
		hwnd,
		^uintptr(0), // HWND_TOPMOST
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE,
	)
	if ret == 0 {
		return fmt.Errorf("failed to bring window to top")
	}

	// 立即取消置顶状态，避免窗口一直在最前面
	procSetWindowPos.Call(
		hwnd,
		^uintptr(1), // HWND_NOTOPMOST
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE,
	)

	return nil
}

var (
	findWinByPIDMu       sync.Mutex
	findWinByPIDTarget   uint32
	findWinByPIDResult   uintptr
	findWinByPIDCallback uintptr
	findWinByPIDOnce     sync.Once
)

// FindWindowByPID 根据进程 ID 查找窗口句柄
// 从 utils.findWindowByPID 迁移 ✅
func (p *Provider) FindWindowByPID(pid int32) (common.WindowHandle, error) {
	findWinByPIDMu.Lock()
	defer findWinByPIDMu.Unlock()

	findWinByPIDTarget = uint32(pid)
	findWinByPIDResult = 0

	findWinByPIDOnce.Do(func() {
		findWinByPIDCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
			var windowPID uint32
			procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&windowPID)))

			if findWinByPIDTarget == windowPID {
				// 检查窗口是否可见
				ret, _, _ := procIsWindowVisible.Call(hwnd)
				if ret != 0 {
					// 获取窗口类名
					buf := make([]uint16, 256)
					ret, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
					if ret > 0 {
						className := syscall.UTF16ToString(buf)
						// 检查是否是 Chrome 窗口
						if len(className) > 0 && (className[0] == 'C' || className[0] == 'c') {
							// 简单检查类名是否包含 Chrome
							for i := 0; i < len(className)-5; i++ {
								if (className[i] == 'C' || className[i] == 'c') &&
									(className[i+1] == 'h' || className[i+1] == 'H') &&
									(className[i+2] == 'r' || className[i+2] == 'R') &&
									(className[i+3] == 'o' || className[i+3] == 'O') &&
									(className[i+4] == 'm' || className[i+4] == 'M') &&
									(className[i+5] == 'e' || className[i+5] == 'E') {
									findWinByPIDResult = hwnd
									return 0 // 停止枚举
								}
							}
						}
					}
				}
			}
			return 1 // 继续枚举
		})
	})

	procEnumWindows.Call(findWinByPIDCallback, 0)

	if findWinByPIDResult == 0 {
		return 0, fmt.Errorf("window not found for PID %d", pid)
	}

	return common.WindowHandle(findWinByPIDResult), nil
}

// sleep 辅助函数 - Windows Sleep API
func (p *Provider) sleep(milliseconds int) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procSleep := kernel32.NewProc("Sleep")
	procSleep.Call(uintptr(milliseconds))
}

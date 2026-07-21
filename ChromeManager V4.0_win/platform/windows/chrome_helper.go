//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// IsChromeDebugPortReady 检查 Chrome 调试端口是否就绪
// 从 utils.IsChromeDebugPortReady 迁移 ✅
func (p *Provider) IsChromeDebugPortReady(debugPort int) bool {
	if debugPort == 0 {
		return false
	}

	script := fmt.Sprintf(`
		try {
			$uri = "http://localhost:%d/json"
			$response = Invoke-RestMethod -Uri $uri -Method GET -TimeoutSec 2
			if ($response) {
				Write-Host "Ready"
			} else {
				Write-Host "NotReady"
			}
		} catch {
			Write-Host "NotReady"
		}
	`, debugPort)

	cmd := exec.Command("powershell", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	return strings.Contains(strings.TrimSpace(string(output)), "Ready")
}

// WaitForChromeWindowReady 等待 Chrome 窗口就绪
// 从 utils.WaitForChromeWindowReady 迁移 ✅
func (p *Provider) WaitForChromeWindowReady(handle common.WindowHandle, pid int32, debugPort int, maxWaitTime time.Duration) error {
	startTime := time.Now()
	checkInterval := 200 * time.Millisecond

	for time.Since(startTime) < maxWaitTime {
		// 检查窗口是否有效
		valid, err := p.IsWindowValid(handle)
		if err != nil || !valid {
			time.Sleep(checkInterval)
			continue
		}

		// 检查窗口是否可见
		visible, err := p.IsWindowVisible(handle)
		if err != nil || !visible {
			time.Sleep(checkInterval)
			continue
		}

		// 检查进程是否还在运行
		if !p.IsProcessRunning(pid) {
			return fmt.Errorf("browser process %d terminated", pid)
		}

		// 如果有调试端口，检查是否就绪
		if debugPort > 0 {
			if !p.IsChromeDebugPortReady(debugPort) {
				time.Sleep(checkInterval)
				continue
			}
		}

		return nil
	}

	elapsed := time.Since(startTime)
	return fmt.Errorf("browser window not ready within %.2fs", elapsed.Seconds())
}

var (
	getChromePopupsHelperMu         sync.Mutex
	getChromePopupsHelperParentPID  uint32
	getChromePopupsHelperChromeHWND uintptr
	getChromePopupsHelperResult     []common.WindowHandle
	getChromePopupsHelperCallback   uintptr
	getChromePopupsHelperOnce       sync.Once
)

// GetChromePopups 获取 Chrome 窗口的弹出窗口列表
// 从 utils.GetChromePopups 迁移 ✅
func (p *Provider) GetChromePopups(chromeHandle common.WindowHandle) ([]common.WindowHandle, error) {
	getChromePopupsHelperMu.Lock()
	defer getChromePopupsHelperMu.Unlock()

	getChromePopupsHelperResult = make([]common.WindowHandle, 0)
	getChromePopupsHelperChromeHWND = uintptr(chromeHandle)
	getChromePopupsHelperParentPID = 0
	procGetWindowThreadProcessId.Call(getChromePopupsHelperChromeHWND, uintptr(unsafe.Pointer(&getChromePopupsHelperParentPID)))

	getChromePopupsHelperOnce.Do(func() {
		getChromePopupsHelperCallback = syscall.NewCallback(func(hwnd, lParam uintptr) uintptr {
			ret, _, _ := procIsWindowVisible.Call(hwnd)
			if ret != 0 {
				// 获取窗口类名
				buf := make([]uint16, 256)
				retClass, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
				if retClass > 0 {
					className := syscall.UTF16ToString(buf)

					// 检查是否是 Chrome 相关窗口
					isChromeWindow := strings.Contains(className, "Chrome_WidgetWin") ||
						strings.Contains(className, "Chrome_RenderWidgetHostHWND") ||
						className == "Chrome_Dialog" ||
						className == "Chrome_WidgetWin_0" ||
						className == "Chrome_WidgetWin_1" ||
						strings.Contains(className, "Chrome_Widget") ||
						strings.Contains(className, "MenuHost") ||
						strings.Contains(className, "PopupHost")

					if isChromeWindow {
						// 获取窗口进程ID
						var pid uint32
						procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

						// 只收集同进程且非主窗口的窗口
						if pid == getChromePopupsHelperParentPID && hwnd != getChromePopupsHelperChromeHWND {
							// 获取窗口标题
							titleBuf := make([]uint16, 512)
							retTitle, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))

							// 过滤掉标题过长的窗口（通常不是弹出窗口）
							if retTitle > 0 {
								title := syscall.UTF16ToString(titleBuf)
								if len(title) <= 100 {
									getChromePopupsHelperResult = append(getChromePopupsHelperResult, common.WindowHandle(hwnd))
								}
							} else {
								// 没有标题的窗口也可能是弹出窗口
								getChromePopupsHelperResult = append(getChromePopupsHelperResult, common.WindowHandle(hwnd))
							}
						}
					}
				}
			}
			return 1 // 继续枚举
		})
	})

	procEnumWindows.Call(getChromePopupsHelperCallback, 0)

	res := make([]common.WindowHandle, len(getChromePopupsHelperResult))
	copy(res, getChromePopupsHelperResult)
	return res, nil
}

// BuildChromeCommand 构建 Chrome 启动命令
// 从 utils.BuildChromeCommand 迁移 ✅
func (p *Provider) BuildChromeCommand(targetPath, arguments string, number int, userDataDir string, debugPort int) []string {
	var cmd []string
	cmd = append(cmd, targetPath)

	if userDataDir != "" {
		cmd = append(cmd, fmt.Sprintf("--user-data-dir=%s", userDataDir))
	}

	if debugPort > 0 {
		cmd = append(cmd, fmt.Sprintf("--remote-debugging-port=%d", debugPort))
	}

	if arguments != "" {
		// 简单的参数分割
		args := strings.Fields(arguments)
		cmd = append(cmd, args...)
	}

	return cmd
}

// TitleSimilarity 计算两个标题的相似度
// 从 utils.TitleSimilarity 迁移 ✅
func (p *Provider) TitleSimilarity(title1, title2 string) float64 {
	if title1 == title2 {
		return 1.0
	}

	// 简单的相似度计算
	shorter := title1
	longer := title2
	if len(title1) > len(title2) {
		shorter = title2
		longer = title1
	}

	if len(longer) == 0 {
		return 0.0
	}

	// 计算公共子串长度
	common := 0
	for i := 0; i < len(shorter); i++ {
		if strings.Contains(longer, string(shorter[i])) {
			common++
		}
	}

	return float64(common) / float64(len(longer))
}

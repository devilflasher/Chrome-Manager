//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// ChromeProcessInfo Chrome进程信息 (临时结构，用于内部处理)
type ChromeProcessInfo struct {
	PID         int32
	HWND        uintptr
	Title       string
	CommandLine string
	UserDataDir string
	Number      int
	DebugPort   int
}

// findChromeProcesses 查找所有Chrome进程
// 从 utils.FindChromeProcesses 迁移完成 ✅
func findChromeProcesses() ([]ChromeProcessInfo, error) {
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

	// 如果没有找到 Chrome 进程，返回空列表而不是错误
	// 这样窗口列表可以正确更新为空
	if len(chromeProcesses) == 0 {
		return chromeProcesses, nil
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

// analyzeChromeProcess 分析单个进程是否为Chrome进程
// 从 utils.analyzeChromeProcess 迁移完成 ✅
func analyzeChromeProcess(p *process.Process) *ChromeProcessInfo {
	name, err := p.Name()
	if err != nil {
		return nil
	}

	// 支持的浏览器进程名
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
	if !isWindow(hwnd) || !isWindowVisible(hwnd) {
		return nil
	}

	title := getWindowTextW(hwnd)

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

// parseChromeCmdLine 解析Chrome命令行参数
// 从 utils.parseChromeCmdLine 迁移完成 ✅
func parseChromeCmdLine(cmdline string) (userDataDir string, debugPort int, number int) {
	// 提取 user-data-dir
	userDataRegex := regexp.MustCompile(`(?i)--user-data-dir(?:=|\s+)(?:"([^"]+)"|([^\s"]+))`)
	if matches := userDataRegex.FindStringSubmatch(cmdline); len(matches) > 2 {
		if matches[1] != "" {
			userDataDir = matches[1]
		} else {
			userDataDir = matches[2]
		}
		number = extractNumberFromPath(userDataDir)
	}

	// 提取 remote-debugging-port
	portRegex := regexp.MustCompile(`--remote-debugging-port[=\s]+(\d+)`)
	if matches := portRegex.FindStringSubmatch(cmdline); len(matches) > 1 {
		debugPort, _ = strconv.Atoi(matches[1])
	}

	return
}

// extractNumberFromPath 从路径中提取编号
// 从 utils.extractNumberFromPath 迁移完成 ✅
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
			return num
		}
	}

	return 0
}

// findWindowByPID 根据进程ID查找窗口
// 从 utils.findWindowByPID 迁移完成 ✅
func findWindowByPID(pid int32) uintptr {
	var targetHWND uintptr

	enumWindows(func(hwnd uintptr) bool {
		windowPID := getWindowThreadProcessID(hwnd)

		if uint32(pid) == windowPID && isWindowVisible(hwnd) {
			className := getClassNameW(hwnd)
			if strings.Contains(className, "Chrome") {
				targetHWND = hwnd
				return false // 停止枚举
			}
		}
		return true // 继续枚举
	})

	return targetHWND
}

// EnumChromeWindows 枚举所有Chrome窗口（Provider方法的实现）
// 从 utils.FindChromeProcesses 迁移完成 ✅
func (p *Provider) EnumChromeWindows() ([]common.WindowInfo, error) {
	chromeProcesses, err := findChromeProcesses()
	if err != nil {
		return nil, err
	}

	var windows []common.WindowInfo
	for _, chromeProc := range chromeProcesses {
		rect, _ := getWindowRect(chromeProc.HWND)

		windowInfo := common.WindowInfo{
			Handle:      common.WindowHandle(chromeProc.HWND),
			HWND:        chromeProc.HWND,
			Title:       chromeProc.Title,
			ClassName:   "Chrome_WidgetWin_1", // Chrome主窗口类名
			ProcessID:   chromeProc.PID,
			Number:      chromeProc.Number,
			IsVisible:   isWindowVisible(chromeProc.HWND),
			DebugPort:   chromeProc.DebugPort,
			UserDataDir: chromeProc.UserDataDir,
			CommandLine: chromeProc.CommandLine,
		}

		if rect != nil {
			windowInfo.Position = common.Rect{
				Left:   int(rect.Left),
				Top:    int(rect.Top),
				Width:  int(rect.Right - rect.Left),
				Height: int(rect.Bottom - rect.Top),
			}
		}

		windows = append(windows, windowInfo)
	}

	return windows, nil
}

//go:build windows

package main

import (
	"chromemanager/api"
	"chromemanager/platform/common"
	"chromemanager/utils"
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// APIAdapter 将 ChromeService 适配为 api.ServiceBridge 接口
// 处理签名差异，屏蔽 ChromeService 内部的方法细节
type APIAdapter struct {
	svc *ChromeService
}

// NewAPIAdapter 创建适配器实例
func NewAPIAdapter(svc *ChromeService) api.ServiceBridge {
	return &APIAdapter{svc: svc}
}

// --- 窗口管理 ---

func (a *APIAdapter) ImportWindows() (interface{}, error) {
	return a.svc.ImportWindows()
}

func (a *APIAdapter) OpenWindows(numbers string) error {
	return a.svc.OpenWindows(numbers)
}

func (a *APIAdapter) ArrangeWindows(mode string) error {
	// 默认对所有已导入窗口进行排列
	// 如果需要区分已选，可以增加模式判断，但目前软件 UI 是全排列
	numbers := a.allWindowNumbers()
	_, err := a.svc.AutoArrangeWindows(numbers)
	return err
}

func (a *APIAdapter) CloseWindows(numbers string) error {
	var nums []int
	lowerInput := strings.ToLower(strings.TrimSpace(numbers))

	switch lowerInput {
	case "", "all":
		// Close all imported windows.
		nums = a.allWindowNumbers()
	case "selected":
		// Close selected windows.
		if a.svc.syncManager != nil {
			nums = a.svc.syncManager.GetSelectedWindowNumbers()
		}
	default:
		// Close the requested window range.
		var err error
		nums, err = a.svc.provider.ParseWindowNumbers(numbers)
		if err != nil {
			return fmt.Errorf("解析窗口编号失败: %v", err)
		}
	}

	if len(nums) == 0 {
		return fmt.Errorf("没有可关闭的窗口(输入: %s)", numbers)
	}

	return a.svc.CloseWindows(nums)
}

func (a *APIAdapter) ZoomWindows(level int, numbers string) error {
	var nums []int
	lowerInput := strings.ToLower(strings.TrimSpace(numbers))

	switch lowerInput {
	case "", "all":
		nums = a.allWindowNumbers()
	case "selected":
		if a.svc.syncManager != nil {
			nums = a.svc.syncManager.GetSelectedWindowNumbers()
		}
	default:
		var err error
		nums, err = a.svc.provider.ParseWindowNumbers(numbers)
		if err != nil {
			return fmt.Errorf("解析窗口编号失败: %v", err)
		}
	}

	if len(nums) == 0 {
		return fmt.Errorf("没有可缩放的窗口")
	}

	// Translate preset zoom levels to native Chrome zoom-out steps from 100%.
	var outCount int
	switch level {
	case 100:
		outCount = 0
	case 75:
		outCount = 3 // 100%% -> 90%% -> 80%% -> 75%%
	case 50:
		outCount = 5 // 100%% -> ... -> 50%%
	case 25:
		outCount = 7 // 100%% -> ... -> 25%%
	default:
		return fmt.Errorf("不支持的缩放级别: %d. 可用值: 25, 50, 75, 100", level)
	}

	for _, num := range nums {
		info := a.svc.syncManager.GetWindowByID(num)
		if info == nil {
			continue
		}

		// 1. Focus the target window.
		a.svc.provider.SetForegroundWindow(common.WindowHandle(info.HWND))
		time.Sleep(150 * time.Millisecond)

		// 2. Reset zoom to 100%.
		_ = sendComboKeys("ctrl+0")
		time.Sleep(100 * time.Millisecond)

		// 3. Apply Ctrl+- steps for the requested level.
		for i := 0; i < outCount; i++ {
			_ = sendComboKeys("ctrl+minus")
			time.Sleep(30 * time.Millisecond)
		}
	}

	return nil
}

func (a *APIAdapter) StartSync(masterNumber int, slaveNumbers []int) error {
	return a.svc.StartSync(masterNumber, slaveNumbers)
}

func (a *APIAdapter) StopSync() error {
	return a.svc.StopSync()
}

func (a *APIAdapter) GetSyncStatus() map[string]interface{} {
	result, err := a.svc.GetSyncStatus()
	if err != nil {
		return map[string]interface{}{"status": "error", "error": err.Error()}
	}
	return result
}

func (a *APIAdapter) SetMasterWindow(windowNumber int) error {
	return a.svc.SetMasterWindow(windowNumber)
}

func (a *APIAdapter) SelectAllWindows(selected bool) error {
	return a.svc.SelectAllWindows(selected)
}

// --- 标签管理 ---

func (a *APIAdapter) KeepOnlyCurrentTab() error {
	// 获取所有已导入窗口的编号并传入
	numbers := a.allWindowNumbers()
	return a.svc.KeepOnlyCurrentTab(numbers)
}

func (a *APIAdapter) KeepOnlyNewTab() error {
	numbers := a.allWindowNumbers()
	return a.svc.KeepOnlyNewTab(numbers)
}

func (a *APIAdapter) NavigateWindowsToURL(url string, numbers string) error {
	var nums []int
	lowerInput := strings.ToLower(strings.TrimSpace(numbers))

	switch lowerInput {
	case "", "all":
		nums = a.allWindowNumbers()
	case "selected":
		if a.svc.syncManager != nil {
			nums = a.svc.syncManager.GetSelectedWindowNumbers()
		}
	default:
		var err error
		nums, err = a.svc.provider.ParseWindowNumbers(numbers)
		if err != nil {
			return fmt.Errorf("解析窗口编号失败: %v", err)
		}
	}

	if len(nums) == 0 {
		return fmt.Errorf("没有找到有效的窗口(输入: %s)", numbers)
	}

	return a.svc.BatchOpenURL(url, nums)
}

// --- 批量输入 ---

func (a *APIAdapter) InputTextToAllWindows(lines []string, overwrite, delayed bool) error {
	numbers := a.allWindowNumbers()
	cfg := utils.TextInputConfig{
		DirectContent: lines,
		InputMethod:   "clipboard",
		Overwrite:     overwrite,
		Delayed:       delayed,
		FilePath:      "DIRECT_CONTENT",
	}
	return a.svc.InputTextFromFile(numbers, cfg)
}

func (a *APIAdapter) InputRandomNumberToAllWindows(min, max float64, isFloat bool, decimalPlaces int, overwrite, delayed bool) error {
	numbers := a.allWindowNumbers()
	cfg := utils.RandomInputConfig{
		MinValue:      min,
		MaxValue:      max,
		IsFloat:       isFloat,
		DecimalPlaces: decimalPlaces,
		Overwrite:     overwrite,
		Delayed:       delayed,
	}
	return a.svc.InputRandomNumbers(numbers, cfg)
}

// --- 系统级操控（向主控窗口发送键鼠信号，Hook 自动同步到从窗口及弹窗）---

func (a *APIAdapter) SendKeystrokeToMaster(keys string, text string) error {
	masterHwnd, err := a.getMasterHWND()
	if err != nil {
		return err
	}
	// 先聚焦主控窗口
	a.svc.provider.SetForegroundWindow(common.WindowHandle(masterHwnd))
	// 给予足够的焦点切换时间，确保输入被正确接收
	time.Sleep(100 * time.Millisecond)

	if keys != "" {
		if err := sendComboKeys(keys); err != nil {
			return fmt.Errorf("发送组合键失败: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if text != "" {
		if err := sendTextInput(text); err != nil {
			return fmt.Errorf("发送文本失败: %v", err)
		}
	}
	return nil
}

func (a *APIAdapter) SendClickToMaster(x, y int, button string) error {
	masterHwnd, err := a.getMasterHWND()
	if err != nil {
		return err
	}
	a.svc.provider.SetForegroundWindow(common.WindowHandle(masterHwnd))
	// 给予足够的焦点切换时间
	time.Sleep(100 * time.Millisecond)

	// 处理 DPI 缩放补偿
	// 在 Windows 10+，我们需要根据窗口的 DPI 调整坐标
	dpi := getDpiForWindow(masterHwnd)
	scale := float64(dpi) / 96.0

	physicalX := int(float64(x) * scale)
	physicalY := int(float64(y) * scale)

	// 将窗口客户区坐标转为屏幕坐标
	type POINT struct{ X, Y int32 }
	pt := POINT{int32(physicalX), int32(physicalY)}
	windows.NewLazyDLL("user32.dll").NewProc("ClientToScreen").Call(
		masterHwnd, uintptr(unsafe.Pointer(&pt)),
	)
	sendMouseClick(int(pt.X), int(pt.Y), button)
	return nil
}

func (a *APIAdapter) FocusMasterWindow() error {
	masterHwnd, err := a.getMasterHWND()
	if err != nil {
		return err
	}
	return a.svc.provider.SetForegroundWindow(common.WindowHandle(masterHwnd))
}

// --- 内部辅助 ---

func (a *APIAdapter) getMasterHWND() (uintptr, error) {
	if a.svc.syncManager == nil {
		return 0, fmt.Errorf("sync manager 未初始化")
	}
	master := a.svc.syncManager.GetMasterWindow()
	if master == nil {
		return 0, fmt.Errorf("未设置主控窗口")
	}
	if master.HWND == 0 {
		return 0, fmt.Errorf("主控窗口句柄无效")
	}
	return master.HWND, nil
}

func (a *APIAdapter) allWindowNumbers() []int {
	if a.svc.syncManager == nil {
		return nil
	}
	all := a.svc.syncManager.GetAllImportedWindows()
	nums := make([]int, 0, len(all))
	for _, w := range all {
		nums = append(nums, w.Number)
	}
	// 强制排序，确保排列顺序一致（从小到大）
	sort.Ints(nums)
	return nums
}

// --- Windows SendInput 实现 ---

type keyboardInput struct {
	typ uint32
	ki  struct {
		wVk         uint16
		wScan       uint16
		dwFlags     uint32
		time        uint32
		dwExtraInfo uintptr
		_           [8]byte
	}
}

const (
	inputKeyboard   = uint32(1)
	inputMouse      = uint32(0)
	keyEventKeyUp   = uint32(0x0002)
	keyEventUnicode = uint32(0x0004)
	mouseEventMove  = uint32(0x0001)
	mouseEventAbsol = uint32(0x8000)
	mouseEventLDown = uint32(0x0002)
	mouseEventLUp   = uint32(0x0004)
	mouseEventRDown = uint32(0x0008)
	mouseEventRUp   = uint32(0x0010)
	mouseEventMDown = uint32(0x0020)
	mouseEventMUp   = uint32(0x0040)
)

var (
	user32         = windows.NewLazyDLL("user32.dll")
	procSendInput  = user32.NewProc("SendInput")
	procGetSysMetr = user32.NewProc("GetSystemMetrics")
)

func sendComboKeys(keys string) error {
	parts := strings.Split(strings.ToLower(keys), "+")
	var modifiers []uint16
	var mainKey uint16

	for _, part := range parts {
		part = strings.TrimSpace(part)
		switch part {
		case "ctrl", "control":
			modifiers = append(modifiers, 0x11)
		case "shift":
			modifiers = append(modifiers, 0x10)
		case "alt":
			modifiers = append(modifiers, 0x12)
		case "win":
			modifiers = append(modifiers, 0x5B)
		case "enter", "return":
			mainKey = 0x0D
		case "tab":
			mainKey = 0x09
		case "esc", "escape":
			mainKey = 0x1B
		case "space":
			mainKey = 0x20
		case "backspace":
			mainKey = 0x08
		case "delete", "del":
			mainKey = 0x2E
		case "up":
			mainKey = 0x26
		case "down":
			mainKey = 0x28
		case "left":
			mainKey = 0x25
		case "right":
			mainKey = 0x27
		case "home":
			mainKey = 0x24
		case "end":
			mainKey = 0x23
		case "f5":
			mainKey = 0x74
		case "0", "num0":
			mainKey = 0x30
		case "-", "minus":
			mainKey = 0xBD // VK_OEM_MINUS
		case "=", "plus":
			mainKey = 0xBB // VK_OEM_PLUS
		default:
			if len(part) == 1 {
				mainKey = uint16(strings.ToUpper(part)[0])
			}
		}
	}

	if mainKey == 0 && len(modifiers) == 0 {
		return fmt.Errorf("无法解析按键: %s", keys)
	}

	for _, mod := range modifiers {
		sendVkDown(mod)
	}
	if mainKey != 0 {
		sendVkDown(mainKey)
		time.Sleep(10 * time.Millisecond)
		sendVkUp(mainKey)
	}
	for i := len(modifiers) - 1; i >= 0; i-- {
		sendVkUp(modifiers[i])
	}
	return nil
}

func sendVkDown(vk uint16) {
	inp := keyboardInput{typ: inputKeyboard}
	inp.ki.wVk = vk
	procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
}

func sendVkUp(vk uint16) {
	inp := keyboardInput{typ: inputKeyboard}
	inp.ki.wVk = vk
	inp.ki.dwFlags = keyEventKeyUp
	procSendInput.Call(1, uintptr(unsafe.Pointer(&inp)), unsafe.Sizeof(inp))
}

func sendTextInput(text string) error {
	for _, ch := range text {
		down := keyboardInput{typ: inputKeyboard}
		down.ki.wScan = uint16(ch)
		down.ki.dwFlags = keyEventUnicode
		procSendInput.Call(1, uintptr(unsafe.Pointer(&down)), unsafe.Sizeof(down))

		up := keyboardInput{typ: inputKeyboard}
		up.ki.wScan = uint16(ch)
		up.ki.dwFlags = keyEventUnicode | keyEventKeyUp
		procSendInput.Call(1, uintptr(unsafe.Pointer(&up)), unsafe.Sizeof(up))
	}
	return nil
}

func sendMouseClick(screenX, screenY int, button string) {
	type mouseInput struct {
		typ uint32
		mi  struct {
			dx          int32
			dy          int32
			mouseData   uint32
			dwFlags     uint32
			time        uint32
			dwExtraInfo uintptr
		}
	}

	smCX, _, _ := procGetSysMetr.Call(0)
	smCY, _, _ := procGetSysMetr.Call(1)
	absX := int32(screenX * 65535 / int(smCX))
	absY := int32(screenY * 65535 / int(smCY))

	var downFlag, upFlag uint32
	switch button {
	case "right":
		downFlag, upFlag = mouseEventRDown, mouseEventRUp
	case "middle":
		downFlag, upFlag = mouseEventMDown, mouseEventMUp
	default:
		downFlag, upFlag = mouseEventLDown, mouseEventLUp
	}

	down := mouseInput{typ: inputMouse}
	down.mi.dx = absX
	down.mi.dy = absY
	down.mi.dwFlags = mouseEventMove | mouseEventAbsol | downFlag
	procSendInput.Call(1, uintptr(unsafe.Pointer(&down)), unsafe.Sizeof(down))

	time.Sleep(20 * time.Millisecond)

	up := mouseInput{typ: inputMouse}
	up.mi.dx = absX
	up.mi.dy = absY
	up.mi.dwFlags = mouseEventMove | mouseEventAbsol | upFlag
	procSendInput.Call(1, uintptr(unsafe.Pointer(&up)), unsafe.Sizeof(up))
}

func getDpiForWindow(hwnd uintptr) uint32 {
	// 获取窗口所在的 DPI
	// Windows 10 1607+ 支持 GetDpiForWindow
	proc := user32.NewProc("GetDpiForWindow")
	if proc.Find() == nil {
		ret, _, _ := proc.Call(hwnd)
		if ret != 0 {
			return uint32(ret)
		}
	}
	// 降级方案：获取系统全局 DPI
	procDC := user32.NewProc("GetDC")
	procGetDeviceCaps := windows.NewLazyDLL("gdi32.dll").NewProc("GetDeviceCaps")
	const LOGPIXELSX = 88
	hdc, _, _ := procDC.Call(0)
	if hdc != 0 {
		defer windows.NewLazyDLL("user32.dll").NewProc("ReleaseDC").Call(0, hdc)
		dpi, _, _ := procGetDeviceCaps.Call(hdc, LOGPIXELSX)
		if dpi != 0 {
			return uint32(dpi)
		}
	}
	return 96 // 默认 100% 缩放
}

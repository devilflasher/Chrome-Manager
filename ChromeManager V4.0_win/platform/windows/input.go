//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 输入模拟相关常量
const (
	INPUT_KEYBOARD    = 1
	INPUT_MOUSE       = 0
	KEYEVENTF_KEYUP   = 0x0002
	KEYEVENTF_UNICODE = 0x0004

	// 鼠标事件标志
	MOUSEEVENTF_MOVE       = 0x0001
	MOUSEEVENTF_LEFTDOWN   = 0x0002
	MOUSEEVENTF_LEFTUP     = 0x0004
	MOUSEEVENTF_RIGHTDOWN  = 0x0008
	MOUSEEVENTF_RIGHTUP    = 0x0010
	MOUSEEVENTF_MIDDLEDOWN = 0x0020
	MOUSEEVENTF_MIDDLEUP   = 0x0040
	MOUSEEVENTF_WHEEL      = 0x0800
	MOUSEEVENTF_ABSOLUTE   = 0x8000

	// 剪贴板相关常量
	CF_UNICODETEXT = 13
	GMEM_MOVEABLE  = 0x0002
)

// 输入模拟相关 API
var (
	procSendInput = user32.NewProc("SendInput")

	// 剪贴板相关 API
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGetClipboardData = user32.NewProc("GetClipboardData")
)

// KEYBDINPUT 键盘输入结构
type KEYBDINPUT struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// MOUSEINPUT 鼠标输入结构
type MOUSEINPUT struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

// INPUT Windows 输入结构
type INPUT struct {
	inputType uint32
	union     [24]byte // 足够容纳 KEYBDINPUT 或 MOUSEINPUT
}

// SendKeyInput 模拟按键输入
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SendKeyInput(keyCode common.KeyCode, keyDown bool) error {
	var input INPUT
	input.inputType = INPUT_KEYBOARD

	// 构造 KEYBDINPUT
	kbd := KEYBDINPUT{
		wVk:     uint16(keyCode),
		wScan:   0,
		dwFlags: 0,
		time:    0,
	}

	if !keyDown {
		kbd.dwFlags = KEYEVENTF_KEYUP
	}

	// 将 KEYBDINPUT 复制到 union
	*(*KEYBDINPUT)(unsafe.Pointer(&input.union[0])) = kbd //nolint:govet // Windows API interaction

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("SendInput failed: %v", err)
	}

	return nil
}

// SendKeyPress 模拟按键按下和释放
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SendKeyPress(keyCode common.KeyCode) error {
	// 按下
	if err := p.SendKeyInput(keyCode, true); err != nil {
		return err
	}

	// 释放
	return p.SendKeyInput(keyCode, false)
}

// SendTextInput 模拟文本输入
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SendTextInput(text string) error {
	for _, char := range text {
		// 构造 Unicode 输入
		var input INPUT
		input.inputType = INPUT_KEYBOARD

		kbd := KEYBDINPUT{
			wVk:     0,
			wScan:   uint16(char),
			dwFlags: KEYEVENTF_UNICODE,
			time:    0,
		}

		// 按下
		*(*KEYBDINPUT)(unsafe.Pointer(&input.union[0])) = kbd //nolint:govet // Windows API interaction
		ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
		if ret == 0 {
			return fmt.Errorf("SendInput failed for char '%c': %v", char, err)
		}

		// 释放
		kbd.dwFlags = KEYEVENTF_UNICODE | KEYEVENTF_KEYUP
		*(*KEYBDINPUT)(unsafe.Pointer(&input.union[0])) = kbd //nolint:govet // Windows API interaction
		ret, _, err = procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
		if ret == 0 {
			return fmt.Errorf("SendInput release failed for char '%c': %v", char, err)
		}
	}

	return nil
}

// SendMouseClick 模拟鼠标点击
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SendMouseClick(x, y int, button common.MouseButton) error {
	// 移动鼠标到指定位置
	if err := p.MoveMouse(x, y); err != nil {
		return err
	}

	// 确定按键标志
	var downFlag, upFlag uint32
	switch button {
	case common.MouseLeft:
		downFlag = MOUSEEVENTF_LEFTDOWN
		upFlag = MOUSEEVENTF_LEFTUP
	case common.MouseRight:
		downFlag = MOUSEEVENTF_RIGHTDOWN
		upFlag = MOUSEEVENTF_RIGHTUP
	case common.MouseMiddle:
		downFlag = MOUSEEVENTF_MIDDLEDOWN
		upFlag = MOUSEEVENTF_MIDDLEUP
	default:
		return fmt.Errorf("unsupported mouse button: %v", button)
	}

	// 按下
	var input INPUT
	input.inputType = INPUT_MOUSE
	mouse := MOUSEINPUT{
		dx:        0,
		dy:        0,
		mouseData: 0,
		dwFlags:   downFlag,
		time:      0,
	}
	*(*MOUSEINPUT)(unsafe.Pointer(&input.union[0])) = mouse //nolint:govet // Windows API interaction

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("mouse down failed: %v", err)
	}

	// 释放
	mouse.dwFlags = upFlag
	*(*MOUSEINPUT)(unsafe.Pointer(&input.union[0])) = mouse //nolint:govet // Windows API interaction

	ret, _, err = procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("mouse up failed: %v", err)
	}

	return nil
}

// MoveMouse 移动鼠标到指定位置
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) MoveMouse(x, y int) error {
	// 将屏幕坐标转换为绝对坐标 (0-65535)
	absX := int32((x * 65536) / getSystemMetrics(0))
	absY := int32((y * 65536) / getSystemMetrics(1))

	var input INPUT
	input.inputType = INPUT_MOUSE

	mouse := MOUSEINPUT{
		dx:        absX,
		dy:        absY,
		mouseData: 0,
		dwFlags:   MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE,
		time:      0,
	}

	*(*MOUSEINPUT)(unsafe.Pointer(&input.union[0])) = mouse //nolint:govet // Windows API interaction

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("mouse move failed: %v", err)
	}

	return nil
}

// SendMouseWheel 模拟鼠标滚轮
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SendMouseWheel(delta int) error {
	var input INPUT
	input.inputType = INPUT_MOUSE

	mouse := MOUSEINPUT{
		dx:        0,
		dy:        0,
		mouseData: uint32(delta),
		dwFlags:   MOUSEEVENTF_WHEEL,
		time:      0,
	}

	*(*MOUSEINPUT)(unsafe.Pointer(&input.union[0])) = mouse //nolint:govet // Windows API interaction

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("mouse wheel failed: %v", err)
	}

	return nil
}

// SetClipboard 设置剪贴板内容
// 从 utils/input_manager.go 迁移完成 ✅
func (p *Provider) SetClipboard(text string) error {
	// 打开剪贴板
	ret, _, err := procOpenClipboard.Call(0)
	if ret == 0 {
		return fmt.Errorf("打开剪贴板失败: %v", err)
	}
	defer procCloseClipboard.Call()

	// 清空剪贴板
	ret, _, err = procEmptyClipboard.Call()
	if ret == 0 {
		return fmt.Errorf("清空剪贴板失败: %v", err)
	}

	// 转换文本为 UTF-16
	utf16Text, err := syscall.UTF16FromString(text)
	if err != nil {
		return fmt.Errorf("转换文本为 UTF-16 失败: %v", err)
	}

	// 计算需要的内存大小（字节）
	size := len(utf16Text) * 2

	// 分配全局内存
	hMem, _, err := procGlobalAlloc.Call(GMEM_MOVEABLE, uintptr(size))
	if hMem == 0 {
		return fmt.Errorf("分配全局内存失败: %v", err)
	}

	// 锁定内存并获取指针
	pMem, _, err := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return fmt.Errorf("锁定全局内存失败: %v", err)
	}

	// 复制 UTF-16 数据到全局内存
	// pMem is a valid pointer from GlobalLock, safe to use
	// pMem is a valid pointer from GlobalLock, safe to use
	destSlice := unsafe.Slice((*uint16)(unsafe.Pointer(pMem)), len(utf16Text)) //nolint:govet,unsafeptr // Windows API interaction
	copy(destSlice, utf16Text)

	// 解锁内存
	procGlobalUnlock.Call(hMem)

	// 设置剪贴板数据
	ret, _, err = procSetClipboardData.Call(CF_UNICODETEXT, hMem)
	if ret == 0 {
		return fmt.Errorf("设置剪贴板数据失败: %v", err)
	}

	return nil
}

// GetClipboard 获取剪贴板内容
// 新实现 ✅
func (p *Provider) GetClipboard() (string, error) {
	// 打开剪贴板
	ret, _, err := procOpenClipboard.Call(0)
	if ret == 0 {
		return "", fmt.Errorf("打开剪贴板失败: %v", err)
	}
	defer procCloseClipboard.Call()

	// 获取剪贴板数据
	hMem, _, err := procGetClipboardData.Call(CF_UNICODETEXT)
	if hMem == 0 {
		return "", fmt.Errorf("获取剪贴板数据失败: %v", err)
	}

	// 锁定内存
	pMem, _, err := procGlobalLock.Call(hMem)
	if pMem == 0 {
		return "", fmt.Errorf("锁定全局内存失败: %v", err)
	}
	defer procGlobalUnlock.Call(hMem)

	// 读取 UTF-16 字符串
	// pMem is a valid pointer from GlobalLock, safe to use
	// pMem is a valid pointer from GlobalLock, safe to use
	text := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(pMem))) //nolint:govet,unsafeptr // Windows API interaction

	return text, nil
}

// InputRandomNumber 在指定窗口输入随机数
// 备注: 这是业务逻辑,已转移至应用层使用基础API组合实现
func (p *Provider) InputRandomNumber(handle common.WindowHandle, config common.InputConfig) error {
	return common.ErrNotImplemented
}

// getSystemMetrics 获取系统度量
var procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

func getSystemMetrics(index int32) int {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(ret)
}

package utils

import (
	"bufio"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	INPUT_KEYBOARD  = 1
	KEYEVENTF_KEYUP = 0x0002
	VK_CONTROL      = 0x11
	VK_A            = 0x41
	VK_V            = 0x56
	WM_CHAR         = 0x0102
	WM_KEYDOWN      = 0x0100
	WM_KEYUP        = 0x0101

	// 剪贴板相关常量
	CF_UNICODETEXT = 13
	GMEM_MOVEABLE  = 0x0002
)

type KEYBDINPUT struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type INPUT struct {
	inputType uint32
	ki        KEYBDINPUT
	padding   [8]byte
}

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
)

type RandomInputConfig struct {
	MinValue      float64 `json:"minValue"`
	MaxValue      float64 `json:"maxValue"`
	IsFloat       bool    `json:"isFloat"`
	DecimalPlaces int     `json:"decimalPlaces"`
	Overwrite     bool    `json:"overwrite"`
	Delayed       bool    `json:"delayed"`
}

type TextInputConfig struct {
	FilePath      string   `json:"filePath"`
	InputMethod   string   `json:"inputMethod"`
	Overwrite     bool     `json:"overwrite"`
	Delayed       bool     `json:"delayed"`
	DirectContent []string `json:"directContent,omitempty"` // 直接内容，用于从前端传递文本行
}

type InputManager struct {
	rand   *rand.Rand
	active atomic.Bool // 标记是否正在执行操作
}

func NewInputManager() *InputManager {
	return &InputManager{
		rand: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (im *InputManager) ForceReset() {
	im.active.Store(false)
}

func (im *InputManager) IsActive() bool {
	return im.active.Load()
}

func (im *InputManager) CanStart() bool {
	return !im.active.Load()
}

func (im *InputManager) InputRandomNumbers(windowHWNDs []uintptr, config RandomInputConfig) error {
	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有提供窗口句柄")
	}

	if config.MaxValue <= config.MinValue {
		return fmt.Errorf("最大值必须大于最小值")
	}

	// 为每个窗口生成随机数字文本
	randomTexts := make([]string, len(windowHWNDs))
	for i := range randomTexts {
		if config.IsFloat {
			randomNumber := config.MinValue + im.rand.Float64()*(config.MaxValue-config.MinValue)
			format := fmt.Sprintf("%%.%df", config.DecimalPlaces)
			randomTexts[i] = fmt.Sprintf(format, randomNumber)

			if strings.Contains(randomTexts[i], ".") {
				randomTexts[i] = strings.TrimRight(randomTexts[i], "0")
				randomTexts[i] = strings.TrimRight(randomTexts[i], ".")
				if randomTexts[i] == "" {
					randomTexts[i] = "0"
				}
			}
		} else {
			randomNumber := int(config.MinValue) + im.rand.Intn(int(config.MaxValue-config.MinValue)+1)
			randomTexts[i] = strconv.Itoa(randomNumber)
		}
	}

	return im.InputTextFromLines(windowHWNDs, randomTexts, "sequential", config.Overwrite, config.Delayed)
}

func (im *InputManager) InputTextFromFile(windowHWNDs []uintptr, config TextInputConfig) error {
	if !im.active.CompareAndSwap(false, true) {
		return fmt.Errorf("输入操作正在进行中，请稍后再试")
	}

	// 确保操作完成后重置状态
	defer func() {
		im.active.Store(false)
	}()

	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有提供窗口句柄")
	}

	var lines []string
	var err error

	if config.FilePath == "DIRECT_CONTENT" && len(config.DirectContent) > 0 {
		lines = config.DirectContent
	} else {
		// 读取文件内容
		lines, err = im.readTextFile(config.FilePath)
		if err != nil {
			return fmt.Errorf("读取文件失败: %v", err)
		}
	}

	if len(lines) == 0 {
		return fmt.Errorf("文件内容为空")
	}

	// 准备文本行
	textLines := make([]string, len(windowHWNDs))
	if config.InputMethod == "random" {
		// 随机分配：洗牌算法，避免不需要的重复
		availableLines := make([]string, len(lines))
		copy(availableLines, lines)
		im.rand.Shuffle(len(availableLines), func(i, j int) {
			availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
		})

		lineIndex := 0
		for i := range textLines {
			if lineIndex >= len(availableLines) {
				// 库被抽空，重新洗牌
				im.rand.Shuffle(len(availableLines), func(i, j int) {
					availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
				})
				lineIndex = 0
			}
			textLines[i] = availableLines[lineIndex]
			lineIndex++
		}
	} else {
		// 顺序分配
		for i := range textLines {
			textLines[i] = lines[i%len(lines)]
		}
	}

	successCount := 0
	for i, hwnd := range windowHWNDs {
		text := textLines[i]

		// 输入到窗口
		if err := im.inputTextToWindow(hwnd, text, config.Overwrite, config.Delayed); err != nil {
			log.Printf("向窗口 %d 输入失败: %v", hwnd, err)
		} else {
			successCount++
		}

		// 添加窗口间的延迟
		if i < len(windowHWNDs)-1 {
			if config.Delayed {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(10 * time.Millisecond) // 极速模式：微小间隔
			}
		}
	}

	if successCount == 0 {
		return fmt.Errorf("所有窗口输入都失败了")
	}

	return nil
}

func (im *InputManager) InputTextFromLines(windowHWNDs []uintptr, lines []string, inputMethod string, overwrite, delayed bool) error {

	if !im.active.CompareAndSwap(false, true) {
		return fmt.Errorf("输入操作正在进行中，请稍后再试")
	}
	defer func() {
		im.active.Store(false)
	}()

	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有提供窗口句柄")
	}

	if len(lines) == 0 {
		return fmt.Errorf("文本行为空")
	}

	// 准备文本行
	textLines := make([]string, len(windowHWNDs))
	if inputMethod == "random" {
		// 随机分配：洗牌算法，避免不需要的重复
		availableLines := make([]string, len(lines))
		copy(availableLines, lines)
		im.rand.Shuffle(len(availableLines), func(i, j int) {
			availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
		})

		lineIndex := 0
		for i := range textLines {
			if lineIndex >= len(availableLines) {
				// 库被抽空，重新洗牌
				im.rand.Shuffle(len(availableLines), func(i, j int) {
					availableLines[i], availableLines[j] = availableLines[j], availableLines[i]
				})
				lineIndex = 0
			}
			textLines[i] = availableLines[lineIndex]
			lineIndex++
		}
	} else {
		// 顺序分配
		for i := range textLines {
			textLines[i] = lines[i%len(lines)]
		}
	}

	successCount := 0
	for i, hwnd := range windowHWNDs {
		text := textLines[i]

		// 输入到窗口
		if err := im.inputTextToWindow(hwnd, text, overwrite, delayed); err != nil {
			log.Printf("向窗口 %d 输入失败: %v", hwnd, err)
		} else {
			successCount++
		}

		// 添加窗口间的延迟
		if i < len(windowHWNDs)-1 {
			if delayed {
				time.Sleep(200 * time.Millisecond)
			} else {
				time.Sleep(10 * time.Millisecond) // 极速模式：微小间隔
			}
		}
	}

	if successCount == 0 {
		return fmt.Errorf("所有窗口输入都失败了")
	}

	return nil
}

func (im *InputManager) inputTextToWindow(hwnd uintptr, text string, overwrite, delayed bool) error {
	// 添加 panic 恢复机制,防止文本输入崩溃导致程序退出
	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ [PANIC] inputTextToWindow 发生 panic: %v, 窗口=%d", r, hwnd)
		}
	}()

	// 激活窗口
	if err := im.activateWindow(hwnd); err != nil {
		return fmt.Errorf("激活窗口失败: %v", err)
	}

	// 等待窗口获得焦点
	if delayed {
		time.Sleep(100 * time.Millisecond)
	} else {
		time.Sleep(20 * time.Millisecond) // 极速模式获取完焦点缩短空挡期
	}

	// 如果需要覆盖，先全选
	if overwrite {
		im.selectAll()
		if delayed {
			time.Sleep(50 * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond) // 全选完毕留极短反应时间以防粘连
		}
	}

	// 输入文本
	if delayed {
		// 逐字符输入，模拟人工打字
		return im.typeTextSlowly(text)
	} else {
		// 快速输入
		return im.typeTextFast(text)
	}
}

func (im *InputManager) activateWindow(hwnd uintptr) error {
	return SetForegroundWindow(hwnd)
}

func (im *InputManager) selectAll() error {
	if err := im.sendKeyDown(VK_CONTROL); err != nil {
		return err
	}

	if err := im.sendKeyDown(VK_A); err != nil {
		im.sendKeyUp(VK_CONTROL)
		return err
	}

	if err := im.sendKeyUp(VK_A); err != nil {
		im.sendKeyUp(VK_CONTROL)
		return err
	}

	if err := im.sendKeyUp(VK_CONTROL); err != nil {
		return err
	}

	return nil
}

func (im *InputManager) typeTextFast(text string) error {
	// 使用剪贴板一次性粘贴文本
	if err := im.pasteTextViaClipboard(text); err != nil {
		// 如果剪贴板失败，降级到逐字符输入（但延迟较短）
		for _, r := range text {
			if err := im.sendChar(r); err != nil {
				return fmt.Errorf("输入字符 '%c' 失败: %v", r, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	return nil
}

func (im *InputManager) typeTextSlowly(text string) error {
	for _, r := range text {
		// 发送字符
		if err := im.sendChar(r); err != nil {
			return fmt.Errorf("输入字符 '%c' 失败: %v", r, err)
		}

		delay := 50 + im.rand.Intn(100)
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	return nil
}

func (im *InputManager) sendKeyDown(vk uint16) error {
	input := INPUT{
		inputType: INPUT_KEYBOARD,
		ki: KEYBDINPUT{
			wVk:     vk,
			wScan:   0,
			dwFlags: 0,
			time:    0,
		},
	}

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("SendInput failed: %v", err)
	}
	return nil
}

func (im *InputManager) sendKeyUp(vk uint16) error {
	input := INPUT{
		inputType: INPUT_KEYBOARD,
		ki: KEYBDINPUT{
			wVk:     vk,
			wScan:   0,
			dwFlags: KEYEVENTF_KEYUP,
			time:    0,
		},
	}

	ret, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	if ret == 0 {
		return fmt.Errorf("SendInput failed: %v", err)
	}
	return nil
}

func (im *InputManager) sendChar(char rune) error {
	// 发送字符按下事件
	input1 := INPUT{
		inputType: INPUT_KEYBOARD,
		ki: KEYBDINPUT{
			wVk:     0,
			wScan:   uint16(char),
			dwFlags: 0x0004,
			time:    0,
		},
	}

	// 发送字符释放事件
	input2 := INPUT{
		inputType: INPUT_KEYBOARD,
		ki: KEYBDINPUT{
			wVk:     0,
			wScan:   uint16(char),
			dwFlags: 0x0004 | KEYEVENTF_KEYUP,
			time:    0,
		},
	}

	// 发送两个事件
	inputs := [2]INPUT{input1, input2}
	ret, _, _ := procSendInput.Call(2, uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(INPUT{}))
	if ret == 0 {
		return fmt.Errorf("SendInput failed")
	}

	return nil
}

func (im *InputManager) readTextFile(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %v", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" { // 跳过空行
			lines = append(lines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取文件失败: %v", err)
	}

	return lines, nil
}

func (im *InputManager) ValidateRandomConfig(config RandomInputConfig) error {
	if config.MaxValue <= config.MinValue {
		return fmt.Errorf("最大值必须大于最小值")
	}

	if config.IsFloat && config.DecimalPlaces < 0 {
		return fmt.Errorf("小数位数不能为负数")
	}

	if config.IsFloat && config.DecimalPlaces > 10 {
		return fmt.Errorf("小数位数不能超过10位")
	}

	return nil
}

func (im *InputManager) ValidateTextConfig(config TextInputConfig) error {
	// 如果是直接内容模式，跳过文件路径验证
	if config.FilePath == "DIRECT_CONTENT" {
		if config.InputMethod != "sequential" && config.InputMethod != "random" {
			return fmt.Errorf("输入方法必须是 'sequential' 或 'random'")
		}
		return nil
	}

	if config.FilePath == "" {
		return fmt.Errorf("必须指定文件路径")
	}

	if _, err := os.Stat(config.FilePath); os.IsNotExist(err) {
		return fmt.Errorf("文件不存在: %s", config.FilePath)
	}

	if config.InputMethod != "sequential" && config.InputMethod != "random" {
		return fmt.Errorf("输入方法必须是 'sequential' 或 'random'")
	}

	return nil
}

func (im *InputManager) GetFilePreview(filePath string) ([]string, error) {
	lines, err := im.readTextFile(filePath)
	if err != nil {
		return nil, err
	}

	if len(lines) <= 10 {
		return lines, nil
	}

	return lines[:10], nil
}

// pasteTextViaClipboard 通过剪贴板一次性粘贴文本
func (im *InputManager) pasteTextViaClipboard(text string) error {
	// 将文本设置到剪贴板
	if err := im.setClipboardText(text); err != nil {
		return fmt.Errorf("设置剪贴板失败: %v", err)
	}

	// 等待剪贴板操作完成
	time.Sleep(50 * time.Millisecond)

	// 发送 Ctrl+V 粘贴
	if err := im.sendCtrlV(); err != nil {
		return fmt.Errorf("发送 Ctrl+V 失败: %v", err)
	}

	return nil
}

// setClipboardText 设置文本到剪贴板
func (im *InputManager) setClipboardText(text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

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
	// 使用 unsafe.Slice 创建目标切片（Go 1.17+ 推荐方式）
	//nolint:govet // Windows API 调用需要 unsafe.Pointer 转换，这是标准做法
	destSlice := unsafe.Slice((*uint16)(unsafe.Pointer(uintptr(pMem))), len(utf16Text)) //nolint:govet,unsafeptr // Windows API interaction
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

// sendCtrlV 发送 Ctrl+V 组合键
func (im *InputManager) sendCtrlV() error {
	// 按下 Ctrl
	if err := im.sendKeyDown(VK_CONTROL); err != nil {
		return err
	}

	// 按下 V
	if err := im.sendKeyDown(VK_V); err != nil {
		im.sendKeyUp(VK_CONTROL) // 出错时释放 Ctrl
		return err
	}

	// 释放 V
	if err := im.sendKeyUp(VK_V); err != nil {
		im.sendKeyUp(VK_CONTROL) // 出错时释放 Ctrl
		return err
	}

	// 释放 Ctrl
	return im.sendKeyUp(VK_CONTROL)
}

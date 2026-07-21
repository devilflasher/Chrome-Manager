//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 快捷键相关 Windows API
var (
	registerHotKeyProc    = user32.NewProc("RegisterHotKey")
	unregisterHotKeyProc  = user32.NewProc("UnregisterHotKey")
	postThreadMessageProc = user32.NewProc("PostThreadMessageW")
	getMessageProc        = user32.NewProc("GetMessageW")
	translateMessageProc  = user32.NewProc("TranslateMessage")
	dispatchMessageProc   = user32.NewProc("DispatchMessageW")
	postQuitMessageProc   = user32.NewProc("PostQuitMessage")
)

// 快捷键相关常量
const (
	modAlt     = 0x0001
	modControl = 0x0002
	modShift   = 0x0004
	modWin     = 0x0008

	wmApp                 = 0x8000
	wmHotKey              = 0x0312
	messageRegisterHotkey = wmApp + 101
	messageUnregister     = wmApp + 102
	messageStopLoop       = wmApp + 103

	errorHotkeyAlreadyRegistered syscall.Errno = 1409
)

// 错误定义
var (
	errManagerUnavailable      = errors.New("global hotkey manager is unavailable")
	errHotkeyAlreadyRegistered = errors.New("hotkey already registered by another application")
	errHotkeyEmpty             = errors.New("hotkey is empty")
)

// registerRequest 快捷键注册请求
type registerRequest struct {
	id       int
	spec     hotkeySpec
	callback func()
	resultCh chan error
}

// unregisterRequest 快捷键注销请求
type unregisterRequest struct {
	id       int
	resultCh chan error
}

// hotkeySpec 内部快捷键规格（Windows特定）
type hotkeySpec struct {
	Modifiers uint32
	Key       uint32
}

// winMSG Windows 消息结构
type winMSG struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct {
		X int32
		Y int32
	}
	LPrivate uint32
}

// hotkeyManager 全局快捷键管理器
// 从 utils.GlobalHotkeyManager 迁移完成 ✅
type hotkeyManager struct {
	threadID uint32

	reqCounter         uint32
	pendingRegisters   sync.Map // map[uint32]*registerRequest
	pendingUnregisters sync.Map // map[uint32]*unregisterRequest
	callbacks          sync.Map // map[int]func()

	readyOnce sync.Once
	readyCh   chan struct{}
	stopOnce  sync.Once
	stoppedCh chan struct{}
}

// newHotkeyManager 创建并启动一个新的全局快捷键管理器
// 从 utils.NewGlobalHotkeyManager 迁移完成 ✅
func newHotkeyManager() (*hotkeyManager, error) {
	if err := registerHotKeyProc.Find(); err != nil {
		return nil, fmt.Errorf("locate RegisterHotKey: %w", err)
	}
	if err := unregisterHotKeyProc.Find(); err != nil {
		return nil, fmt.Errorf("locate UnregisterHotKey: %w", err)
	}

	manager := &hotkeyManager{
		readyCh:   make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}

	go manager.messageLoop()

	<-manager.readyCh

	return manager, nil
}

// register 注册指定 ID 的快捷键
// 从 utils.GlobalHotkeyManager.Register 迁移完成 ✅
func (m *hotkeyManager) register(id int, spec hotkeySpec, callback func()) error {
	if callback == nil {
		return fmt.Errorf("hotkey callback cannot be nil")
	}

	if m.threadID == 0 {
		return errManagerUnavailable
	}

	req := &registerRequest{
		id:       id,
		spec:     spec,
		callback: callback,
		resultCh: make(chan error, 1),
	}

	requestID := atomic.AddUint32(&m.reqCounter, 1)
	m.pendingRegisters.Store(requestID, req)

	if err := postThreadMessage(m.threadID, messageRegisterHotkey, uintptr(requestID), 0); err != nil {
		m.pendingRegisters.Delete(requestID)
		return fmt.Errorf("post register request failed: %w", err)
	}

	err := <-req.resultCh
	close(req.resultCh)
	return err
}

// unregister 取消注册指定 ID 的快捷键
// 从 utils.GlobalHotkeyManager.Unregister 迁移完成 ✅
func (m *hotkeyManager) unregister(id int) error {
	if m.threadID == 0 {
		return errManagerUnavailable
	}

	req := &unregisterRequest{
		id:       id,
		resultCh: make(chan error, 1),
	}

	requestID := atomic.AddUint32(&m.reqCounter, 1)
	m.pendingUnregisters.Store(requestID, req)

	if err := postThreadMessage(m.threadID, messageUnregister, uintptr(requestID), 0); err != nil {
		m.pendingUnregisters.Delete(requestID)
		return fmt.Errorf("post unregister request failed: %w", err)
	}

	err := <-req.resultCh
	close(req.resultCh)
	return err
}

// stop 停止消息循环并释放所有注册的快捷键
// 从 utils.GlobalHotkeyManager.Stop 迁移完成 ✅
func (m *hotkeyManager) stop() {
	m.stopOnce.Do(func() {
		if m.threadID == 0 {
			close(m.stoppedCh)
			return
		}
		_ = postThreadMessage(m.threadID, messageStopLoop, 0, 0)
		<-m.stoppedCh
	})
}

// messageLoop Windows 消息循环
// 从 utils.GlobalHotkeyManager.messageLoop 迁移完成 ✅
func (m *hotkeyManager) messageLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	m.threadID = windows.GetCurrentThreadId()
	m.readyOnce.Do(func() {
		close(m.readyCh)
	})

	var msg winMSG
	for {
		ret, err := getNextMessage(&msg)
		if err != nil {
			break
		}
		if ret == 0 {
			break
		}

		switch msg.Message {
		case messageRegisterHotkey:
			m.handleRegister(uint32(msg.WParam))
		case messageUnregister:
			m.handleUnregister(uint32(msg.WParam))
		case messageStopLoop:
			m.handleStop()
			postQuitMessage(0)
		case wmHotKey:
			id := int(msg.WParam)
			if cb, ok := m.callbacks.Load(id); ok {
				if fn, ok := cb.(func()); ok {
					go fn()
				}
			}
		default:
			translateMessage(&msg)
			dispatchMessage(&msg)
		}
	}

	close(m.stoppedCh)
}

// handleRegister 处理快捷键注册
func (m *hotkeyManager) handleRegister(requestID uint32) {
	value, ok := m.pendingRegisters.LoadAndDelete(requestID)
	if !ok {
		return
	}

	req := value.(*registerRequest)

	r, _, callErr := registerHotKeyProc.Call(
		0,
		uintptr(req.id),
		uintptr(req.spec.Modifiers),
		uintptr(req.spec.Key),
	)

	if r == 0 {
		lastErr := windows.GetLastError()
		if lastErr == errorHotkeyAlreadyRegistered {
			req.resultCh <- errHotkeyAlreadyRegistered
		} else if lastErr != nil && lastErr != syscall.Errno(0) {
			req.resultCh <- fmt.Errorf("register hotkey failed: %w", lastErr)
		} else {
			req.resultCh <- fmt.Errorf("register hotkey failed: %v", callErr)
		}
		return
	}

	m.callbacks.Store(req.id, req.callback)
	req.resultCh <- nil
}

// handleUnregister 处理快捷键注销
func (m *hotkeyManager) handleUnregister(requestID uint32) {
	value, ok := m.pendingUnregisters.LoadAndDelete(requestID)
	if !ok {
		return
	}

	req := value.(*unregisterRequest)

	r, _, callErr := unregisterHotKeyProc.Call(0, uintptr(req.id))
	if r == 0 {
		lastErr := windows.GetLastError()
		if lastErr != nil && lastErr != syscall.Errno(0) {
			req.resultCh <- fmt.Errorf("unregister hotkey failed: %w", lastErr)
		} else {
			req.resultCh <- fmt.Errorf("unregister hotkey failed: %v", callErr)
		}
		return
	}

	m.callbacks.Delete(req.id)
	req.resultCh <- nil
}

// handleStop 处理停止请求
func (m *hotkeyManager) handleStop() {
	m.callbacks.Range(func(key, value any) bool {
		id, ok := key.(int)
		if !ok {
			return true
		}
		unregisterHotKeyProc.Call(0, uintptr(id))
		return true
	})

	m.pendingRegisters.Range(func(key, value any) bool {
		if req, ok := value.(*registerRequest); ok {
			req.resultCh <- errManagerUnavailable
		}
		return true
	})

	m.pendingUnregisters.Range(func(key, value any) bool {
		if req, ok := value.(*unregisterRequest); ok {
			req.resultCh <- errManagerUnavailable
		}
		return true
	})
}

// 辅助函数

func postThreadMessage(threadID uint32, message uint32, wParam, lParam uintptr) error {
	r, _, err := postThreadMessageProc.Call(uintptr(threadID), uintptr(message), wParam, lParam)
	if r == 0 {
		if err != nil {
			if errno, ok := err.(syscall.Errno); ok && errno == 0 {
				return syscall.EINVAL
			}
			return err
		}
		return syscall.EINVAL
	}
	return nil
}

func getNextMessage(msg *winMSG) (int32, error) {
	r, _, err := getMessageProc.Call(uintptr(unsafe.Pointer(msg)), 0, 0, 0)
	result := int32(r)
	if result == -1 {
		if err != nil {
			if errno, ok := err.(syscall.Errno); ok && errno == 0 {
				return result, syscall.EINVAL
			}
			return result, err
		}
		return result, syscall.EINVAL
	}
	return result, nil
}

func translateMessage(msg *winMSG) {
	translateMessageProc.Call(uintptr(unsafe.Pointer(msg)))
}

func dispatchMessage(msg *winMSG) {
	dispatchMessageProc.Call(uintptr(unsafe.Pointer(msg)))
}

func postQuitMessage(code uintptr) {
	postQuitMessageProc.Call(code)
}

// specialKeys 特殊键映射表
var specialKeys = map[string]uint32{
	"SPACE":     0x20,
	"TAB":       0x09,
	"ENTER":     0x0D,
	"RETURN":    0x0D,
	"ESC":       0x1B,
	"ESCAPE":    0x1B,
	"UP":        0x26,
	"DOWN":      0x28,
	"LEFT":      0x25,
	"RIGHT":     0x27,
	"HOME":      0x24,
	"END":       0x23,
	"PAGEUP":    0x21,
	"PAGEDOWN":  0x22,
	"INSERT":    0x2D,
	"DELETE":    0x2E,
	"BACKSPACE": 0x08,
}

// parseHotkeySpec 将类似 "Ctrl+Alt+S" 的字符串解析为 hotkeySpec
// 从 utils.ParseHotkeySpec 迁移完成 ✅
func parseHotkeySpec(hotkey string) (hotkeySpec, error) {
	trimmed := strings.TrimSpace(hotkey)
	if trimmed == "" {
		return hotkeySpec{}, errHotkeyEmpty
	}

	parts := strings.Split(trimmed, "+")
	var modifiers uint32
	var key uint32
	keySet := false

	for _, raw := range parts {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}

		upper := strings.ToUpper(token)

		switch upper {
		case "CTRL", "CONTROL", "CTL":
			modifiers |= modControl
			continue
		case "ALT", "MENU":
			modifiers |= modAlt
			continue
		case "SHIFT":
			modifiers |= modShift
			continue
		case "WIN", "WINDOWS", "SUPER", "CMD", "COMMAND":
			modifiers |= modWin
			continue
		}

		if keySet {
			return hotkeySpec{}, fmt.Errorf("快捷键 '%s' 包含多个主键", trimmed)
		}

		if vk, ok := parsePrimaryKey(upper); ok {
			key = vk
			keySet = true
			continue
		}

		return hotkeySpec{}, fmt.Errorf("无法识别的按键: %s", token)
	}

	if !keySet {
		return hotkeySpec{}, fmt.Errorf("快捷键 '%s' 缺少主键", trimmed)
	}

	return hotkeySpec{
		Modifiers: modifiers,
		Key:       key,
	}, nil
}

// parsePrimaryKey 解析主键
func parsePrimaryKey(token string) (uint32, bool) {
	// 单字母或单数字
	if len(token) == 1 {
		r := rune(token[0])
		if unicode.IsLetter(r) {
			return uint32(strings.ToUpper(token)[0]), true
		}
		if unicode.IsDigit(r) {
			return uint32(token[0]), true
		}
	}

	// 功能键 F1-F24
	if strings.HasPrefix(token, "F") && len(token) >= 2 && len(token) <= 3 {
		numberPart := token[1:]
		switch numberPart {
		case "1":
			return 0x70, true
		case "2":
			return 0x71, true
		case "3":
			return 0x72, true
		case "4":
			return 0x73, true
		case "5":
			return 0x74, true
		case "6":
			return 0x75, true
		case "7":
			return 0x76, true
		case "8":
			return 0x77, true
		case "9":
			return 0x78, true
		case "10":
			return 0x79, true
		case "11":
			return 0x7A, true
		case "12":
			return 0x7B, true
		case "13":
			return 0x7C, true
		case "14":
			return 0x7D, true
		case "15":
			return 0x7E, true
		case "16":
			return 0x7F, true
		case "17":
			return 0x80, true
		case "18":
			return 0x81, true
		case "19":
			return 0x82, true
		case "20":
			return 0x83, true
		case "21":
			return 0x84, true
		case "22":
			return 0x85, true
		case "23":
			return 0x86, true
		case "24":
			return 0x87, true
		default:
			return 0, false
		}
	}

	// 特殊键
	if vk, ok := specialKeys[token]; ok {
		return vk, true
	}

	return 0, false
}

// convertToCommonSpec 将内部 hotkeySpec 转换为 common.HotkeySpec
func convertToCommonSpec(spec hotkeySpec) common.HotkeySpec {
	var modifiers common.KeyModifier

	if spec.Modifiers&modControl != 0 {
		modifiers |= common.ModControl
	}
	if spec.Modifiers&modShift != 0 {
		modifiers |= common.ModShift
	}
	if spec.Modifiers&modAlt != 0 {
		modifiers |= common.ModAlt
	}
	if spec.Modifiers&modWin != 0 {
		modifiers |= common.ModWin
	}

	return common.HotkeySpec{
		Modifiers: modifiers,
		Key:       common.KeyCode(spec.Key),
	}
}

// convertFromCommonSpec 将 common.HotkeySpec 转换为内部 hotkeySpec
func convertFromCommonSpec(spec common.HotkeySpec) hotkeySpec {
	var modifiers uint32

	if spec.Modifiers&common.ModControl != 0 {
		modifiers |= modControl
	}
	if spec.Modifiers&common.ModShift != 0 {
		modifiers |= modShift
	}
	if spec.Modifiers&common.ModAlt != 0 {
		modifiers |= modAlt
	}
	if spec.Modifiers&common.ModWin != 0 {
		modifiers |= modWin
	}

	return hotkeySpec{
		Modifiers: modifiers,
		Key:       uint32(spec.Key),
	}
}

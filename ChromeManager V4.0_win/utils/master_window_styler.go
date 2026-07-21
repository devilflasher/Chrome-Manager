package utils

import (
	"fmt"
	"unsafe"
)

const (
	DWMWA_BORDER_COLOR  = 34 // 边框颜色
	DWMWA_CAPTION_COLOR = 35 // 标题栏颜色
	DWMWA_TEXT_COLOR    = 36 // 标题栏文字颜色
)

const (
	MASTER_BORDER_COLOR = 0x000000FF // 更亮的红色边框 (纯红色)
	DWMWA_COLOR_DEFAULT = 0xFFFFFFFE
)

type MasterWindowStyler struct {
	originalTitles map[uintptr]string
	titleToHwndMap map[string]uintptr
}

func NewMasterWindowStyler() *MasterWindowStyler {
	return &MasterWindowStyler{
		originalTitles: make(map[uintptr]string),
		titleToHwndMap: make(map[string]uintptr),
	}
}

func (m *MasterWindowStyler) SetMasterWindowStyle(hwnd uintptr) error {
	if originalTitle, err := GetWindowText(hwnd); err == nil && originalTitle != "" {
		m.originalTitles[hwnd] = originalTitle
		m.titleToHwndMap[originalTitle] = hwnd // 同时记录标题映射
	}

	if err := m.setBorderColor(hwnd, MASTER_BORDER_COLOR); err != nil {
		return err
	}

	// 将窗口置顶
	procSetWindowPos.Call(
		hwnd,
		uintptr(0),
		0,
		0,
		0,
		0,
		uintptr(0x0001|0x0002|0x0010|0x0020),
	)

	// 激活窗口
	procSetForegroundWindow.Call(hwnd)

	return nil
}

func (m *MasterWindowStyler) ClearMasterWindowStyle(hwnd uintptr) error {
	return m.clearMasterWindowStyleInternal(hwnd, true)
}

func (m *MasterWindowStyler) ForceClearMasterWindowStyle(hwnd uintptr) error {
	err := m.clearMasterWindowStyleInternal(hwnd, false)

	// 如果失败，尝试按标题匹配清除所有可能的窗口
	if err != nil || !m.verifyBorderCleared(hwnd) {
		m.clearByTitleMatch(hwnd)

		// 如果还是失败，使用核弹式清除
		m.NuclearClearBorder(hwnd)
	}

	return err
}

func (m *MasterWindowStyler) verifyBorderCleared(_ uintptr) bool {
	// 暂不实现边框清除的深层验证，默认返回失败以触发强制清除
	return false
}

func (m *MasterWindowStyler) clearByTitleMatch(targetHwnd uintptr) {
	// 获取目标窗口的标题
	targetTitle, err := GetWindowText(targetHwnd)
	if err != nil {
		return
	}

	// 遍历所有记录的窗口，按标题匹配
	for recordedHwnd, recordedTitle := range m.originalTitles {
		// 如果标题匹配，尝试清除
		if recordedTitle == targetTitle {
			if recordedHwnd != targetHwnd {
				m.clearMasterWindowStyleInternal(recordedHwnd, false)
			}

			m.clearMasterWindowStyleInternal(targetHwnd, false)

			break
		}
	}
}

func (m *MasterWindowStyler) clearMasterWindowStyleInternal(hwnd uintptr, checkRecord bool) error {
	if !IsWindowValid(hwnd) {
		// 即使窗口无效，也要清理记录
		if checkRecord {
			delete(m.originalTitles, hwnd)
		}
		return nil
	}

	if checkRecord {
		if originalTitle, exists := m.originalTitles[hwnd]; exists {
			delete(m.originalTitles, hwnd)
			delete(m.titleToHwndMap, originalTitle)
		}
	}

	m.performSimpleBorderClearing(hwnd)

	return nil
}

func (m *MasterWindowStyler) setBorderColor(hwnd uintptr, color uint32) error {
	ret, _, err := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(DWMWA_BORDER_COLOR),
		uintptr(unsafe.Pointer(&color)),
		unsafe.Sizeof(color),
	)

	if ret != 0 {
		return fmt.Errorf("DwmSetWindowAttribute failed with code %d: %v", ret, err)
	}

	return nil
}

func (m *MasterWindowStyler) ClearAllMasterWindowStyles() {
	for hwnd := range m.originalTitles {
		m.ClearMasterWindowStyle(hwnd)
	}

	// 清空记录
	m.originalTitles = make(map[uintptr]string)
}

func (m *MasterWindowStyler) RecordWindowForClearing(hwnd uintptr, title string) {
	// 将此窗口添加到我们的内部记录中，确保标题匹配清除能找到它
	if m.titleToHwndMap == nil {
		m.titleToHwndMap = make(map[string]uintptr)
	}
	if m.originalTitles == nil {
		m.originalTitles = make(map[uintptr]string)
	}

	m.titleToHwndMap[title] = hwnd
	m.originalTitles[hwnd] = title
}

func (m *MasterWindowStyler) NuclearClearBorder(hwnd uintptr) error {
	colors := []uint32{
		DWMWA_COLOR_DEFAULT,
		0x00000000, // 透明
		0x00FFFFFF, // 白色
		0x00C0C0C0, // 浅灰
		0x00808080, // 灰色
		0x00404040, // 深灰
		0x00CCCCCC,
		0x00666666,
	}

	for _, color := range colors {
		if err := m.setBorderColor(hwnd, color); err == nil {
			// 强制刷新多次
			for j := 0; j < 3; j++ {
				procSetWindowPos.Call(
					hwnd,
					uintptr(0),
					0, 0, 0, 0,
					uintptr(0x0001|0x0002|0x0020),
				)
				procRedrawWindow.Call(
					hwnd,
					0, 0,
					uintptr(0x0001|0x0004|0x0010),
				)
			}
		}
	}

	return nil
}

func (m *MasterWindowStyler) performSimpleBorderClearing(hwnd uintptr) bool {
	// 直接尝试恢复默认边框
	if err := m.setBorderColor(hwnd, DWMWA_COLOR_DEFAULT); err != nil {
		if err := m.setBorderColor(hwnd, 0x00000000); err != nil {
			return false
		}
	}

	// 简单刷新
	procSetWindowPos.Call(
		hwnd, uintptr(0), 0, 0, 0, 0,
		uintptr(0x0001|0x0002|0x0020),
	)

	return true
}

func (m *MasterWindowStyler) IsSupported() bool {
	return procDwmSetWindowAttribute.Find() == nil
}

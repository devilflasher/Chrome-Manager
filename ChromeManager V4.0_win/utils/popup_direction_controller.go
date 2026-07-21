package utils

import (
	"log"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	
	MIN_POPUP_WIDTH  = 150
	MAX_POPUP_WIDTH  = 500
	MIN_POPUP_HEIGHT = 80
	MAX_POPUP_HEIGHT = 800
)


type PopupDirectionController struct {
	user32                   *syscall.LazyDLL
	procSetWindowPos         *syscall.LazyProc
	procGetWindowRect        *syscall.LazyProc
	procGetMonitorInfo       *syscall.LazyProc
	procMonitorFromWindow    *syscall.LazyProc
	procGetSystemMetrics     *syscall.LazyProc
	
	enabled                  bool
	lastProcessTime          time.Time
	processedPopups          map[uintptr]time.Time
}






func NewPopupDirectionController() *PopupDirectionController {
	user32 := syscall.NewLazyDLL("user32.dll")
	
	controller := &PopupDirectionController{
		user32:              user32,
		procSetWindowPos:    user32.NewProc("SetWindowPos"),
		procGetWindowRect:   user32.NewProc("GetWindowRect"),
		procGetMonitorInfo:  user32.NewProc("GetMonitorInfoW"),
		procMonitorFromWindow: user32.NewProc("MonitorFromWindow"),
		procGetSystemMetrics: user32.NewProc("GetSystemMetrics"),
		enabled:             true,
		processedPopups:     make(map[uintptr]time.Time),
	}

	return controller
}


func (pdc *PopupDirectionController) SetEnabled(enabled bool) {
	pdc.enabled = enabled
}


func (pdc *PopupDirectionController) IsEnabled() bool {
	return pdc.enabled
}


func (pdc *PopupDirectionController) ProcessChromePopups(chromeHWND uintptr) {
	if !pdc.enabled {
		return
	}
	
	// 限制处理频率，防止过度调用
	now := time.Now()
	if now.Sub(pdc.lastProcessTime) < 30*time.Millisecond {
		return
	}
	pdc.lastProcessTime = now
	
	
	popups := GetChromePopups(chromeHWND)
	if len(popups) == 0 {
		return
	}
	
	
	chromeRect, err := pdc.getWindowRect(chromeHWND)
	if err != nil {
		log.Printf("⚠️ 无法获取Chrome窗口位置: %v", err)
		return
	}
	
	// 获取屏幕工作区域信息
	workArea, err := pdc.getWorkArea(chromeHWND)
	if err != nil {
		log.Printf("⚠️ 无法获取工作区域信息: %v", err)
		return
	}
	
	// 处理每个弹出窗口
	for _, popup := range popups {
		pdc.processPopupDirection(popup, chromeRect, workArea)
	}
}


func (pdc *PopupDirectionController) processPopupDirection(popupHWND uintptr, chromeRect, workArea *RECT) bool {
	
	if !pdc.isChromeExtensionPopup(popupHWND) {
		return false
	}
	
	// 检查是否已经处理过此弹出窗口（避免重复处理）
	if lastProcess, exists := pdc.processedPopups[popupHWND]; exists {
		if time.Since(lastProcess) < 200*time.Millisecond {
			return false
		}
	}
	
	// 获取弹出窗口当前位置
	popupRect, err := pdc.getWindowRect(popupHWND)
	if err != nil {
		return false
	}
	
	// 计算弹出窗口尺寸
	popupWidth := popupRect.Right - popupRect.Left
	popupHeight := popupRect.Bottom - popupRect.Top
	
	// 检查弹出窗口是否已经在正确的向下位置
	if pdc.isPopupInCorrectDownwardPosition(popupRect, chromeRect) {
		pdc.processedPopups[popupHWND] = time.Now()
		return false
	}
	
	// 智能计算向下弹出的最佳位置
	newX, newY := pdc.calculateOptimalDownwardPosition(popupRect, chromeRect, workArea, popupWidth, popupHeight)
	
	// 移动弹出窗口到计算出的向下位置
	success := pdc.movePopupWindow(popupHWND, int(newX), int(newY), int(popupWidth), int(popupHeight))
	if success {
		// 记录处理时间
		pdc.processedPopups[popupHWND] = time.Now()

		// 清理过期的记录
		pdc.cleanupProcessedPopups()

		return true
	}
	
	return false
}


func (pdc *PopupDirectionController) isChromeExtensionPopup(hwnd uintptr) bool {
	// 获取窗口类名
	className, err := GetClassName(hwnd)
	if err != nil {
		return false
	}
	
	
	if !(className == "Chrome_WidgetWin_1" || 
		 className == "Chrome_RenderWidgetHostHWND" ||
		 className == "Chrome_WidgetWin_0" ||
		 strings.Contains(className, "Chrome_Widget")) {
		return false
	}
	
	// 获取窗口尺寸
	rect, err := pdc.getWindowRect(hwnd)
	if err != nil {
		return false
	}
	
	width := rect.Right - rect.Left
	height := rect.Bottom - rect.Top
	
	// 优化尺寸检查条件，更精确地识别扩展弹出窗口
	if width >= 50 && width <= 800 && height >= 50 && height <= 1000 {
		// 获取窗口标题进行进一步验证
		title, _ := GetWindowText(hwnd)
		
		// 扩展弹出窗口的特征检查
		titleLower := strings.ToLower(title)
		
		// 常见的扩展弹出窗口特征
		isExtensionPopup := title == "" || 
						   len(title) < 100 || 
						   strings.Contains(titleLower, "extension") ||
						   strings.Contains(titleLower, "plugin") ||
						   strings.Contains(titleLower, "wallet") ||
						   strings.Contains(titleLower, "okx") ||
						   strings.Contains(titleLower, "metamask") ||
						   strings.Contains(titleLower, "popup") ||
						   strings.Contains(titleLower, "menu") ||
						   
						   (len(title) == 32 && isAlphaNumeric(title))
		
		if isExtensionPopup {
			return true
		}
	}

	return false
}

func (pdc *PopupDirectionController) isPopupInCorrectDownwardPosition(popupRect, chromeRect *RECT) bool {
	
	isBelow := popupRect.Top >= chromeRect.Bottom - 10 
	
	
	popupCenterX := (popupRect.Left + popupRect.Right) / 2
	chromeCenterX := (chromeRect.Left + chromeRect.Right) / 2
	horizontalDistance := abs(popupCenterX - chromeCenterX)
	chromeWidth := chromeRect.Right - chromeRect.Left
	isWithinHorizontalRange := horizontalDistance <= chromeWidth/2 + 50 // 允许一定的水平偏移
	
	return isBelow && isWithinHorizontalRange
}


func (pdc *PopupDirectionController) calculateOptimalDownwardPosition(popupRect, chromeRect, workArea *RECT, popupWidth, popupHeight int32) (int32, int32) {
	// 优先考虑弹出窗口当前的水平位置，但确保合理
	preferredX := popupRect.Left
	
	
	chromeLeft := chromeRect.Left
	chromeRight := chromeRect.Right
	
	
	if popupRect.Right < chromeLeft - 100 || popupRect.Left > chromeRight + 100 {
		chromeCenterX := (chromeLeft + chromeRight) / 2
		preferredX = chromeCenterX - popupWidth/2
	}
	
	// 确保弹出窗口在屏幕工作区域内
	if preferredX < workArea.Left {
		preferredX = workArea.Left + 10
	} else if preferredX + popupWidth > workArea.Right {
		preferredX = workArea.Right - popupWidth - 10
	}
	
	
	newY := chromeRect.Bottom + 5
	
	// 确保弹出窗口不会超出屏幕底部（如果超出，允许但记录日志）
	if newY + popupHeight > workArea.Bottom {
		log.Printf("⚠️ 弹出窗口可能超出屏幕底部，但仍强制向下显示")
	}
	
	return preferredX, newY
}

// abs 计算绝对值
func abs(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}


func isAlphaNumeric(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 0
}


func (pdc *PopupDirectionController) getWindowRect(hwnd uintptr) (*RECT, error) {
	var rect RECT
	ret, _, _ := pdc.procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return nil, syscall.GetLastError()
	}
	return &rect, nil
}


func (pdc *PopupDirectionController) getWorkArea(hwnd uintptr) (*RECT, error) {
	// 获取窗口所在的显示器
	monitor, _, _ := pdc.procMonitorFromWindow.Call(hwnd, 2) 
	if monitor == 0 {
		return nil, syscall.GetLastError()
	}
	
	// 获取显示器信息
	var monitorInfo MONITORINFO
	monitorInfo.cbSize = uint32(unsafe.Sizeof(monitorInfo))
	
	ret, _, _ := pdc.procGetMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&monitorInfo)))
	if ret == 0 {
		return nil, syscall.GetLastError()
	}
	
	return &monitorInfo.rcWork, nil
}


func (pdc *PopupDirectionController) movePopupWindow(hwnd uintptr, x, y, width, height int) bool {
	ret, _, _ := pdc.procSetWindowPos.Call(
		hwnd,
		HWND_TOP,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		SWP_NOACTIVATE|SWP_SHOWWINDOW,
	)
	return ret != 0
}


func (pdc *PopupDirectionController) cleanupProcessedPopups() {
	now := time.Now()
	for hwnd, processTime := range pdc.processedPopups {
		if now.Sub(processTime) > 5*time.Second {
			delete(pdc.processedPopups, hwnd)
		}
	}
}


func (pdc *PopupDirectionController) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"enabled":              pdc.enabled,
		"processed_popups":     len(pdc.processedPopups),
		"last_process_time":    pdc.lastProcessTime,
	}
}
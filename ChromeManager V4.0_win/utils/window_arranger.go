package utils

import (
	"fmt"
	"log"
	"math"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

type WindowArrangeParams struct {
	StartX            int `json:"startX"`
	StartY            int `json:"startY"`
	Width             int `json:"width"`
	Height            int `json:"height"`
	HorizontalSpacing int `json:"horizontalSpacing"`
	VerticalSpacing   int `json:"verticalSpacing"`
	WindowsPerRow     int `json:"windowsPerRow"`
}

type WindowArrangeResult struct {
	Adjusted        bool `json:"adjusted"`
	RequestedWidth  int  `json:"requestedWidth"`
	RequestedHeight int  `json:"requestedHeight"`
	ActualWidth     int  `json:"actualWidth"`
	ActualHeight    int  `json:"actualHeight"`
}

type ScreenInfo struct {
	Name       string `json:"name"`
	Primary    bool   `json:"primary"`
	Left       int    `json:"left"`
	Top        int    `json:"top"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	WorkLeft   int    `json:"workLeft"`
	WorkTop    int    `json:"workTop"`
	WorkWidth  int    `json:"workWidth"`
	WorkHeight int    `json:"workHeight"`
	DeviceName string `json:"deviceName"`
}

// 补充常量
const (
	SM_CXSCREEN           = 0
	SM_CYSCREEN           = 1
	MONITOR_DEFAULTTONULL = 0
	MONITORINFOF_PRIMARY  = 1
)

type MONITORINFO struct {
	cbSize    uint32
	rcMonitor RECT
	rcWork    RECT
	dwFlags   uint32
}

var (
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfo      = user32.NewProc("GetMonitorInfoW")
)

type WindowArranger struct {
	// 添加完成回调函数
	OnArrangeComplete func() error
}

func NewWindowArranger() *WindowArranger {
	return &WindowArranger{}
}

func (wa *WindowArranger) SetOnArrangeComplete(callback func() error) {
	wa.OnArrangeComplete = callback
}

func (wa *WindowArranger) ArrangeWindows(hwndList []uintptr) error {
	if len(hwndList) == 0 {
		return fmt.Errorf("窗口列表为空")
	}

	// 获取主屏幕信息
	screenWidth := int(getSystemMetrics(SM_CXSCREEN))
	screenHeight := int(getSystemMetrics(SM_CYSCREEN))

	// 计算最佳布局
	count := len(hwndList)
	cols := int(math.Sqrt(float64(count)))
	if cols*cols < count {
		cols++
	}

	// 计算窗口大小
	windowWidth := screenWidth / cols
	windowHeight := screenHeight / ((count + cols - 1) / cols)

	// 自动排列参数
	params := WindowArrangeParams{
		StartX:            0,
		StartY:            0,
		Width:             windowWidth,
		Height:            windowHeight,
		HorizontalSpacing: 0,
		VerticalSpacing:   0,
		WindowsPerRow:     cols,
	}

	_, err := wa.CustomArrangeWindows(hwndList, params)
	return err
}

func (wa *WindowArranger) CustomArrangeWindows(hwndList []uintptr, params WindowArrangeParams) (*WindowArrangeResult, error) {
	if len(hwndList) == 0 {
		return nil, fmt.Errorf("窗口列表为空")
	}
	params = normalizeArrangeParams(params)

	result := WindowArrangeResult{
		RequestedWidth:  params.Width,
		RequestedHeight: params.Height,
		ActualWidth:     params.Width,
		ActualHeight:    params.Height,
	}
	withPerMonitorDPIContext(func() {
		result = arrangeWindowPass(hwndList, params)
		bringArrangedWindowsToTop(hwndList)
	})

	// 调用完成回调函数
	if wa.OnArrangeComplete != nil {
		if err := wa.OnArrangeComplete(); err != nil {
			log.Printf("窗口排列完成回调函数执行失败: %v", err)
		}
	}

	return &result, nil
}

func withPerMonitorDPIContext(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if err := procSetThreadDpiAwarenessContext.Find(); err != nil {
		fn()
		return
	}

	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is represented as (DPI_AWARENESS_CONTEXT)-4.
	previousContext, _, _ := procSetThreadDpiAwarenessContext.Call(^uintptr(3))
	defer func() {
		if previousContext != 0 {
			procSetThreadDpiAwarenessContext.Call(previousContext)
		}
	}()

	fn()
}

func normalizeArrangeParams(params WindowArrangeParams) WindowArrangeParams {
	if params.WindowsPerRow <= 0 {
		params.WindowsPerRow = 1
	}
	if params.Width <= 0 {
		params.Width = 500
	}
	if params.Height <= 0 {
		params.Height = 400
	}
	return params
}

func arrangeWindowPass(hwndList []uintptr, params WindowArrangeParams) WindowArrangeResult {
	result := WindowArrangeResult{
		RequestedWidth:  params.Width,
		RequestedHeight: params.Height,
		ActualWidth:     params.Width,
		ActualHeight:    params.Height,
	}

	index := 0
	currentY := params.StartY
	for index < len(hwndList) {
		currentX := params.StartX
		rowHeight := params.Height

		for col := 0; col < params.WindowsPerRow && index < len(hwndList); col++ {
			hwnd := hwndList[index]
			actualWidth, actualHeight, err := arrangeWindow(hwnd, currentX, currentY, params.Width, params.Height)
			if err != nil {
				log.Printf("移动窗口 %d 到位置 (%d, %d) 失败: %v", hwnd, currentX, currentY, err)
				actualWidth = params.Width
				actualHeight = params.Height
			}

			if actualWidth > result.ActualWidth {
				result.ActualWidth = actualWidth
			}
			if actualHeight > result.ActualHeight {
				result.ActualHeight = actualHeight
			}
			if actualWidth > params.Width+1 || actualHeight > params.Height+1 {
				result.Adjusted = true
			}
			if actualHeight > rowHeight {
				rowHeight = actualHeight
			}

			currentX += actualWidth + params.HorizontalSpacing
			index++
		}

		currentY += rowHeight + params.VerticalSpacing
	}

	return result
}

func arrangeWindow(hwnd uintptr, x, y, width, height int) (int, int, error) {
	ensureResizableWindow(hwnd)
	ShowWindow(hwnd, SW_SHOWNORMAL)

	ret, _, _ := procSetWindowPos.Call(
		hwnd,
		uintptr(HWND_TOP),
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		SWP_SHOWWINDOW|SWP_FRAMECHANGED,
	)
	if ret == 0 {
		return width, height, fmt.Errorf("failed to set window position")
	}

	moveRet, _, _ := procMoveWindow.Call(
		hwnd,
		uintptr(x),
		uintptr(y),
		uintptr(width),
		uintptr(height),
		uintptr(1),
	)
	if moveRet == 0 {
		return width, height, fmt.Errorf("failed to move window")
	}

	procUpdateWindow.Call(hwnd)
	procRedrawWindow.Call(
		hwnd,
		0,
		0,
		RDW_INVALIDATE|RDW_ERASE|RDW_FRAME|RDW_ALLCHILDREN,
	)

	rect, err := GetWindowRect(hwnd)
	if err != nil {
		return width, height, nil
	}

	actualWidth := int(rect.Right - rect.Left)
	actualHeight := int(rect.Bottom - rect.Top)
	if actualWidth <= 0 || actualHeight <= 0 {
		return width, height, nil
	}

	return actualWidth, actualHeight, nil
}

func ensureResizableWindow(hwnd uintptr) {
	gwlStyle := ^uintptr(15)
	style, _, _ := procGetWindowLongW.Call(hwnd, gwlStyle)
	style |= uintptr(WS_SIZEBOX | WS_SYSMENU)
	procSetWindowLongW.Call(hwnd, gwlStyle, style)
	procSetWindowPos.Call(
		hwnd,
		uintptr(HWND_TOP),
		0, 0, 0, 0,
		SWP_NOMOVE|SWP_NOSIZE|SWP_NOZORDER|SWP_FRAMECHANGED,
	)
}

func bringArrangedWindowsToTop(hwndList []uintptr) {
	for _, hwnd := range hwndList {
		ret, _, _ := procSetWindowPos.Call(
			hwnd,
			^uintptr(0),
			0, 0, 0, 0,
			SWP_NOMOVE|SWP_NOSIZE,
		)
		if ret == 0 {
			log.Printf("设置窗口 %d 置顶失败", hwnd)
			continue
		}
		procSetWindowPos.Call(
			hwnd,
			^uintptr(1),
			0, 0, 0, 0,
			SWP_NOMOVE|SWP_NOSIZE,
		)
	}
}

var (
	enumScreensMu       sync.Mutex
	enumScreensTarget   func(hMonitor uintptr, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr
	enumScreensCallback uintptr
	enumScreensOnce     sync.Once
)

func (wa *WindowArranger) GetScreensInfo() ([]ScreenInfo, error) {
	var screens []ScreenInfo

	// 定义回调函数
	callback := func(hMonitor uintptr, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr {
		var mi MONITORINFO
		mi.cbSize = uint32(unsafe.Sizeof(mi))

		ret, _, _ := procGetMonitorInfo.Call(hMonitor, uintptr(unsafe.Pointer(&mi)))
		if ret == 0 {
			return 1 // 继续枚举
		}

		isPrimary := (mi.dwFlags & MONITORINFOF_PRIMARY) != 0
		screenName := fmt.Sprintf("屏幕 %d", len(screens)+1)
		if isPrimary {
			screenName += " (主)"
		}

		// 计算分辨率并添加到名称
		width := int(mi.rcMonitor.Right - mi.rcMonitor.Left)
		height := int(mi.rcMonitor.Bottom - mi.rcMonitor.Top)
		screenName += fmt.Sprintf(" - %dx%d", width, height)

		screen := ScreenInfo{
			Name:       screenName,
			Primary:    isPrimary,
			Left:       int(mi.rcMonitor.Left),
			Top:        int(mi.rcMonitor.Top),
			Width:      width,
			Height:     height,
			WorkLeft:   int(mi.rcWork.Left),
			WorkTop:    int(mi.rcWork.Top),
			WorkWidth:  int(mi.rcWork.Right - mi.rcWork.Left),
			WorkHeight: int(mi.rcWork.Bottom - mi.rcWork.Top),
		}

		screens = append(screens, screen)
		return 1 // 继续枚举
	}

	enumScreensMu.Lock()
	enumScreensTarget = callback
	enumScreensOnce.Do(func() {
		enumScreensCallback = syscall.NewCallback(func(hMonitor, hdcMonitor uintptr, lprcMonitor *RECT, dwData uintptr) uintptr {
			if enumScreensTarget != nil {
				return enumScreensTarget(hMonitor, hdcMonitor, lprcMonitor, dwData)
			}
			return 0
		})
	})
	ret, _, _ := procEnumDisplayMonitors.Call(0, 0, enumScreensCallback, 0)
	enumScreensMu.Unlock()

	if ret == 0 && len(screens) == 0 {
		// 枚举失败，使用备用方法获取主屏幕
		log.Println("枚举显示器失败，使用备用方法")
		screenWidth := int(getSystemMetrics(SM_CXSCREEN))
		screenHeight := int(getSystemMetrics(SM_CYSCREEN))

		screen := ScreenInfo{
			Name:       fmt.Sprintf("主屏幕 - %dx%d", screenWidth, screenHeight),
			Primary:    true,
			Left:       0,
			Top:        0,
			Width:      screenWidth,
			Height:     screenHeight,
			WorkLeft:   0,
			WorkTop:    0,
			WorkWidth:  screenWidth,
			WorkHeight: screenHeight,
		}
		screens = append(screens, screen)
	}

	return screens, nil
}

func (wa *WindowArranger) ArrangeWindowsOnScreen(hwndList []uintptr, screenIndex int, params WindowArrangeParams) error {
	screens, err := wa.GetScreensInfo()
	if err != nil {
		return fmt.Errorf("获取屏幕信息失败: %v", err)
	}

	if screenIndex < 0 || screenIndex >= len(screens) {
		return fmt.Errorf("无效的屏幕索引: %d", screenIndex)
	}

	screen := screens[screenIndex]

	// 调整参数为屏幕坐标
	adjustedParams := params
	adjustedParams.StartX += screen.WorkLeft
	adjustedParams.StartY += screen.WorkTop

	_, err = wa.CustomArrangeWindows(hwndList, adjustedParams)
	return err
}

func getSystemMetrics(nIndex int) int32 {
	ret, _, _ := procGetSystemMetrics.Call(uintptr(nIndex))
	return int32(ret)
}

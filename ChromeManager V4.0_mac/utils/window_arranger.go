package utils

import (
	"chromemanager/platform/common"
	"fmt"
	"log"
	"math"
	"time"
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

type ScreenInfo struct {
	Name       string  `json:"name"`
	Primary    bool    `json:"primary"`
	Left       int     `json:"left"`
	Top        int     `json:"top"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	WorkLeft   int     `json:"workLeft"`
	WorkTop    int     `json:"workTop"`
	WorkWidth  int     `json:"workWidth"`
	WorkHeight int     `json:"workHeight"`
	Scale      float64 `json:"scale"`
	DeviceName string  `json:"deviceName"`
}

type WindowArranger struct {
	provider common.PlatformProvider
	// 添加完成回调函数
	OnArrangeComplete func() error
}

func NewWindowArranger(provider common.PlatformProvider) *WindowArranger {
	return &WindowArranger{
		provider: provider,
	}
}

func (wa *WindowArranger) SetOnArrangeComplete(callback func() error) {
	wa.OnArrangeComplete = callback
}

func (wa *WindowArranger) ArrangeWindows(hwndList []uintptr) error {
	if len(hwndList) == 0 {
		return fmt.Errorf("窗口列表为空")
	}

	// 获取主屏幕信息
	primaryScreen, err := wa.provider.GetPrimaryScreen()
	if err != nil {
		return fmt.Errorf("无法获取主屏幕: %v", err)
	}

	// 计算最佳布局
	count := len(hwndList)
	cols := int(math.Sqrt(float64(count)))
	if cols*cols < count {
		cols++
	}

	// 计算窗口大小
	windowWidth := primaryScreen.Width / cols
	windowHeight := primaryScreen.Height / ((count + cols - 1) / cols)

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

	return wa.CustomArrangeWindows(hwndList, params)
}

func (wa *WindowArranger) CustomArrangeWindows(hwndList []uintptr, params WindowArrangeParams) error {
	if len(hwndList) == 0 {
		return fmt.Errorf("窗口列表为空")
	}

	// 1. 恢复所有窗口 (如果最小化) - 需要 Provider 支持 ShowWindow(SW_NORMAL)
	// 在 macOS 实现中，我们把 ShowWindow 映射为 BringWindowToTop
	for _, hwnd := range hwndList {
		handle := common.WindowHandle(hwnd)
		wa.provider.ShowWindow(handle, true)
	}

	// 2. 排列窗口
	for i, hwnd := range hwndList {
		handle := common.WindowHandle(hwnd)
		row := i / params.WindowsPerRow
		col := i % params.WindowsPerRow

		x := params.StartX + col*(params.Width+params.HorizontalSpacing)
		y := params.StartY + row*(params.Height+params.VerticalSpacing)

		rect := common.Rect{
			Left:   x,
			Top:    y, // 这里假设 StartY 是相对于屏幕顶部的 (macOS Provider 内部已处理或 AX 直接使用)
			Width:  params.Width,
			Height: params.Height,
		}

		if err := wa.provider.SetWindowPosition(handle, rect); err != nil {
			log.Printf("移动窗口 %d 到位置 (%d, %d) 失败: %v", hwnd, x, y, err)
			continue
		}
	}

	// 3. 将所有排列的窗口置顶并激活
	for i, hwnd := range hwndList {
		handle := common.WindowHandle(hwnd)
		if err := wa.provider.BringWindowToTop(handle); err != nil {
			log.Printf("将窗口 %d 置顶失败: %v", hwnd, err)
		}
		// 稍微延迟一下，确保窗口切换顺畅
		if i < len(hwndList)-1 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	// 4. 调用完成回调函数
	if wa.OnArrangeComplete != nil {
		if err := wa.OnArrangeComplete(); err != nil {
			log.Printf("窗口排列完成回调函数执行失败: %v", err)
		}
	}

	return nil
}

func (wa *WindowArranger) GetScreensInfo() ([]ScreenInfo, error) {
	commonScreens, err := wa.provider.GetScreensInfo()
	if err != nil {
		return nil, err
	}

	var screens []ScreenInfo
	for _, s := range commonScreens {
		screens = append(screens, ScreenInfo{
			Name:       s.Name,
			Primary:    s.Primary,
			Left:       s.Bounds.Left,
			Top:        s.Bounds.Top,
			Width:      s.Bounds.Width,
			Height:     s.Bounds.Height,
			WorkLeft:   s.WorkArea.Left,
			WorkTop:    s.WorkArea.Top,
			WorkWidth:  s.WorkArea.Width,
			WorkHeight: s.WorkArea.Height,
			Scale:      s.Scale,
			DeviceName: s.Name, // Simply use Name or add Logic
		})
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

	return wa.CustomArrangeWindows(hwndList, adjustedParams)
}

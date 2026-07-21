package algorithms

import (
	"chromemanager/platform/common"
	"math"
)

// CalculateGridLayout 计算网格布局的窗口位置。
// 这是一个纯算法函数,不依赖任何操作系统API,可在所有平台复用。
//
// 参数:
//   windowCount: 窗口数量
//   params: 布局参数
//   workArea: 可用的工作区域
//
// 返回:
//   每个窗口的位置和大小
func CalculateGridLayout(
	windowCount int,
	params common.WindowArrangeParams,
	workArea common.Rect,
) []common.Rect {
	if windowCount <= 0 {
		return nil
	}

	layouts := make([]common.Rect, 0, windowCount)

	// 如果未指定窗口大小,使用默认值
	windowWidth := params.Width
	if windowWidth <= 0 {
		windowWidth = common.DefaultWindowWidth
	}

	windowHeight := params.Height
	if windowHeight <= 0 {
		windowHeight = common.DefaultWindowHeight
	}

	// 计算每行窗口数
	windowsPerRow := params.WindowsPerRow
	if windowsPerRow <= 0 {
		windowsPerRow = common.DefaultWindowsPerRow
	}

	// 计算行数和列数
	cols := windowsPerRow
	rows := (windowCount + cols - 1) / cols // 向上取整

	// 计算起始位置
	startX := params.StartX
	startY := params.StartY

	// 如果起始位置为0,尝试居中
	if startX == 0 && startY == 0 {
		totalWidth := cols*windowWidth + (cols-1)*params.HorizontalSpacing
		totalHeight := rows*windowHeight + (rows-1)*params.VerticalSpacing

		startX = workArea.Left + (workArea.Width-totalWidth)/2
		startY = workArea.Top + (workArea.Height-totalHeight)/2

		// 确保不超出边界
		if startX < workArea.Left {
			startX = workArea.Left
		}
		if startY < workArea.Top {
			startY = workArea.Top
		}
	}

	// 计算每个窗口的位置
	for i := 0; i < windowCount; i++ {
		row := i / cols
		col := i % cols

		x := startX + col*(windowWidth+params.HorizontalSpacing)
		y := startY + row*(windowHeight+params.VerticalSpacing)

		layouts = append(layouts, common.Rect{
			Left:   x,
			Top:    y,
			Width:  windowWidth,
			Height: windowHeight,
		})
	}

	return layouts
}

// CalculateCustomLayout 根据自定义参数计算窗口布局。
// 这个函数提供更灵活的布局计算,支持不同的排列模式。
//
// 参数:
//   windowCount: 窗口数量
//   params: 布局参数
//   workArea: 可用的工作区域
//
// 返回:
//   每个窗口的位置和大小
func CalculateCustomLayout(
	windowCount int,
	params common.WindowArrangeParams,
	workArea common.Rect,
) []common.Rect {
	// 目前使用网格布局,后续可以扩展更多布局模式
	return CalculateGridLayout(windowCount, params, workArea)
}

// OptimizeLayout 优化窗口布局,尝试最大化利用屏幕空间。
// 这是一个高级算法,会根据窗口数量和屏幕大小自动调整参数。
//
// 参数:
//   windowCount: 窗口数量
//   workArea: 可用的工作区域
//
// 返回:
//   优化后的布局参数和窗口位置
func OptimizeLayout(
	windowCount int,
	workArea common.Rect,
) (common.WindowArrangeParams, []common.Rect) {
	// 计算最优的每行窗口数
	// 尝试让布局接近正方形
	idealCols := int(math.Sqrt(float64(windowCount)))
	if idealCols < 1 {
		idealCols = 1
	}

	// 计算行数
	rows := (windowCount + idealCols - 1) / idealCols

	// 计算窗口大小以充分利用空间
	// 留出一些边距
	margin := 20
	spacing := 10

	availableWidth := workArea.Width - 2*margin
	availableHeight := workArea.Height - 2*margin

	windowWidth := (availableWidth - (idealCols-1)*spacing) / idealCols
	windowHeight := (availableHeight - (rows-1)*spacing) / rows

	// 确保窗口大小不会太小
	if windowWidth < common.MinWindowWidth {
		windowWidth = common.MinWindowWidth
	}
	if windowHeight < common.MinWindowHeight {
		windowHeight = common.MinWindowHeight
	}

	// 创建优化后的参数
	params := common.WindowArrangeParams{
		StartX:            workArea.Left + margin,
		StartY:            workArea.Top + margin,
		Width:             windowWidth,
		Height:            windowHeight,
		HorizontalSpacing: spacing,
		VerticalSpacing:   spacing,
		WindowsPerRow:     idealCols,
		ScreenIndex:       -1,
	}

	// 计算布局
	layouts := CalculateGridLayout(windowCount, params, workArea)

	return params, layouts
}

// CalculateTiledLayout 计算平铺布局(无间距,窗口紧密排列)。
// 适合需要最大化屏幕利用率的场景。
func CalculateTiledLayout(
	windowCount int,
	workArea common.Rect,
) []common.Rect {
	params := common.WindowArrangeParams{
		StartX:            workArea.Left,
		StartY:            workArea.Top,
		HorizontalSpacing: 0,
		VerticalSpacing:   0,
		WindowsPerRow:     int(math.Sqrt(float64(windowCount))),
	}

	// 计算窗口大小
	cols := params.WindowsPerRow
	if cols < 1 {
		cols = 1
	}
	rows := (windowCount + cols - 1) / cols

	params.Width = workArea.Width / cols
	params.Height = workArea.Height / rows

	return CalculateGridLayout(windowCount, params, workArea)
}

// CalculateCascadeLayout 计算层叠布局(窗口依次偏移)。
// 适合需要查看多个窗口标题栏的场景。
func CalculateCascadeLayout(
	windowCount int,
	workArea common.Rect,
	baseWidth, baseHeight int,
	cascadeOffset int,
) []common.Rect {
	if baseWidth <= 0 {
		baseWidth = common.DefaultWindowWidth
	}
	if baseHeight <= 0 {
		baseHeight = common.DefaultWindowHeight
	}
	if cascadeOffset <= 0 {
		cascadeOffset = 30 // 默认偏移30像素
	}

	layouts := make([]common.Rect, 0, windowCount)

	startX := workArea.Left + 50
	startY := workArea.Top + 50

	for i := 0; i < windowCount; i++ {
		x := startX + i*cascadeOffset
		y := startY + i*cascadeOffset

		// 如果超出工作区,重新开始
		if x+baseWidth > workArea.Left+workArea.Width ||
			y+baseHeight > workArea.Top+workArea.Height {
			x = startX
			y = startY
		}

		layouts = append(layouts, common.Rect{
			Left:   x,
			Top:    y,
			Width:  baseWidth,
			Height: baseHeight,
		})
	}

	return layouts
}

// FitsInWorkArea 检查布局是否完全在工作区内。
func FitsInWorkArea(layout common.Rect, workArea common.Rect) bool {
	return layout.Left >= workArea.Left &&
		layout.Top >= workArea.Top &&
		layout.Left+layout.Width <= workArea.Left+workArea.Width &&
		layout.Top+layout.Height <= workArea.Top+workArea.Height
}

// ClampToWorkArea 将布局限制在工作区内。
func ClampToWorkArea(layout common.Rect, workArea common.Rect) common.Rect {
	// 确保左上角在工作区内
	if layout.Left < workArea.Left {
		layout.Left = workArea.Left
	}
	if layout.Top < workArea.Top {
		layout.Top = workArea.Top
	}

	// 确保右下角在工作区内
	if layout.Left+layout.Width > workArea.Left+workArea.Width {
		layout.Left = workArea.Left + workArea.Width - layout.Width
	}
	if layout.Top+layout.Height > workArea.Top+workArea.Height {
		layout.Top = workArea.Top + workArea.Height - layout.Height
	}

	// 如果窗口太大,缩小它
	if layout.Width > workArea.Width {
		layout.Width = workArea.Width
	}
	if layout.Height > workArea.Height {
		layout.Height = workArea.Height
	}

	return layout
}

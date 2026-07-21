package common

import (
	"errors"
	"time"
)

// Common errors
var (
	ErrBrowserNotFound = errors.New("browser not found")
)

// IconType 图标类型
type IconType int

const (
	IconTypeSmall IconType = 0
	IconTypeBig   IconType = 1
)

// WindowHandle 是跨平台的窗口句柄类型。
// 在Windows上对应HWND,在macOS上对应AXUIElementRef的指针值。
type WindowHandle uintptr

// WindowInfo 包含窗口的基本信息。
type WindowInfo struct {
	// Handle 窗口句柄
	Handle WindowHandle

	// Title 窗口标题
	Title string

	// ClassName 窗口类名(Windows)或Role(macOS)
	ClassName string

	// ProcessID 窗口所属进程ID
	ProcessID int32

	// Position 窗口位置和大小
	Position Rect

	// IsVisible 窗口是否可见
	IsVisible bool

	// Number 窗口编号(应用内部使用)
	Number int

	// HWND 原始窗口句柄(兼容旧代码,逐步废弃)
	// Deprecated: 使用 Handle 替代
	HWND uintptr

	// Chrome-specific fields (optional, only for Chrome windows)
	// DebugPort Chrome调试端口
	DebugPort int
	// UserDataDir Chrome用户数据目录
	UserDataDir string
	// CommandLine 进程命令行
	CommandLine string
}

// Rect 表示矩形区域(位置和大小)。
type Rect struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Point 表示二维坐标点。
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Size 表示尺寸。
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ScreenInfo 包含显示器信息。
type ScreenInfo struct {
	// Name 显示器名称
	Name string `json:"name"`

	// Primary 是否为主显示器
	Primary bool `json:"primary"`

	// Bounds 显示器完整区域
	Bounds Rect

	// WorkArea 工作区域(去除任务栏等)
	WorkArea Rect

	// DeviceName 设备名称(平台特定)
	DeviceName string `json:"deviceName"`

	// 兼容旧代码的字段
	Left       int `json:"left"`
	Top        int `json:"top"`
	Width      int `json:"width"`
	Height     int `json:"height"`
	WorkLeft   int `json:"workLeft"`
	WorkTop    int `json:"workTop"`
	WorkWidth  int `json:"workWidth"`
	WorkHeight int `json:"workHeight"`

	// Scale 屏幕缩放比例
	Scale float64 `json:"scale"`
}

// KeyModifier 表示键盘修饰键。
type KeyModifier uint32

const (
	// ModControl Ctrl键
	ModControl KeyModifier = 1 << iota
	// ModShift Shift键
	ModShift
	// ModAlt Alt键(Windows)/Option键(macOS)
	ModAlt
	// ModWin Win键(Windows)/Command键(macOS)
	ModWin
)

// KeyCode 表示键码(统一编码,需要在各平台转换)。
type KeyCode uint16

const (
	KeyEnter KeyCode = 0x0D
	KeySpace KeyCode = 0x20
	KeyEsc   KeyCode = 0x1B
	KeyTab   KeyCode = 0x09
	KeyBack  KeyCode = 0x08
	KeyA     KeyCode = 0x41
	KeyC     KeyCode = 0x43
	KeyV     KeyCode = 0x56
)

// HotkeySpec 表示快捷键规格。
type HotkeySpec struct {
	// Modifiers 修饰键组合
	Modifiers KeyModifier

	// Key 按键码
	Key KeyCode

	// RawString 原始字符串(如 "Ctrl+Alt+S")
	RawString string
}

// MouseButton 表示鼠标按键。
type MouseButton int

const (
	// MouseLeft 鼠标左键
	MouseLeft MouseButton = iota
	// MouseRight 鼠标右键
	MouseRight
	// MouseMiddle 鼠标中键
	MouseMiddle
)

// SyncConfig 同步配置。
type SyncConfig struct {
	// MouseMoveThreshold 鼠标移动阈值(像素)
	MouseMoveThreshold int `json:"mouseMoveThreshold"`

	// MouseMoveInterval 鼠标移动事件间隔
	MouseMoveInterval time.Duration `json:"mouseMoveInterval"`

	// KeyboardInterval 键盘事件间隔
	KeyboardInterval time.Duration `json:"keyboardInterval"`

	// WheelEventThreshold 滚轮事件阈值
	WheelEventThreshold time.Duration `json:"wheelEventThreshold"`

	// EnablePopupSync 启用弹出窗口同步
	EnablePopupSync bool `json:"enablePopupSync"`

	// EnableZoomSync 启用缩放同步
	EnableZoomSync bool `json:"enableZoomSync"`

	// EnableScrollSync 启用滚动同步
	EnableScrollSync bool `json:"enableScrollSync"`

	// EnableKeyboardSync 启用键盘同步
	EnableKeyboardSync bool `json:"enableKeyboardSync"`

	// HighPrecisionMode 高精度模式
	HighPrecisionMode bool `json:"highPrecisionMode"`

	// MaxConcurrentEvents 最大并发事件数
	MaxConcurrentEvents int `json:"maxConcurrentEvents"`

	// EventBufferSize 事件缓冲区大小
	EventBufferSize int `json:"eventBufferSize"`
}

// SyncStatus 同步状态。
type SyncStatus int

const (
	// SyncStatusIdle 空闲状态
	SyncStatusIdle SyncStatus = iota
	// SyncStatusRunning 运行中
	SyncStatusRunning
	// SyncStatusPaused 暂停
	SyncStatusPaused
	// SyncStatusError 错误状态
	SyncStatusError
)

// SyncState 同步状态信息。
type SyncState struct {
	// Status 当前状态
	Status SyncStatus

	// MasterWindow 主窗口信息
	MasterWindow *WindowInfo

	// SlaveWindows 从窗口信息列表
	SlaveWindows []WindowInfo

	// TotalAgents 代理窗口总数
	TotalAgents int

	// ActiveAgents 活动代理窗口数
	ActiveAgents int

	// Config 同步配置
	Config SyncConfig
}

// NotificationOptions 通知选项。
type NotificationOptions struct {
	// Title 通知标题
	Title string

	// Message 通知内容
	Message string

	// IconPath 图标路径
	IconPath string

	// Duration 显示时长(0表示使用系统默认)
	Duration time.Duration

	// Silent 是否静音
	Silent bool
}

// ShortcutInfo 快捷方式信息。
type ShortcutInfo struct {
	// Number 快捷方式编号
	Number int
	// FilePath 快捷方式文件路径
	FilePath string
	// TargetPath 目标程序路径
	TargetPath string
	// Arguments 启动参数
	Arguments string
	// WorkingDir 工作目录
	WorkingDir string
}

// InputConfig 输入配置。
type InputConfig struct {
	// MinValue 最小值
	MinValue float64 `json:"minValue"`

	// MaxValue 最大值
	MaxValue float64 `json:"maxValue"`

	// IsFloat 是否为浮点数
	IsFloat bool `json:"isFloat"`

	// DecimalPlaces 小数位数
	DecimalPlaces int `json:"decimalPlaces"`

	// Overwrite 是否覆盖现有内容
	Overwrite bool `json:"overwrite"`

	// Delayed 是否延迟输入
	Delayed bool `json:"delayed"`
}

// WindowArrangeParams 窗口排列参数。
type WindowArrangeParams struct {
	// StartX 起始X坐标
	StartX int `json:"startX"`

	// StartY 起始Y坐标
	StartY int `json:"startY"`

	// Width 窗口宽度
	Width int `json:"width"`

	// Height 窗口高度
	Height int `json:"height"`

	// HorizontalSpacing 水平间距
	HorizontalSpacing int `json:"horizontalSpacing"`

	// VerticalSpacing 垂直间距
	VerticalSpacing int `json:"verticalSpacing"`

	// WindowsPerRow 每行窗口数
	WindowsPerRow int `json:"windowsPerRow"`

	// ScreenIndex 目标屏幕索引(-1表示当前屏幕)
	ScreenIndex int `json:"screenIndex"`
}

// 辅助函数

// IsValid 检查窗口句柄是否有效。
func (h WindowHandle) IsValid() bool {
	return h != 0
}

// Contains 检查矩形是否包含指定点。
func (r Rect) Contains(p Point) bool {
	return p.X >= r.Left && p.X < r.Left+r.Width &&
		p.Y >= r.Top && p.Y < r.Top+r.Height
}

// Center 返回矩形中心点。
func (r Rect) Center() Point {
	return Point{
		X: r.Left + r.Width/2,
		Y: r.Top + r.Height/2,
	}
}

// HasModifier 检查是否包含指定修饰键。
func (m KeyModifier) HasModifier(mod KeyModifier) bool {
	return m&mod != 0
}

// String 返回同步状态的字符串表示。
func (s SyncStatus) String() string {
	switch s {
	case SyncStatusIdle:
		return "idle"
	case SyncStatusRunning:
		return "running"
	case SyncStatusPaused:
		return "paused"
	case SyncStatusError:
		return "error"
	default:
		return "unknown"
	}
}

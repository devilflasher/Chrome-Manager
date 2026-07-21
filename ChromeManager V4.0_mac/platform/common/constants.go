package common

import "time"

// 窗口管理常量
const (
	// DefaultWindowWidth 默认窗口宽度
	DefaultWindowWidth = 720

	// DefaultWindowHeight 默认窗口高度
	DefaultWindowHeight = 460

	// MinWindowWidth 最小窗口宽度
	MinWindowWidth = 400

	// MinWindowHeight 最小窗口高度
	MinWindowHeight = 300

	// DefaultWindowSpacing 默认窗口间距
	DefaultWindowSpacing = 0

	// DefaultWindowsPerRow 默认每行窗口数
	DefaultWindowsPerRow = 5
)

// 同步管理常量
const (
	// DefaultMouseMoveThreshold 默认鼠标移动阈值(像素)
	DefaultMouseMoveThreshold = 2

	// DefaultMouseMoveInterval 默认鼠标移动间隔
	DefaultMouseMoveInterval = 5 * time.Millisecond

	// DefaultKeyboardInterval 默认键盘事件间隔
	DefaultKeyboardInterval = 3 * time.Millisecond

	// DefaultWheelEventThreshold 默认滚轮事件阈值
	DefaultWheelEventThreshold = 5 * time.Millisecond

	// DefaultMaxConcurrentEvents 默认最大并发事件数
	DefaultMaxConcurrentEvents = 500

	// DefaultEventBufferSize 默认事件缓冲区大小
	DefaultEventBufferSize = 5000

	// DefaultMaxRetryAttempts 默认最大重试次数
	DefaultMaxRetryAttempts = 2

	// DefaultRetryDelay 默认重试延迟
	DefaultRetryDelay = 20 * time.Millisecond

	// DefaultErrorThreshold 默认错误阈值
	DefaultErrorThreshold = 20
)

// 通知常量
const (
	// DefaultNotificationDuration 默认通知显示时长
	DefaultNotificationDuration = 3 * time.Second

	// MaxNotificationTitleLength 最大通知标题长度
	MaxNotificationTitleLength = 64

	// MaxNotificationMessageLength 最大通知消息长度
	MaxNotificationMessageLength = 256
)

// 输入模拟常量
const (
	// InputDelayMin 最小输入延迟
	InputDelayMin = 10 * time.Millisecond

	// InputDelayMax 最大输入延迟
	InputDelayMax = 100 * time.Millisecond

	// DefaultInputDelay 默认输入延迟
	DefaultInputDelay = 50 * time.Millisecond
)

// 屏幕刷新常量
const (
	// ScreenInfoCacheTimeout 屏幕信息缓存超时
	ScreenInfoCacheTimeout = 5 * time.Second

	// WindowInfoCacheTimeout 窗口信息缓存超时
	WindowInfoCacheTimeout = 1 * time.Second
)

// 应用程序常量
const (
	// AppName 应用程序名称
	AppName = "ChromeManager"

	// AppVersion 应用程序版本
	AppVersion = "4.0.0"

	// ChromeClassName Chrome窗口类名(Windows)
	ChromeClassName = "Chrome_WidgetWin_1"

	// ChromeProcessName Chrome进程名称
	ChromeProcessName = "chrome.exe" // Windows
	// ChromeProcessNameMac = "Google Chrome" // macOS

	// ChromeUserDataDirPrefix Chrome用户数据目录前缀
	ChromeUserDataDirPrefix = ""

	// ChromeDefaultProfile Chrome默认配置名称
	ChromeDefaultProfile = "Default"
)

// 通用键码定义(统一编码)
// 注意: 这些是内部统一编码,需要在各平台转换为平台特定键码
const (
	KeyCodeUnknown KeyCode = 0

	// 字母键 A-Z (65-90)
	KeyCodeA KeyCode = 65
	KeyCodeB KeyCode = 66
	KeyCodeC KeyCode = 67
	KeyCodeD KeyCode = 68
	KeyCodeE KeyCode = 69
	KeyCodeF KeyCode = 70
	KeyCodeG KeyCode = 71
	KeyCodeH KeyCode = 72
	KeyCodeI KeyCode = 73
	KeyCodeJ KeyCode = 74
	KeyCodeK KeyCode = 75
	KeyCodeL KeyCode = 76
	KeyCodeM KeyCode = 77
	KeyCodeN KeyCode = 78
	KeyCodeO KeyCode = 79
	KeyCodeP KeyCode = 80
	KeyCodeQ KeyCode = 81
	KeyCodeR KeyCode = 82
	KeyCodeS KeyCode = 83
	KeyCodeT KeyCode = 84
	KeyCodeU KeyCode = 85
	KeyCodeV KeyCode = 86
	KeyCodeW KeyCode = 87
	KeyCodeX KeyCode = 88
	KeyCodeY KeyCode = 89
	KeyCodeZ KeyCode = 90

	// 数字键 0-9 (48-57)
	KeyCode0 KeyCode = 48
	KeyCode1 KeyCode = 49
	KeyCode2 KeyCode = 50
	KeyCode3 KeyCode = 51
	KeyCode4 KeyCode = 52
	KeyCode5 KeyCode = 53
	KeyCode6 KeyCode = 54
	KeyCode7 KeyCode = 55
	KeyCode8 KeyCode = 56
	KeyCode9 KeyCode = 57

	// 功能键
	KeyCodeF1  KeyCode = 112
	KeyCodeF2  KeyCode = 113
	KeyCodeF3  KeyCode = 114
	KeyCodeF4  KeyCode = 115
	KeyCodeF5  KeyCode = 116
	KeyCodeF6  KeyCode = 117
	KeyCodeF7  KeyCode = 118
	KeyCodeF8  KeyCode = 119
	KeyCodeF9  KeyCode = 120
	KeyCodeF10 KeyCode = 121
	KeyCodeF11 KeyCode = 122
	KeyCodeF12 KeyCode = 123

	// 控制键
	KeyCodeEscape    KeyCode = 27
	KeyCodeTab       KeyCode = 9
	KeyCodeCapsLock  KeyCode = 20
	KeyCodeShift     KeyCode = 16
	KeyCodeControl   KeyCode = 17
	KeyCodeAlt       KeyCode = 18
	KeyCodeSpace     KeyCode = 32
	KeyCodeEnter     KeyCode = 13
	KeyCodeBackspace KeyCode = 8
	KeyCodeDelete    KeyCode = 46
	KeyCodeInsert    KeyCode = 45
	KeyCodeHome      KeyCode = 36
	KeyCodeEnd       KeyCode = 35
	KeyCodePageUp    KeyCode = 33
	KeyCodePageDown  KeyCode = 34

	// 方向键
	KeyCodeLeft  KeyCode = 37
	KeyCodeUp    KeyCode = 38
	KeyCodeRight KeyCode = 39
	KeyCodeDown  KeyCode = 40

	// 特殊字符
	KeyCodeMinus      KeyCode = 189 // -
	KeyCodeEqual      KeyCode = 187 // =
	KeyCodeBracketL   KeyCode = 219 // [
	KeyCodeBracketR   KeyCode = 221 // ]
	KeyCodeBackslash  KeyCode = 220 // \
	KeyCodeSemicolon  KeyCode = 186 // ;
	KeyCodeQuote      KeyCode = 222 // '
	KeyCodeComma      KeyCode = 188 // ,
	KeyCodePeriod     KeyCode = 190 // .
	KeyCodeSlash      KeyCode = 191 // /
	KeyCodeBackquote  KeyCode = 192 // `
)

// 默认配置
var (
	// DefaultSyncConfig 默认同步配置
	DefaultSyncConfig = SyncConfig{
		MouseMoveThreshold:  DefaultMouseMoveThreshold,
		MouseMoveInterval:   DefaultMouseMoveInterval,
		KeyboardInterval:    DefaultKeyboardInterval,
		WheelEventThreshold: DefaultWheelEventThreshold,
		EnablePopupSync:     true,
		EnableZoomSync:      true,
		EnableScrollSync:    true,
		EnableKeyboardSync:  true,
		HighPrecisionMode:   true,
		MaxConcurrentEvents: DefaultMaxConcurrentEvents,
		EventBufferSize:     DefaultEventBufferSize,
	}

	// DefaultWindowArrangeParams 默认窗口排列参数
	DefaultWindowArrangeParams = WindowArrangeParams{
		StartX:            0,
		StartY:            0,
		Width:             DefaultWindowWidth,
		Height:            DefaultWindowHeight,
		HorizontalSpacing: DefaultWindowSpacing,
		VerticalSpacing:   DefaultWindowSpacing,
		WindowsPerRow:     DefaultWindowsPerRow,
		ScreenIndex:       -1, // 当前屏幕
	}
)

package common

import "errors"

// 窗口管理相关错误
var (
	// ErrWindowNotFound 窗口未找到
	ErrWindowNotFound = errors.New("window not found")

	// ErrInvalidHandle 无效的窗口句柄
	ErrInvalidHandle = errors.New("invalid window handle")

	// ErrWindowClosed 窗口已关闭
	ErrWindowClosed = errors.New("window closed")

	// ErrInvalidParameter 无效的参数
	ErrInvalidParameter = errors.New("invalid parameter")

	// ErrOperationFailed 操作失败
	ErrOperationFailed = errors.New("operation failed")
)

// 同步管理相关错误
var (
	// ErrSyncAlreadyRunning 同步已在运行
	ErrSyncAlreadyRunning = errors.New("sync already running")

	// ErrSyncNotRunning 同步未运行
	ErrSyncNotRunning = errors.New("sync not running")

	// ErrSyncFailed 同步失败
	ErrSyncFailed = errors.New("sync failed")

	// ErrNoMasterWindow 没有主窗口
	ErrNoMasterWindow = errors.New("no master window specified")

	// ErrNoSlaveWindows 没有从窗口
	ErrNoSlaveWindows = errors.New("no slave windows specified")

	// ErrHookInstallFailed 钩子安装失败
	ErrHookInstallFailed = errors.New("hook install failed")
)

// 快捷键相关错误
var (
	// ErrHotkeyAlreadyRegistered 快捷键已被占用
	ErrHotkeyAlreadyRegistered = errors.New("hotkey already registered")

	// ErrHotkeyNotRegistered 快捷键未注册
	ErrHotkeyNotRegistered = errors.New("hotkey not registered")

	// ErrInvalidHotkey 无效的快捷键
	ErrInvalidHotkey = errors.New("invalid hotkey")

	// ErrHotkeyEmpty 快捷键为空
	ErrHotkeyEmpty = errors.New("hotkey is empty")

	// ErrHotkeyParseError 快捷键解析错误
	ErrHotkeyParseError = errors.New("hotkey parse error")
)

// 输入模拟相关错误
var (
	// ErrInputFailed 输入失败
	ErrInputFailed = errors.New("input failed")

	// ErrInvalidKeyCode 无效的键码
	ErrInvalidKeyCode = errors.New("invalid key code")

	// ErrInvalidMouseButton 无效的鼠标按键
	ErrInvalidMouseButton = errors.New("invalid mouse button")

	// ErrClipboardError 剪贴板错误
	ErrClipboardError = errors.New("clipboard error")
)

// 通知相关错误
var (
	// ErrNotificationFailed 通知失败
	ErrNotificationFailed = errors.New("notification failed")

	// ErrNotificationNotSupported 通知不支持
	ErrNotificationNotSupported = errors.New("notification not supported on this platform")
)

// 权限相关错误
var (
	// ErrPermissionDenied 权限被拒绝
	ErrPermissionDenied = errors.New("permission denied")

	// ErrAccessibilityPermissionRequired macOS需要辅助功能权限
	ErrAccessibilityPermissionRequired = errors.New("accessibility permission required")

	// ErrAdminPermissionRequired Windows需要管理员权限
	ErrAdminPermissionRequired = errors.New("administrator permission required")
)

// 平台相关错误
var (
	// ErrPlatformNotSupported 平台不支持
	ErrPlatformNotSupported = errors.New("platform not supported")

	// ErrFeatureNotSupported 功能不支持
	ErrFeatureNotSupported = errors.New("feature not supported on this platform")
)

// 内部错误
var (
	// ErrInternal 内部错误
	ErrInternal = errors.New("internal error")

	// ErrNotImplemented 功能未实现
	ErrNotImplemented = errors.New("not implemented")
)

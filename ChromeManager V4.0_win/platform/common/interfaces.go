package common

import "time"

// PlatformProvider 是所有平台功能的统一入口。
// 它组合了所有子Provider,提供完整的平台功能。
type PlatformProvider interface {
	// WindowProvider 窗口管理功能
	WindowProvider

	// SyncProvider 同步功能
	SyncProvider

	// HotkeyProvider 快捷键功能
	HotkeyProvider

	// InputProvider 输入模拟功能
	InputProvider

	// NotificationProvider 通知功能
	NotificationProvider

	// AdminProvider 管理员权限功能
	AdminProvider

	// DialogProvider 对话框功能
	DialogProvider

	// Shutdown 关闭Provider,释放资源
	Shutdown() error
}

// WindowProvider 提供跨平台的窗口管理接口。
type WindowProvider interface {
	// FindWindow 根据类名和窗口名查找窗口。
	// 参数可以为空字符串,表示不过滤该条件。
	FindWindow(className, windowName string) (WindowHandle, error)

	// EnumWindows 枚举所有窗口。
	// callback返回false时停止枚举。
	EnumWindows(callback func(WindowHandle) bool) error

	// GetWindowInfo 获取窗口详细信息。
	GetWindowInfo(handle WindowHandle) (*WindowInfo, error)

	// GetWindowTitle 获取窗口标题。
	GetWindowTitle(handle WindowHandle) (string, error)

	// SetWindowTitle 设置窗口标题。
	SetWindowTitle(handle WindowHandle, title string) error

	// GetWindowRect 获取窗口位置和大小。
	GetWindowRect(handle WindowHandle) (*Rect, error)

	// SetWindowPosition 设置窗口位置和大小。
	SetWindowPosition(handle WindowHandle, rect Rect) error

	// ShowWindow 显示或隐藏窗口。
	ShowWindow(handle WindowHandle, show bool) error

	// SetForegroundWindow 激活窗口(使其成为前台窗口)。
	SetForegroundWindow(handle WindowHandle) error

	// GetForegroundWindow 获取当前前台窗口。
	GetForegroundWindow() (WindowHandle, error)

	// IsWindowVisible 检查窗口是否可见。
	IsWindowVisible(handle WindowHandle) (bool, error)

	// IsWindowValid 检查窗口句柄是否有效。
	IsWindowValid(handle WindowHandle) (bool, error)

	// GetWindowProcessID 获取窗口所属进程ID。
	GetWindowProcessID(handle WindowHandle) (int32, error)

	// GetWindowClassName 获取窗口类名(Windows)或Role(macOS)。
	GetWindowClassName(handle WindowHandle) (string, error)

	// GetScreensInfo 获取所有显示器信息。
	GetScreensInfo() ([]ScreenInfo, error)

	// GetPrimaryScreen 获取主显示器信息。
	GetPrimaryScreen() (*ScreenInfo, error)

	// PostMessage 向窗口发送异步消息(平台特定)。
	PostMessage(handle WindowHandle, msg uint32, wParam, lParam uintptr) error

	// EnumChromeWindows 枚举所有Chrome窗口(应用特定)。
	EnumChromeWindows() ([]WindowInfo, error)

	// IsValidChromeWindow 检查是否为有效的Chrome窗口(应用特定)。
	IsValidChromeWindow(handle WindowHandle) (bool, error)

	// BringWindowToTop 将窗口置顶并激活。
	BringWindowToTop(handle WindowHandle) error

	// CloseWindowGracefully 优雅地关闭窗口(等待关闭完成)。
	// timeout: 超时时间(单位:100ms的倍数)
	CloseWindowGracefully(handle WindowHandle, timeout int) error

	// IsProcessRunning 检查进程是否正在运行。
	IsProcessRunning(pid int32) bool

	// FindWindowByPID 根据进程ID查找窗口句柄。
	FindWindowByPID(pid int32) (WindowHandle, error)

	// FindBrowserPath 查找浏览器的安装路径。
	FindBrowserPath(browserType string) (string, error)

	// SHChangeNotify 通知Shell图标或文件关联已更改(Windows特定)。
	SHChangeNotify() error

	// IsChromeDebugPortReady 检查Chrome调试端口是否就绪。
	IsChromeDebugPortReady(debugPort int) bool

	// WaitForChromeWindowReady 等待Chrome窗口就绪。
	// maxWaitTime: 最大等待时间
	WaitForChromeWindowReady(handle WindowHandle, pid int32, debugPort int, maxWaitTime time.Duration) error

	// GetChromePopups 获取Chrome窗口的弹出窗口列表。
	GetChromePopups(chromeHandle WindowHandle) ([]WindowHandle, error)

	// BuildChromeCommand 构建Chrome启动命令。
	BuildChromeCommand(targetPath, arguments string, number int, userDataDir string, debugPort int) []string

	// TitleSimilarity 计算两个标题的相似度(0.0-1.0)。
	TitleSimilarity(title1, title2 string) float64

	// ParseShortcutsFromDirectory 从目录中解析快捷方式。
	ParseShortcutsFromDirectory(shortcutDir string) ([]ShortcutInfo, error)

	// ParseWindowNumbers 解析窗口编号字符串(如"1,2,3-5")。
	ParseWindowNumbers(numbersStr string) ([]int, error)
}

// SyncProvider 提供跨平台的同步功能接口。
type SyncProvider interface {
	// Start 启动同步。
	// masterWindow: 主窗口句柄
	// slaveWindows: 从窗口句柄列表
	Start(masterWindow WindowHandle, slaveWindows []WindowHandle) error

	// Stop 停止同步。
	Stop() error

	// IsRunning 检查同步是否正在运行。
	IsRunning() bool

	// GetState 获取同步状态信息。
	GetState() SyncState

	// SetConfig 设置同步配置。
	SetConfig(config SyncConfig) error

	// GetConfig 获取当前同步配置。
	GetConfig() SyncConfig

	// PauseSync 暂停同步(保持钩子,但不转发事件)。
	PauseSync() error

	// ResumeSync 恢复同步。
	ResumeSync() error
}

// HotkeyProvider 提供跨平台的全局快捷键接口。
type HotkeyProvider interface {
	// Register 注册全局快捷键。
	// id: 快捷键ID(用于标识)
	// spec: 快捷键规格
	// callback: 触发时的回调函数
	Register(id int, spec HotkeySpec, callback func()) error

	// Unregister 注销快捷键。
	Unregister(id int) error

	// IsRegistered 检查快捷键是否已注册。
	IsRegistered(id int) bool

	// Shutdown 关闭快捷键管理器,注销所有快捷键。
	Shutdown() error

	// ParseHotkeySpec 解析快捷键字符串为HotkeySpec。
	// 例如: "Ctrl+Alt+S" -> HotkeySpec{Modifiers: ModControl|ModAlt, Key: 'S'}
	ParseHotkeySpec(hotkeyStr string) (HotkeySpec, error)
}

// InputProvider 提供跨平台的输入模拟接口。
type InputProvider interface {
	// SendKeyInput 模拟按键输入。
	// keyCode: 键码
	// keyDown: true表示按下,false表示释放
	SendKeyInput(keyCode KeyCode, keyDown bool) error

	// SendKeyPress 模拟按键按下和释放(完整的按键操作)。
	SendKeyPress(keyCode KeyCode) error

	// SendTextInput 模拟文本输入(输入字符串)。
	SendTextInput(text string) error

	// SendMouseClick 模拟鼠标点击。
	// x, y: 屏幕坐标
	// button: 鼠标按键
	SendMouseClick(x, y int, button MouseButton) error

	// MoveMouse 移动鼠标到指定位置。
	MoveMouse(x, y int) error

	// SendMouseWheel 模拟鼠标滚轮。
	// delta: 滚动量(正数向上,负数向下)
	SendMouseWheel(delta int) error

	// SetClipboard 设置剪贴板内容。
	SetClipboard(text string) error

	// GetClipboard 获取剪贴板内容。
	GetClipboard() (string, error)

	// InputRandomNumber 在指定窗口输入随机数(应用特定)。
	InputRandomNumber(handle WindowHandle, config InputConfig) error
}

// NotificationProvider 提供跨平台的系统通知接口。
type NotificationProvider interface {
	// ShowNotification 显示系统通知。
	ShowNotification(options NotificationOptions) error

	// CloseNotification 关闭通知(如果平台支持)。
	CloseNotification(notificationID string) error

	// IsSupported 检查当前平台是否支持通知。
	IsSupported() bool
}

// WindowArrangerProvider 提供窗口排列功能接口。
// 这是一个可选接口,用于窗口自动排列。
type WindowArrangerProvider interface {
	// AutoArrangeWindows 自动排列窗口(网格布局)。
	// windows: 要排列的窗口句柄列表
	// screenIndex: 目标屏幕索引(-1表示当前屏幕)
	AutoArrangeWindows(windows []WindowHandle, screenIndex int) error

	// CustomArrangeWindows 自定义排列窗口。
	// windows: 要排列的窗口句柄列表
	// params: 排列参数
	CustomArrangeWindows(windows []WindowHandle, params WindowArrangeParams) error

	// CalculateWindowLayout 计算窗口布局(不实际移动窗口)。
	// windowCount: 窗口数量
	// params: 排列参数
	// 返回: 每个窗口应该的位置
	CalculateWindowLayout(windowCount int, params WindowArrangeParams) ([]Rect, error)
}

// PermissionChecker 提供权限检查接口(平台特定)。
type PermissionChecker interface {
	// CheckAccessibilityPermission 检查辅助功能权限(macOS需要)。
	CheckAccessibilityPermission() (bool, error)

	// CheckAdminPermission 检查管理员权限(Windows需要)。
	CheckAdminPermission() (bool, error)

	// RequestPermission 请求必要的权限。
	RequestPermission() error
}

// AdminProvider 提供管理员权限管理接口
type AdminProvider interface {
	// IsRunningAsAdmin 检查当前进程是否以管理员权限运行
	IsRunningAsAdmin() bool

	// CheckAdminPrivileges 检查并打印管理员权限状态
	CheckAdminPrivileges()

	// RequestAdminPrivileges 请求管理员权限（重新启动程序）
	RequestAdminPrivileges() error
}

// DialogProvider 提供系统对话框接口
type DialogProvider interface {
	// SelectFolderDialog 打开文件夹选择对话框
	// title: 对话框标题
	// 返回: 选择的文件夹路径，如果用户取消则返回错误
	SelectFolderDialog(title string) (string, error)

	// SaveFileDialog 打开文件保存对话框
	// title: 对话框标题
	// defaultFileName: 默认文件名
	// fileFilter: 文件过滤器 (例如: "JSON Files|*.json|All Files|*.*")
	// 返回: 选择的文件路径，如果用户取消则返回错误
	SaveFileDialog(title, defaultFileName, fileFilter string) (string, error)

	// SelectFileDialog 打开文件选择对话框
	// title: 对话框标题
	// fileFilter: 文件过滤器 (例如: "Executable Files|*.exe|All Files|*.*")
	// 返回: 选择的文件路径，如果用户取消则返回错误
	SelectFileDialog(title, fileFilter string) (string, error)
}

// 编译时接口实现检查
// 当创建具体平台实现时,应该使用这些检查确保实现了所有接口

// var _ PlatformProvider = (*WindowsProvider)(nil)  // Windows实现检查
// var _ PlatformProvider = (*DarwinProvider)(nil)   // macOS实现检查

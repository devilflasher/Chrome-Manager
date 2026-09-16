package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"chromemanager/api"
	"chromemanager/config"
	"chromemanager/platform"
	"chromemanager/platform/common"
	"chromemanager/syncmanager"
	"chromemanager/utils"
	"embed"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIconData []byte

type Settings = config.Settings

type WindowConfig = config.WindowConfig

// ChromeService 提供Chrome窗口管理功能
type ChromeService struct {
	app              *application.App
	systray          *application.SystemTray
	mainWindow       *application.WebviewWindow
	syncManager      *syncmanager.SyncManager
	settingsMux      sync.RWMutex
	cachedSettings   *config.Settings
	windowArranger   *utils.WindowArranger
	masterStyler     *utils.MasterWindowStyler
	tabManager       *utils.TabManager
	inputManager     *utils.InputManager
	envCreator       *utils.EnvironmentCreator // 添加环境创建器
	provider         common.PlatformProvider   // 平台抽象层 Provider
	registeredHotkey string                    // 已注册的快捷键字符串
}

const (
	syncToggleHotkeyID    = 1
	syncToggleHotkeyEvent = "sync.toggle.hotkey"
	syncAutoStoppedEvent  = "sync.auto.stopped"
	syncStartedEvent      = "sync.started"      // 同步成功启动
	syncStartFailedEvent  = "sync.start.failed" // 同步启动失败
	syncStoppedEvent      = "sync.stopped"      // 同步成功停止
	syncStopFailedEvent   = "sync.stop.failed"  // 同步停止失败
	windowsUpdatedEvent   = "windows.updated"   // 窗口列表已更新
	selectionUpdatedEvent = "selection.updated" // 选中状态已更新
)

// NewChromeService 创建新的ChromeService实例
const (
	gracefulCloseTimeoutUnits  = 30
	profileReleaseTimeout      = 15 * time.Second
	profileReleasePollInterval = 250 * time.Millisecond
	closeSyncDrainDelay        = 350 * time.Millisecond
	defaultWindowCloseInterval = 0.1
	closeWindowSettleTimeout   = 2 * time.Second
	closeWindowSettlePoll      = 200 * time.Millisecond
)

func NewChromeService() *ChromeService {
	// 初始化平台抽象层 Provider
	provider, err := platform.NewProvider()
	if err != nil {
		log.Fatalf("初始化平台 Provider 失败: %v", err)
	}

	service := &ChromeService{
		provider:       provider,
		windowArranger: utils.NewWindowArranger(),
		tabManager:     utils.NewTabManager(),
		inputManager:   utils.NewInputManager(),
	}

	if settings, err := service.loadSettings(); err == nil {
		service.cachedSettings = settings

		// 迁移旧的绝对坐标到相对坐标
		if settings.CustomArrangeParams.StartX != 0 || settings.CustomArrangeParams.StartY != 0 {
			// 初始化 windowArranger 以便获取屏幕信息
			if service.windowArranger == nil {
				service.windowArranger = utils.NewWindowArranger()
			}

			// 检查坐标是否为旧的绝对坐标
			if screens, err := service.windowArranger.GetScreensInfo(); err == nil && len(screens) > 0 {
				shouldReset := false
				startX := settings.CustomArrangeParams.StartX

				// 如果 StartX 匹配任何屏幕的偏移量，说明是旧的绝对坐标
				for _, screen := range screens {
					if startX == screen.WorkLeft || startX == screen.Left {
						shouldReset = true
						log.Printf("⚠️ 检测到旧的绝对坐标 (StartX=%d)，重置为相对坐标", startX)
						break
					}
				}

				if shouldReset {
					settings.CustomArrangeParams.StartX = 0
					settings.CustomArrangeParams.StartY = 0
					service.cachedSettings = settings
					if err := service.saveSettings(settings); err != nil {
						log.Printf("保存迁移后的设置失败: %v", err)
					} else {
						log.Printf("✅ 已自动修正配置文件中的坐标值")
					}
				}
			}
		}
	} else {
		service.cachedSettings = config.GetDefaultSettings()
	}

	// 初始化环境创建器
	service.envCreator = utils.NewEnvironmentCreator(
		service.cachedSettings.CacheDir,
		service.cachedSettings.ShortcutPath,
	)
	service.envCreator.ChromePath = service.cachedSettings.ChromePath
	service.envCreator.BrowserType = service.cachedSettings.BrowserType

	// 设置窗口排列完成后的回调函数 - 让软件窗口置顶
	service.windowArranger.SetOnArrangeComplete(func() error {
		return service.bringMainWindowToTop()
	})

	service.syncManager = syncmanager.NewSyncManager(provider, service)

	service.masterStyler = utils.NewMasterWindowStyler()

	// 应用同步快捷键设置（使用 provider 实现）
	if err := service.applySyncToggleHotkey(service.cachedSettings.SyncToggleHotkey); err != nil {
		log.Printf("注册同步快捷键失败: %v", err)
	}

	// 确保应用图标存在（从嵌入资源释放到 icons 目录）
	iconPath, err := ensureAppIcon()
	if err != nil {
		log.Printf("⚠️ 生成应用图标失败: %v，将使用 exe 文件作为图标", err)
		iconPath = "" // 空路径会让通知使用 exe
	}

	// 图标路径已在 provider 中使用，无需额外初始化
	_ = iconPath // 保留变量避免未使用警告

	return service
}

func (c *ChromeService) applySyncToggleHotkey(raw string) error {
	trimmed := strings.TrimSpace(raw)

	if c.provider == nil {
		if trimmed == "" {
			c.registeredHotkey = ""
			return nil
		}
		return fmt.Errorf("全局快捷键模块不可用")
	}

	previous := c.registeredHotkey

	if trimmed == "" {
		if previous != "" {
			if err := c.provider.Unregister(syncToggleHotkeyID); err != nil {
				return err
			}
		}
		c.registeredHotkey = ""
		return nil
	}

	if trimmed == previous {
		return nil
	}

	spec, err := c.provider.ParseHotkeySpec(trimmed)
	if err != nil {
		return err
	}

	callback := func() {
		c.handleSyncToggleHotkey()
	}

	if previous != "" {
		if err := c.provider.Unregister(syncToggleHotkeyID); err != nil {
			return err
		}
		c.registeredHotkey = ""
	}

	if err := c.provider.Register(syncToggleHotkeyID, spec, callback); err != nil {
		if previous != "" {
			if prevSpec, perr := c.provider.ParseHotkeySpec(previous); perr == nil {
				if regErr := c.provider.Register(syncToggleHotkeyID, prevSpec, callback); regErr == nil {
					c.registeredHotkey = previous
				}
			}
		}
		return err
	}

	c.registeredHotkey = trimmed
	return nil
}

func (c *ChromeService) handleSyncToggleHotkey() {
	if c.app == nil {
		return
	}

	c.app.EmitEvent(syncToggleHotkeyEvent)
}

func (c *ChromeService) ShutdownHotkeys() {
	if c.provider == nil {
		return
	}

	if c.registeredHotkey != "" {
		_ = c.provider.Unregister(syncToggleHotkeyID)
		c.registeredHotkey = ""
	}
}

// GetSettings 实现SettingsGetter接口
func (c *ChromeService) GetSettings() *config.Settings {
	c.settingsMux.RLock()
	defer c.settingsMux.RUnlock()
	return c.cachedSettings
}

// getConfigPath 获取配置文件路径
func (c *ChromeService) getConfigPath() string {
	configPath, _ := config.GetSettingsFilePath()
	return configPath
}

// ensureAppIcon 确保应用图标存在于 icons 目录中
// 如果不存在，则从嵌入的资源中释放出来
func ensureAppIcon() (string, error) {
	// 获取程序所在目录
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取程序路径失败: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	// 创建 icons 目录
	iconsDir := filepath.Join(exeDir, "icons")
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		return "", fmt.Errorf("创建 icons 目录失败: %w", err)
	}

	// 检查图标文件是否已存在
	iconPath := filepath.Join(iconsDir, "appicon.png")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		// 图标不存在，从嵌入资源中写入
		if err := os.WriteFile(iconPath, appIconData, 0644); err != nil {
			return "", fmt.Errorf("写入图标文件失败: %w", err)
		}
		log.Printf("✅ 已生成应用图标: %s", iconPath)
	} else {
		log.Printf("📌 使用已存在的应用图标: %s", iconPath)
	}

	return iconPath, nil
}

// 窗口配置常量统一在 config/config.go 中定义

// getDefaultSettings 获取默认设置
func (c *ChromeService) getDefaultSettings() *Settings {
	return &Settings{
		ShortcutPath:           "",
		CacheDir:               "",
		ScreenSelection:        "",
		AutoModifyShortcutIcon: true,
		WindowOpenSpeed:        0.1,
		WindowCloseInterval:    0.1,
		LastWindowNumbers:      "",
		WindowNumbersHistory:   "",
		CustomArrangeParams: config.CustomArrangeParams{
			Width:             500,
			Height:            400,
			StartX:            0,
			StartY:            0,
			HorizontalSpacing: 0,
			VerticalSpacing:   0,
			WindowsPerRow:     5,
		},
		CustomURLs:       make(map[string]string),
		SyncToggleHotkey: "",
		WindowConfig: WindowConfig{
			Width:     config.DefaultWindowWidth,
			Height:    config.DefaultWindowHeight,
			MinWidth:  0,  // 不使用，由硬编码常量控制
			MinHeight: 0,  // 不使用，由硬编码常量控制
			X:         -1, // -1表示使用系统默认位置
			Y:         -1, // -1表示使用系统默认位置
			Maximized: false,
		},
		// 分组相关默认设置
		EnableGroupMode: true,
		CurrentGroup:    "默认分组",
		Groups:          []config.GroupConfig{},
	}
}

// loadSettings 从文件加载设置
func (c *ChromeService) loadSettings() (*Settings, error) {
	configPath := c.getConfigPath()

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		settings := c.getDefaultSettings()
		_ = c.saveSettings(settings)
		return settings, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	if settings.WindowConfig.Width == 0 {
		settings.WindowConfig.Width = config.DefaultWindowWidth
		settings.WindowConfig.Height = config.DefaultWindowHeight
	}

	if settings.WindowCloseInterval <= 0 {
		settings.WindowCloseInterval = defaultWindowCloseInterval
	}

	if settings.SyncConfig.WheelEventThreshold == 0 {
		defaultSyncConfig := config.GetDefaultSettings().SyncConfig
		settings.SyncConfig = defaultSyncConfig
	}
	// 迁移旧配置到新Profiles结构
	if settings.Profiles == nil {
		settings.Profiles = make(map[string]config.BrowserProfile)
	}

	// 确保Chrome配置存在（从旧字段迁移）
	if _, ok := settings.Profiles[config.BrowserTypeChrome]; !ok {
		// 如果有旧的配置，迁移到Chrome Profile
		chromeProfile := config.BrowserProfile{
			ShortcutPath:   settings.ShortcutPath,
			CacheDir:       settings.CacheDir,
			ExecutablePath: settings.ChromePath,
			Groups:         settings.Groups,
			CurrentGroup:   settings.CurrentGroup,
		}

		// 如果旧配置也为空，初始化为空字符串
		if chromeProfile.ShortcutPath == "" {
			chromeProfile.ShortcutPath = ""
		}

		settings.Profiles[config.BrowserTypeChrome] = chromeProfile
	}

	// 初始化其他浏览器的默认Profile（如果不存在）
	browserTypes := []string{config.BrowserTypeEdge, config.BrowserTypeOpera, config.BrowserTypeBrave}
	for _, num := range browserTypes {
		if _, ok := settings.Profiles[num]; !ok {
			settings.Profiles[num] = config.BrowserProfile{
				ShortcutPath:   "",
				CacheDir:       "",
				ExecutablePath: "",
				Groups:         make([]config.GroupConfig, 0),
				CurrentGroup:   "",
			}
		}
	}

	// 确保默认浏览器类型
	if settings.BrowserType == "" {
		settings.BrowserType = config.BrowserTypeChrome
	}

	// 根据当前浏览器类型，更新顶层字段以保持向后兼容（可选，但推荐用于内部逻辑统一）
	currentProfile := settings.Profiles[settings.BrowserType]
	settings.ShortcutPath = currentProfile.ShortcutPath
	settings.CacheDir = currentProfile.CacheDir
	settings.ChromePath = currentProfile.ExecutablePath
	settings.Groups = currentProfile.Groups
	settings.CurrentGroup = currentProfile.CurrentGroup

	return &settings, nil
}

// saveSettings 保存设置到文件
func (c *ChromeService) saveSettings(settings *Settings) error {
	configPath := c.getConfigPath()

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return err
	}

	return nil
}

// SaveWindowSize 保存窗口尺寸
func (c *ChromeService) SaveWindowSize(width, height int) error {
	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	settings.WindowConfig.Width = width
	settings.WindowConfig.Height = height

	return c.saveSettings(settings)
}

// saveWindowPosition 保存窗口位置和状态
func (c *ChromeService) saveWindowPosition() error {
	c.settingsMux.Lock()
	defer c.settingsMux.Unlock()

	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	if c.mainWindow == nil {
		return fmt.Errorf("main window is nil")
	}

	x, y := c.mainWindow.Position()
	settings.WindowConfig.X = x
	settings.WindowConfig.Y = y

	width, height := c.mainWindow.Size()
	settings.WindowConfig.Width = width
	settings.WindowConfig.Height = height

	maximized := c.mainWindow.IsMaximised()
	settings.WindowConfig.Maximized = maximized

	err = c.saveSettings(settings)
	if err != nil {
		return err
	}

	c.cachedSettings = settings

	return nil
}

// OpenWindows 打开Chrome窗口
func (c *ChromeService) OpenWindows(windowNumbers string) error {
	if c.provider == nil {
		return fmt.Errorf("provider not initialized")
	}

	numbers, err := c.provider.ParseWindowNumbers(windowNumbers)
	if err != nil {
		return fmt.Errorf("invalid window numbers: %v", err)
	}

	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	} else {
		settings.LastWindowNumbers = windowNumbers    // 保存到最后使用的编号
		settings.WindowNumbersHistory = windowNumbers // 保存到历史记录
		if err := c.saveSettings(settings); err != nil {
		} else {
		}

		c.settingsMux.Lock()
		c.cachedSettings = settings
		c.settingsMux.Unlock()
	}

	// 从快捷方式目录解析快捷方式
	groupShortcutPath := config.GetGroupShortcutPath(settings)
	if groupShortcutPath == "" {
		return fmt.Errorf("shortcut path is not configured")
	}

	shortcuts, err := c.provider.ParseShortcutsFromDirectory(groupShortcutPath)
	if err != nil {
		return fmt.Errorf("failed to parse shortcuts: %v", err)
	}

	shortcutMap := make(map[int]common.ShortcutInfo)
	for _, shortcut := range shortcuts {
		shortcutMap[shortcut.Number] = shortcut
	}

	var openedWindows []syncmanager.WindowInfo
	var notFoundWindows []int
	var failedWindows []int
	var failedWindowDetails []string
	var closingWindows []int

	for _, number := range numbers {
		shortcut, exists := shortcutMap[number]
		if !exists {
			notFoundWindows = append(notFoundWindows, number)
			continue
		}

		windowInfo, err := c.launchChromeFromShortcut(shortcut, settings)
		if err != nil {
			if isProfileClosingError(err) {
				closingWindows = append(closingWindows, number)
				continue
			}
			failedWindows = append(failedWindows, number)
			failedWindowDetails = append(failedWindowDetails, fmt.Sprintf("窗口 %d: %v", number, err))
			continue
		}

		openedWindows = append(openedWindows, *windowInfo)

		// 添加延迟以避免同时启动太多窗口
		time.Sleep(time.Duration(settings.WindowOpenSpeed*1000) * time.Millisecond)
	}

	// 构建详细的提示消息
	if len(openedWindows) == 0 {
		// 如果一个窗口都没打开，返回错误
		var errMsg string
		if len(notFoundWindows) > 0 {
			errMsg = fmt.Sprintf("未找到对应的窗口快捷方式: %v", notFoundWindows)
		}
		if len(closingWindows) > 0 {
			if errMsg != "" {
				errMsg += "; "
			}
			errMsg += profileClosingMessage(closingWindows)
		}
		if len(failedWindows) > 0 {
			if errMsg != "" {
				errMsg += "; "
			}
			errMsg += fmt.Sprintf("启动失败的窗口: %v", failedWindows)
			if len(failedWindowDetails) > 0 {
				errMsg += fmt.Sprintf("；首个错误：%s", failedWindowDetails[0])
			}
		}
		return errors.New(errMsg)
	} else if len(notFoundWindows) > 0 || len(closingWindows) > 0 || len(failedWindows) > 0 {
		// 如果部分窗口打开成功，部分失败，返回包含详细信息的错误
		var warnMsg string
		warnMsg = fmt.Sprintf("成功打开 %d 个窗口", len(openedWindows))
		if len(notFoundWindows) > 0 {
			warnMsg += fmt.Sprintf("，未找到以下窗口: %v", notFoundWindows)
		}
		if len(closingWindows) > 0 {
			warnMsg += "；" + profileClosingMessage(closingWindows)
		}
		if len(failedWindows) > 0 {
			warnMsg += fmt.Sprintf("，启动失败: %v", failedWindows)
			if len(failedWindowDetails) > 0 {
				warnMsg += fmt.Sprintf("；首个错误：%s", failedWindowDetails[0])
			}
		}
		return errors.New(warnMsg)
	}

	return nil
}

func isProfileClosingError(err error) bool {
	if err == nil {
		return false
	}

	message := err.Error()
	return strings.Contains(message, "profile is still closing") ||
		strings.Contains(message, "profile still busy")
}

func profileClosingMessage(windowNumbers []int) string {
	return fmt.Sprintf("窗口 %s 的浏览器进程仍在收尾，正在释放配置目录，请稍后再打开", formatWindowNumberList(windowNumbers))
}

func formatWindowNumberList(windowNumbers []int) string {
	if len(windowNumbers) == 0 {
		return ""
	}

	sortedNumbers := append([]int(nil), windowNumbers...)
	sort.Ints(sortedNumbers)

	parts := make([]string, 0, len(sortedNumbers))
	for _, number := range sortedNumbers {
		parts = append(parts, fmt.Sprintf("%d", number))
	}

	return strings.Join(parts, "、")
}

// launchChromeFromShortcut 从快捷方式启动Chrome窗口
func (c *ChromeService) launchChromeFromShortcut(shortcut common.ShortcutInfo, settings *Settings) (*syncmanager.WindowInfo, error) {
	// 分配调试端口
	debugPort := config.BaseDebugPort + shortcut.Number

	// 构建新的Chrome启动参数（基于原始参数，添加默认参数）
	newArgs := shortcut.Arguments

	// 设置调试端口参数（替换或添加）
	debugPortArg := fmt.Sprintf("--remote-debugging-port=%d", debugPort)
	if strings.Contains(newArgs, "--remote-debugging-port=") {
		// 替换已有的调试端口参数
		re := regexp.MustCompile(`--remote-debugging-port=\d+`)
		newArgs = re.ReplaceAllString(newArgs, debugPortArg)
	} else {
		// 添加新的调试端口参数
		newArgs += " " + debugPortArg
	}

	// 添加默认的Chrome参数（如果参数中没有的话）
	for _, defaultArg := range config.ChromeDefaultArgs {
		if !strings.Contains(newArgs, defaultArg) {
			newArgs += " " + defaultArg
		}
	}

	// 从原始参数中提取用户数据目录（用于返回信息）
	userDataDir := extractUserDataDirFromArgs(newArgs)
	if err := c.waitForProfileRelease(userDataDir, profileReleaseTimeout); err != nil {
		return nil, fmt.Errorf("profile is still closing for window %d: %v", shortcut.Number, err)
	}

	// 直接启动Chrome，不使用快捷方式

	args, err := parseArguments(newArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %v", err)
	}

	// 直接启动Chrome进程
	cmd := exec.Command(shortcut.TargetPath, args...)
	cmd.Dir = shortcut.WorkingDir // 设置工作目录

	launchTarget := shortcut.TargetPath
	pid := int32(0)
	if directErr := cmd.Start(); directErr != nil {
		fallbackTarget, resolveErr := c.resolveBrowserExecutableForFallback(settings)
		if resolveErr != nil {
			return nil, fmt.Errorf("直接启动浏览器失败: %v；查找系统浏览器用于兜底启动也失败: %v", directErr, resolveErr)
		}

		fallbackWorkingDir := filepath.Dir(fallbackTarget)
		if shellErr := shellLaunchExecutable(fallbackTarget, newArgs, fallbackWorkingDir); shellErr != nil {
			return nil, fmt.Errorf("直接启动浏览器失败: %v；Windows Shell 兜底启动失败: %v", directErr, shellErr)
		}
		launchTarget = fallbackTarget
		log.Printf("窗口 %d 直接启动失败，已通过 Windows Shell 成功启动: %v", shortcut.Number, directErr)
	} else {
		pid = int32(cmd.Process.Pid)
	}

	// 等待进程启动
	time.Sleep(time.Duration(settings.WindowOpenSpeed*1000) * time.Millisecond)

	windowInfo := &syncmanager.WindowInfo{
		Number:      shortcut.Number,
		DebugPort:   debugPort,
		UserDataDir: userDataDir,
		CommandLine: strings.TrimSpace(launchTarget + " " + newArgs),
		PID:         pid,
	}

	return windowInfo, nil
}

func (c *ChromeService) resolveBrowserExecutableForFallback(settings *Settings) (string, error) {
	if settings != nil && settings.ChromePath != "" {
		if info, err := os.Stat(settings.ChromePath); err == nil && !info.IsDir() {
			return settings.ChromePath, nil
		}
	}

	if c.provider == nil {
		return "", fmt.Errorf("浏览器提供程序未初始化")
	}

	browserType := config.BrowserTypeChrome
	if settings != nil && settings.BrowserType != "" {
		browserType = settings.BrowserType
	}
	browserPath, err := c.provider.FindBrowserPath(browserType)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(browserPath); err != nil || info.IsDir() {
		if err != nil {
			return "", fmt.Errorf("浏览器程序不可访问: %w", err)
		}
		return "", fmt.Errorf("浏览器路径不是可执行文件: %s", browserPath)
	}
	return browserPath, nil
}

// extractUserDataDirFromArgs 从参数中提取用户数据目录
func extractUserDataDirFromArgs(args string) string {
	// 先尝试匹配带引号的路径
	reQuoted := regexp.MustCompile(`--user-data-dir=["']([^"']+)["']`)
	if matches := reQuoted.FindStringSubmatch(args); len(matches) > 1 {
		return matches[1]
	}

	// 如果没有引号，匹配到下一个空格为止
	reUnquoted := regexp.MustCompile(`--user-data-dir=([^\s]+)`)
	if matches := reUnquoted.FindStringSubmatch(args); len(matches) > 1 {
		return matches[1]
	}

	return ""
}

// parseArguments 解析参数字符串为参数数组
func parseArguments(args string) ([]string, error) {
	var result []string
	var current strings.Builder
	inQuotes := false
	escapeNext := false

	for i, char := range args {
		if escapeNext {
			current.WriteRune(char)
			escapeNext = false
			continue
		}

		switch char {
		case '\\':
			if i+1 < len(args) && (args[i+1] == '"' || args[i+1] == '\'') {
				escapeNext = true
			} else {
				current.WriteRune(char)
			}
		case '"', '\'':
			inQuotes = !inQuotes
		case ' ', '\t':
			if inQuotes {
				current.WriteRune(char)
			} else if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result, nil
}

func normalizeUserDataDir(userDataDir string) string {
	if userDataDir == "" {
		return ""
	}

	normalized := filepath.Clean(userDataDir)
	if runtime.GOOS == "windows" {
		normalized = strings.ToLower(normalized)
	}
	return normalized
}

func getChromeProfileLockFiles(userDataDir string) []string {
	if userDataDir == "" {
		return nil
	}

	root := filepath.Clean(userDataDir)
	return []string{
		filepath.Join(root, "SingletonLock"),
		filepath.Join(root, "SingletonCookie"),
		filepath.Join(root, "SingletonSocket"),
	}
}

func (c *ChromeService) isProfileStillInUse(userDataDir string) bool {
	targetDir := normalizeUserDataDir(userDataDir)
	if targetDir == "" {
		return false
	}

	windows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return false
	}

	for _, window := range windows {
		if normalizeUserDataDir(window.UserDataDir) != targetDir {
			continue
		}

		if window.ProcessID == 0 || c.provider.IsProcessRunning(window.ProcessID) {
			return true
		}
	}

	return false
}

func (c *ChromeService) waitForProfileRelease(userDataDir string, timeout time.Duration) error {
	targetDir := normalizeUserDataDir(userDataDir)
	if targetDir == "" {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for {
		inUse := c.isProfileStillInUse(targetDir)
		lockedFiles := make([]string, 0, 3)

		for _, lockFile := range getChromeProfileLockFiles(targetDir) {
			if _, err := os.Stat(lockFile); err == nil {
				lockedFiles = append(lockedFiles, filepath.Base(lockFile))
			} else if !errors.Is(err, os.ErrNotExist) {
				lockedFiles = append(lockedFiles, filepath.Base(lockFile))
			}
		}

		if !inUse && len(lockedFiles) == 0 {
			return nil
		}

		if time.Now().After(deadline) {
			reasons := make([]string, 0, 2)
			if inUse {
				reasons = append(reasons, "existing Chrome process still running")
			}
			if len(lockedFiles) > 0 {
				reasons = append(reasons, fmt.Sprintf("profile lock files still present: %s", strings.Join(lockedFiles, ", ")))
			}
			return fmt.Errorf("profile still busy for %s: %s", targetDir, strings.Join(reasons, "; "))
		}

		time.Sleep(profileReleasePollInterval)
	}
}

// ImportWindows 导入现有Chrome窗口
func (c *ChromeService) isWindowClosePending(hwnd uintptr, pid int32) bool {
	if valid, _ := c.provider.IsWindowValid(common.WindowHandle(hwnd)); valid {
		return true
	}

	if pid != 0 && c.provider.IsProcessRunning(pid) {
		return true
	}

	return false
}

func (c *ChromeService) waitForWindowClose(hwnd uintptr, pid int32, timeout time.Duration) bool {
	if !c.isWindowClosePending(hwnd, pid) {
		return false
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !c.isWindowClosePending(hwnd, pid) {
			return false
		}
		time.Sleep(closeWindowSettlePoll)
	}

	return c.isWindowClosePending(hwnd, pid)
}

func normalizeWindowCloseInterval(seconds float64) time.Duration {
	if seconds <= 0 {
		seconds = defaultWindowCloseInterval
	}
	if seconds > 1.5 {
		seconds = 1.5
	}

	return time.Duration(seconds * float64(time.Second))
}

func (c *ChromeService) ImportWindows() ([]syncmanager.WindowInfo, error) {

	chromeWindows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to find Chrome windows: %v", err)
	}
	settings, settingsErr := c.loadSettings()
	if settingsErr != nil {
		settings = c.getDefaultSettings()
	}
	chromeWindows = c.resolveImportedWindowNumbers(chromeWindows, settings)

	var windowInfos []syncmanager.WindowInfo
	var mu sync.Mutex
	var wg sync.WaitGroup

	usedNumbers := make(map[int]bool)
	tempNumber := 1 // 临时编号从1开始，而不是1000

	// 并发处理所有Chrome窗口
	for _, chromeWindow := range chromeWindows {
		wg.Add(1)
		go func(wp common.WindowInfo) {
			defer wg.Done()

			windowInfo := syncmanager.WindowInfo{
				Number:      wp.Number,
				PID:         wp.ProcessID,
				HWND:        wp.HWND,
				Title:       wp.Title,
				DebugPort:   wp.DebugPort,
				UserDataDir: wp.UserDataDir,
				CommandLine: wp.CommandLine,
				IsMaster:    false,
				IsRunning:   true, // 导入的窗口都是正在运行的
			}

			// 线程安全地处理编号分配
			mu.Lock()
			// 如果无法从命令行提取编号，分配临时编号
			if windowInfo.Number == 0 {
				for usedNumbers[tempNumber] {
					tempNumber++
				}
				windowInfo.Number = tempNumber
				usedNumbers[tempNumber] = true
			} else {
				usedNumbers[windowInfo.Number] = true
			}

			// 验证窗口信息完整性
			if windowInfo.Title == "" {
				windowInfo.Title = fmt.Sprintf("Chrome Window %d", windowInfo.Number)
			}

			windowInfos = append(windowInfos, windowInfo)
			mu.Unlock()

		}(chromeWindow)
	}

	// 等待所有并发处理完成
	wg.Wait()

	// 按窗口编号排序，提供更好的用户体验
	sort.Slice(windowInfos, func(i, j int) bool {
		return windowInfos[i].Number < windowInfos[j].Number
	})

	// 无论是否找到窗口，都更新同步管理器的状态（支持空列表清空状态）
	if c.syncManager != nil {
		_ = c.syncManager.NotifyWindowsImported(windowInfos)
	}

	// 发送事件通知前端刷新 UI
	if c.app != nil {
		c.app.EmitEvent(windowsUpdatedEvent, windowInfos)
	}

	return windowInfos, nil
}

// SetMasterWindow 设置主控窗口（公共接口）
func (c *ChromeService) SetMasterWindow(windowNumber int) error {
	return c.setMasterWindowInternal(windowNumber, true)
}

// SelectAllWindows API 触发的全选操作
func (c *ChromeService) SelectAllWindows(selected bool) error {
	// 更新后端的选中状态
	if c.syncManager != nil {
		_ = c.syncManager.SelectAllWindows(selected)
	}

	// 通知前端刷新 UI
	if c.app != nil {
		c.app.EmitEvent(selectionUpdatedEvent, map[string]interface{}{
			"selected": selected,
		})
	}
	return nil
}

// setMasterWindowInternal 设置主控窗口（内部方法）
func (c *ChromeService) setMasterWindowInternal(windowNumber int, shouldStopSync bool) error {

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	windowInfo := c.syncManager.GetWindowByID(windowNumber)
	if windowInfo == nil {
		return fmt.Errorf("window %d not found", windowNumber)
	}

	currentMaster := c.getCurrentMasterWindowID()
	var currentMasterInfo *syncmanager.WindowInfo
	if currentMaster != 0 {
		currentMasterInfo = c.syncManager.GetWindowByID(currentMaster)
	}

	needSwitchMaster := currentMaster != 0 && currentMaster != windowNumber

	if needSwitchMaster && c.masterStyler != nil && currentMasterInfo != nil {
		_ = c.masterStyler.ForceClearMasterWindowStyle(currentMasterInfo.HWND)
	}

	if shouldStopSync && c.syncManager.IsRunning() {
		if needSwitchMaster {
			_ = c.syncManager.StopSync()
		} else {
			_ = c.StopSync()
		}
	}

	if err := c.syncManager.SetMasterWindow(windowNumber); err != nil {
		return err
	}

	if c.masterStyler != nil {
		_ = c.masterStyler.SetMasterWindowStyle(windowInfo.HWND)
	}

	return nil
}

func (c *ChromeService) getCurrentMasterWindowID() int {
	if c.syncManager == nil {
		return 0
	}
	state := c.syncManager.GetState()
	if state.MasterWindowInfo != nil {
		return state.MasterWindowInfo.Number
	}
	return 0
}

// StartSync 开始同步 - 异步执行，立即返回
func (c *ChromeService) StartSync(masterWindowNumber int, slaveWindowNumbers []int) error {
	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 快速参数验证
	actualMasterNumber := masterWindowNumber
	actualSlaveNumbers := slaveWindowNumbers

	if masterWindowNumber <= 0 && len(slaveWindowNumbers) > 0 {
		actualMasterNumber = slaveWindowNumbers[0]
		actualSlaveNumbers = slaveWindowNumbers[1:]
	}

	if actualMasterNumber <= 0 {
		return fmt.Errorf("无法开始同步: 需要至少选择一个窗口")
	}

	masterWindow := c.syncManager.GetWindowByID(actualMasterNumber)
	if masterWindow == nil {
		return fmt.Errorf("主控窗口 %d 未找到", actualMasterNumber)
	}

	// 在goroutine中异步执行耗时操作，不阻塞UI
	go func() {
		// 先停止已有同步
		if c.syncManager.IsRunning() {
			_ = c.syncManager.StopSync()
		}

		// 收集从属窗口信息，保留 debugPort 等元数据供底层同步器使用
		var slaveWindows []syncmanager.WindowInfo
		for _, slaveNumber := range actualSlaveNumbers {
			slaveWindow := c.syncManager.GetWindowByID(slaveNumber)
			if slaveWindow != nil {
				slaveWindows = append(slaveWindows, *slaveWindow)
			}
		}

		// 设置主控窗口样式
		if err := c.setMasterWindowInternal(actualMasterNumber, false); err != nil {
			log.Printf("设置主控窗口失败: %v", err)
			if c.app != nil {
				c.app.EmitEvent(syncStartFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// 启动同步
		if err := c.syncManager.Start(masterWindow, slaveWindows); err != nil {
			log.Printf("启动同步失败: %v", err)
			if c.app != nil {
				c.app.EmitEvent(syncStartFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// 发送成功事件
		if c.app != nil {
			c.app.EmitEvent(syncStartedEvent, map[string]interface{}{
				"masterWindowId": actualMasterNumber,
			})
		}

		// 发送系统通知
		if c.provider != nil {
			message := "同步已启动"
			if actualMasterNumber > 0 {
				message = fmt.Sprintf("同步已启动 - 主控窗口: %d", actualMasterNumber)
			}
			if err := c.provider.ShowNotification(common.NotificationOptions{
				Title:   "Chrome多窗口管理器 V4.0",
				Message: message,
				Silent:  true,
			}); err != nil {
				log.Printf("发送同步开启通知失败: %v", err)
			}
		}
	}()

	// 立即返回，不阻塞UI
	return nil
}

// StopSync 停止同步 - 异步执行，立即返回
func (c *ChromeService) StopSync() error {
	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 在goroutine中异步执行耗时操作，不阻塞UI
	go func() {
		// 清除主控窗口样式
		if c.masterStyler != nil {
			currentMaster := c.getCurrentMasterWindowID()
			if currentMaster != 0 {
				if currentMasterInfo := c.syncManager.GetWindowByID(currentMaster); currentMasterInfo != nil {
					c.masterStyler.ClearMasterWindowStyle(currentMasterInfo.HWND)
				}
			}
		}

		// 停止同步
		if err := c.syncManager.StopSync(); err != nil {
			log.Printf("停止同步失败: %v", err)
			if c.app != nil {
				c.app.EmitEvent(syncStopFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// 发送成功事件
		if c.app != nil {
			c.app.EmitEvent(syncStoppedEvent)
		}

		// 发送系统通知
		if c.provider != nil {
			if err := c.provider.ShowNotification(common.NotificationOptions{
				Title:   "Chrome多窗口管理器 V4.0",
				Message: "同步已停止",
				Silent:  true,
			}); err != nil {
				log.Printf("发送同步关闭通知失败: %v", err)
			}
		}
	}()

	// 立即返回，不阻塞UI
	return nil
}

func (c *ChromeService) GetSyncStatus() (map[string]interface{}, error) {
	if c.syncManager == nil {
		return nil, fmt.Errorf("sync manager not initialized")
	}

	state := c.syncManager.GetState()
	return map[string]interface{}{
		"status":        string(state.Status),
		"totalAgents":   state.TotalAgents,
		"activeAgents":  state.ActiveAgents,
		"totalCommands": state.TotalCommands,
		"masterWindow":  state.MasterWindowInfo,
		"lastError":     state.LastError,
		"startTime":     state.StartTime,
	}, nil
}

// GetImportedWindows 获取已导入的窗口列表（用于前端显示）
func (c *ChromeService) GetImportedWindows() ([]syncmanager.WindowInfo, error) {
	chromeWindows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to get imported windows: %v", err)
	}
	settings, settingsErr := c.loadSettings()
	if settingsErr != nil {
		settings = c.getDefaultSettings()
	}
	chromeWindows = c.resolveImportedWindowNumbers(chromeWindows, settings)

	var windowInfos []syncmanager.WindowInfo
	for _, chromeWindow := range chromeWindows {
		windowInfo := syncmanager.WindowInfo{
			Number:      chromeWindow.Number,
			PID:         chromeWindow.ProcessID,
			HWND:        chromeWindow.HWND,
			Title:       chromeWindow.Title,
			DebugPort:   chromeWindow.DebugPort,
			UserDataDir: chromeWindow.UserDataDir,
			CommandLine: chromeWindow.CommandLine,
			IsMaster:    false,
			IsRunning:   c.provider.IsProcessRunning(chromeWindow.ProcessID),
		}

		if !windowInfo.IsRunning {
			continue
		}

		windowInfos = append(windowInfos, windowInfo)
	}

	return windowInfos, nil
}

func (c *ChromeService) RefreshWindowList() ([]syncmanager.WindowInfo, error) {
	return c.GetImportedWindows()
}

// GetUpdatedWindowList 获取实时更新的窗口列表，自动移除已关闭的窗口
func (c *ChromeService) GetUpdatedWindowList() ([]syncmanager.WindowInfo, error) {
	chromeWindows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to find Chrome windows: %v", err)
	}

	runningWindowMap := make(map[int32]common.WindowInfo)
	for _, window := range chromeWindows {
		runningWindowMap[window.ProcessID] = window
	}

	previousWindows := c.syncManager.GetAllImportedWindows()

	var updatedWindows []syncmanager.WindowInfo
	for _, prevWindow := range previousWindows {
		if runningWindow, isRunning := runningWindowMap[prevWindow.PID]; isRunning {
			updatedWindows = append(updatedWindows, syncmanager.WindowInfo{
				Number:      prevWindow.Number,
				PID:         prevWindow.PID,
				HWND:        prevWindow.HWND,
				Title:       runningWindow.Title,
				DebugPort:   prevWindow.DebugPort,
				UserDataDir: prevWindow.UserDataDir,
				CommandLine: runningWindow.CommandLine,
				IsMaster:    prevWindow.IsMaster,
				IsRunning:   true,
			})
		}
	}

	for _, runningWindow := range chromeWindows {
		found := false
		for _, prevWindow := range previousWindows {
			if prevWindow.PID == runningWindow.ProcessID {
				found = true
				break
			}
		}

		if !found {
			updatedWindows = append(updatedWindows, syncmanager.WindowInfo{
				Number:      runningWindow.Number,
				PID:         runningWindow.ProcessID,
				HWND:        runningWindow.HWND,
				Title:       runningWindow.Title,
				DebugPort:   runningWindow.DebugPort,
				UserDataDir: runningWindow.UserDataDir,
				CommandLine: runningWindow.CommandLine,
				IsMaster:    false,
				IsRunning:   true,
			})
		}
	}

	sort.Slice(updatedWindows, func(i, j int) bool {
		return updatedWindows[i].Number < updatedWindows[j].Number
	})

	if len(updatedWindows) > 0 {
		_ = c.syncManager.NotifyWindowsImported(updatedWindows)
	}

	return updatedWindows, nil
}

func (c *ChromeService) SelectFolder() (string, error) {
	if c.app == nil {
		return "", fmt.Errorf("app not initialized")
	}

	dialog := application.OpenFileDialog()
	dialog.CanChooseDirectories(true)
	dialog.CanChooseFiles(false)
	dialog.SetTitle("请选择文件夹")

	folderPath, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("文件夹选择失败: %v", err)
	}
	if folderPath == "" {
		return "", fmt.Errorf("user cancelled folder selection")
	}

	return folderPath, nil
}

// SaveFileDialog 打开文件保存对话框
func (c *ChromeService) SaveFileDialog(defaultFileName string, fileFilter string) (string, error) {
	if c.provider == nil {
		return "", fmt.Errorf("provider not initialized")
	}

	filePath, err := c.provider.SaveFileDialog("保存文件", defaultFileName, fileFilter)
	if err != nil {
		return "", fmt.Errorf("文件保存对话框失败: %v", err)
	}

	return filePath, nil
}

// SaveJSONFile 保存JSON数据到文件
func (c *ChromeService) SaveJSONFile(filePath string, jsonData string) error {

	if err := os.WriteFile(filePath, []byte(jsonData), 0644); err != nil {
		return fmt.Errorf("保存JSON文件失败: %v", err)
	}

	return nil
}

// RestoreDefaultIcons 一键还原快捷方式图标（支持所有分组）
func (c *ChromeService) RestoreDefaultIcons() error {
	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	restorer := utils.NewShortcutRestorer()
	return restorer.RestoreToDefaultIconsAllGroups(settings)
}

// CleanIconCache 一键清理图标缓存
func (c *ChromeService) CleanIconCache() error {
	iconManager, err := utils.NewOptimizedIconManager()
	if err != nil {
		return fmt.Errorf("创建优化图标管理器失败: %v", err)
	}

	if err := iconManager.ClearCache(); err != nil {
		return fmt.Errorf("清理图标缓存失败: %v", err)
	}

	return nil
}

func (c *ChromeService) ImportWindowsWithIconSettings() ([]syncmanager.WindowInfo, error) {
	// 加载设置以获取图标配置
	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	chromeWindows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to find Chrome windows: %v", err)
	}
	chromeWindows = c.resolveImportedWindowNumbers(chromeWindows, settings)

	var (
		windowInfos []syncmanager.WindowInfo
		mu          sync.Mutex
		wg          sync.WaitGroup
	)

	usedNumbers := make(map[int]bool)
	tempNumber := 1

	for _, chromeWindow := range chromeWindows {
		wg.Add(1)
		go func(wp common.WindowInfo) {
			defer wg.Done()

			windowInfo := syncmanager.WindowInfo{
				Number:      wp.Number,
				PID:         wp.ProcessID,
				HWND:        wp.HWND,
				Title:       wp.Title,
				DebugPort:   wp.DebugPort,
				UserDataDir: wp.UserDataDir,
				CommandLine: wp.CommandLine,
				IsMaster:    false,
				IsRunning:   true,
			}

			mu.Lock()
			if windowInfo.Number == 0 {
				for usedNumbers[tempNumber] {
					tempNumber++
				}
				windowInfo.Number = tempNumber
				usedNumbers[tempNumber] = true
			} else {
				usedNumbers[windowInfo.Number] = true
			}

			if windowInfo.Title == "" {
				windowInfo.Title = fmt.Sprintf("Chrome Window %d", windowInfo.Number)
			}

			windowInfos = append(windowInfos, windowInfo)
			mu.Unlock()
		}(chromeWindow)
	}

	wg.Wait()

	sort.Slice(windowInfos, func(i, j int) bool {
		return windowInfos[i].Number < windowInfos[j].Number
	})

	// 处理任务栏图标（如果启用）
	if settings.AutoModifyShortcutIcon && len(chromeWindows) > 0 {
		// 将 common.WindowInfo 转换为 utils.ChromeProcessInfo 以便使用图标管理器
		chromeProcessInfos := make([]utils.ChromeProcessInfo, 0, len(chromeWindows))
		for _, window := range chromeWindows {
			chromeProcessInfos = append(chromeProcessInfos, utils.ChromeProcessInfo{
				PID:         window.ProcessID,
				HWND:        window.HWND,
				Title:       window.Title,
				CommandLine: window.CommandLine,
				UserDataDir: window.UserDataDir,
				Number:      window.Number,
				DebugPort:   window.DebugPort,
			})
		}

		// 应用图标设置
		shortcutPath := config.GetGroupShortcutPath(settings)
		if iconManager, err := utils.NewOptimizedIconManager(); err == nil {
			iconManager.ApplyIconsToWindows(chromeProcessInfos, true, shortcutPath)
		} else {
			log.Printf("⚠️ 创建图标管理器失败: %v", err)
		}
	}

	// 无论是否找到窗口，都更新同步管理器的状态
	if c.syncManager != nil {
		_ = c.syncManager.NotifyWindowsImported(windowInfos)
	}

	// 发送事件通知前端刷新 UI
	if c.app != nil {
		c.app.EmitEvent(windowsUpdatedEvent, windowInfos)
	}

	return windowInfos, nil
}

func (c *ChromeService) GetSettingsForFrontend() (map[string]interface{}, error) {

	settings, err := c.loadSettings()
	if err != nil {
		return nil, err
	}

	filteredWindowConfig := map[string]interface{}{
		"Width":     settings.WindowConfig.Width,
		"Height":    settings.WindowConfig.Height,
		"X":         settings.WindowConfig.X,
		"Y":         settings.WindowConfig.Y,
		"Maximized": settings.WindowConfig.Maximized,
		// 故意不包含MinWidth和MinHeight，这些值不应该让用户修改
	}

	// 转换为map[string]interface{}格式返回给前端
	return map[string]interface{}{
		"ShortcutPath":           settings.ShortcutPath,
		"CacheDir":               settings.CacheDir,
		"ChromePath":             settings.ChromePath,
		"BrowserType":            settings.BrowserType,
		"ScreenSelection":        settings.ScreenSelection,
		"AutoModifyShortcutIcon": settings.AutoModifyShortcutIcon,
		"WindowOpenSpeed":        settings.WindowOpenSpeed,
		"WindowCloseInterval":    settings.WindowCloseInterval,
		"LastWindowNumbers":      settings.LastWindowNumbers,
		"LastEnvCreationNumbers": settings.LastEnvCreationNumbers, // 添加环境创建编号历史
		"CustomArrangeParams":    settings.CustomArrangeParams,    // 添加自定义排列参数
		"CustomURLs":             settings.CustomURLs,
		"SyncToggleHotkey":       settings.SyncToggleHotkey,
		"WindowConfig":           filteredWindowConfig, // 使用过滤后的配置
		// 分组相关字段
		"EnableGroupMode": settings.EnableGroupMode,
		"Groups":          settings.Groups,
		"CurrentGroup":    settings.CurrentGroup,

		// Profiles
		"Profiles": settings.Profiles,

		// OpenClaw API
		"EnableAPIServer": settings.EnableAPIServer,
		"APIPort":         settings.APIPort,
		"APIToken":        settings.APIToken,
	}, nil
}

func syncActiveBrowserProfile(settings *Settings) {
	if settings == nil {
		return
	}
	if settings.BrowserType == "" {
		settings.BrowserType = config.BrowserTypeChrome
	}
	if settings.Profiles == nil {
		settings.Profiles = make(map[string]config.BrowserProfile)
	}

	profile := settings.Profiles[settings.BrowserType]
	profile.ShortcutPath = settings.ShortcutPath
	profile.CacheDir = settings.CacheDir
	profile.ExecutablePath = settings.ChromePath
	profile.Groups = settings.Groups
	profile.CurrentGroup = settings.CurrentGroup
	settings.Profiles[settings.BrowserType] = profile
}

// UpdateSettings 更新设置
func (c *ChromeService) UpdateSettings(settingsMap map[string]interface{}) error {

	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	var (
		hotkeyProvided bool
		newHotkey      string
	)

	if value, ok := settingsMap["ShortcutPath"]; ok {
		if str, ok := value.(string); ok {
			settings.ShortcutPath = str
		}
	}
	if value, ok := settingsMap["CacheDir"]; ok {
		if str, ok := value.(string); ok {
			settings.CacheDir = str
		}
	}
	if value, ok := settingsMap["ChromePath"]; ok {
		if str, ok := value.(string); ok {
			settings.ChromePath = str
		}
	}
	if value, ok := settingsMap["BrowserType"]; ok {
		if str, ok := value.(string); ok {
			settings.BrowserType = str
		}
	}
	if value, ok := settingsMap["ScreenSelection"]; ok {
		if str, ok := value.(string); ok {
			settings.ScreenSelection = str
		}
	}
	if value, ok := settingsMap["AutoModifyShortcutIcon"]; ok {
		if b, ok := value.(bool); ok {
			settings.AutoModifyShortcutIcon = b
		}
	}
	if value, ok := settingsMap["WindowOpenSpeed"]; ok {
		if f, ok := value.(float64); ok {
			settings.WindowOpenSpeed = f
		}
	}
	if value, ok := settingsMap["WindowCloseInterval"]; ok {
		if f, ok := value.(float64); ok {
			switch {
			case f <= 0:
				settings.WindowCloseInterval = defaultWindowCloseInterval
			case f > 1.5:
				settings.WindowCloseInterval = 1.5
			default:
				settings.WindowCloseInterval = f
			}
		}
	}
	if value, ok := settingsMap["LastWindowNumbers"]; ok {
		if str, ok := value.(string); ok {
			settings.LastWindowNumbers = str
		}
	}
	if value, ok := settingsMap["LastEnvCreationNumbers"]; ok {
		if str, ok := value.(string); ok {
			settings.LastEnvCreationNumbers = str
		}
	}
	if value, ok := settingsMap["SyncToggleHotkey"]; ok {
		switch v := value.(type) {
		case string:
			newHotkey = v
			hotkeyProvided = true
		case nil:
			newHotkey = ""
			hotkeyProvided = true
		default:
			return fmt.Errorf("SyncToggleHotkey 必须为字符串")
		}
	}

	// 处理CustomURLs
	if value, ok := settingsMap["CustomURLs"]; ok {
		if customURLsMap, ok := value.(map[string]interface{}); ok {
			settings.CustomURLs = make(map[string]string)
			for k, v := range customURLsMap {
				if str, ok := v.(string); ok {
					settings.CustomURLs[k] = str
				}
			}
		}
	}

	// 处理分组相关字段
	if value, ok := settingsMap["EnableGroupMode"]; ok {
		if b, ok := value.(bool); ok {
			settings.EnableGroupMode = b
		}
	}

	if value, ok := settingsMap["CurrentGroup"]; ok {
		if str, ok := value.(string); ok {
			settings.CurrentGroup = str
		}
	}

	// Handle Profiles map update from frontend
	if value, ok := settingsMap["Profiles"]; ok {
		if profilesMap, ok := value.(map[string]interface{}); ok {
			if settings.Profiles == nil {
				settings.Profiles = make(map[string]config.BrowserProfile)
			}
			for browserType, profileData := range profilesMap {
				if profileMap, ok := profileData.(map[string]interface{}); ok {
					profile := config.BrowserProfile{}
					// If profile already exists, keep existing values as base (though we overwrite mostly)
					if existing, exists := settings.Profiles[browserType]; exists {
						profile = existing
					} else {
						// Ensure groups slice is initialized
						profile.Groups = make([]config.GroupConfig, 0)
					}

					if val, ok := profileMap["shortcutPath"].(string); ok {
						profile.ShortcutPath = val
					}
					if val, ok := profileMap["cacheDir"].(string); ok {
						profile.CacheDir = val
					}
					if val, ok := profileMap["executablePath"].(string); ok {
						profile.ExecutablePath = val
					}
					// Note: Groups inside profiles are not currently sent by frontend save logic deep merge,
					// relying on top-level Groups update?
					// Actually, frontend save sends 'Profiles' which contains whatever 'window.browserProfiles' has.
					// 'window.browserProfiles' usually has the data from 'GetSettingsForFrontend'.
					// If frontend didn't modify groups in the profile map explicitly, they might refer to old groups.
					// But 'UpdateSettings' also handles top-level 'Groups'.
					// To be safe, we should respect top-level 'Groups' for the ACTIVE browser type,
					// and 'Profiles' map for others.
					// But wait, the previous logic at bottom of this function syncs top-level fields TO the active profile.
					// So we should update profiles map here, and let the bottom logic overwrite the active profile with top-level fields (which are freshest from inputs).
					// Yes, that works.

					settings.Profiles[browserType] = profile
				}
			}
		}
	}

	if value, ok := settingsMap["Groups"]; ok {
		if groupsArray, ok := value.([]interface{}); ok {
			settings.Groups = make([]config.GroupConfig, 0, len(groupsArray))
			for _, groupInterface := range groupsArray {
				if groupMap, ok := groupInterface.(map[string]interface{}); ok {
					group := config.GroupConfig{}
					if name, ok := groupMap["name"].(string); ok {
						group.Name = name
					}
					if shortcutPath, ok := groupMap["shortcutPath"].(string); ok {
						group.ShortcutPath = shortcutPath
					}
					if cacheDir, ok := groupMap["cacheDir"].(string); ok {
						group.CacheDir = cacheDir
					}
					settings.Groups = append(settings.Groups, group)
				} else {
				}
			}
		} else {
		}
	} else {
	}

	// 更新Profiles
	if settings.Profiles == nil {
		settings.Profiles = make(map[string]config.BrowserProfile)
	}

	// 获取或创建当前类型的Profile
	currentProfile := settings.Profiles[settings.BrowserType]

	// 更新Profile字段
	currentProfile.ShortcutPath = settings.ShortcutPath
	currentProfile.CacheDir = settings.CacheDir
	currentProfile.ExecutablePath = settings.ChromePath
	currentProfile.Groups = settings.Groups
	currentProfile.CurrentGroup = settings.CurrentGroup

	// 保存回Profiles
	settings.Profiles[settings.BrowserType] = currentProfile

	syncActiveBrowserProfile(settings)

	if hotkeyProvided {
		if err := c.applySyncToggleHotkey(newHotkey); err != nil {
			return err
		}
		settings.SyncToggleHotkey = strings.TrimSpace(newHotkey)
	}

	// OpenClaw API
	if value, ok := settingsMap["EnableAPIServer"]; ok {
		if b, ok := value.(bool); ok {
			settings.EnableAPIServer = b
		}
	}
	if value, ok := settingsMap["APIPort"]; ok {
		if f, ok := value.(float64); ok {
			settings.APIPort = int(f)
		} else if i, ok := value.(int); ok {
			settings.APIPort = i
		}
	}
	if value, ok := settingsMap["APIToken"]; ok {
		if str, ok := value.(string); ok {
			settings.APIToken = str
		}
	}

	// 如果开启了 API 且令牌为空，立即生成
	if settings.EnableAPIServer && settings.APIToken == "" {
		settings.APIToken = api.GenerateToken()
	}

	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return err
	}

	if c.envCreator != nil {
		c.envCreator.CacheDir = config.GetGroupCacheDir(settings)
		c.envCreator.ShortcutDir = config.GetGroupShortcutPath(settings)
		c.envCreator.ChromePath = settings.ChromePath
		c.envCreator.BrowserType = settings.BrowserType
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	return nil
}

// MinimizeMainWindow 最小化主窗口
func (c *ChromeService) MinimizeMainWindow() error {

	if c.mainWindow == nil {
		return fmt.Errorf("main window not found")
	}

	c.mainWindow.Minimise()
	return nil
}

// MaximizeMainWindow 最大化/还原主窗口
func (c *ChromeService) MaximizeMainWindow() error {

	if c.mainWindow == nil {
		return fmt.Errorf("main window not found")
	}

	// 记录操作前的最大化状态
	wasMaximized := c.mainWindow.IsMaximised()

	c.mainWindow.ToggleMaximise()

	// 如果窗口之前是最大化的，现在被还原了，需要重新设置最小尺寸
	if wasMaximized {
		c.setWindowMinSize()
	}

	return nil
}

// ShowMainWindow 显示主窗口（智能激活版本）
func (c *ChromeService) ShowMainWindow() error {

	if c.mainWindow == nil {
		return fmt.Errorf("main window not found")
	}

	defer func() {
		if r := recover(); r != nil {
		}
	}()

	// 增强窗口激活逻辑
	// 1. 先恢复窗口（如果最小化）
	c.mainWindow.Restore()

	// 2. 显示窗口
	c.mainWindow.Show()

	// 3. 异步激活窗口（确保窗口真正前置）
	go func() {
		time.Sleep(50 * time.Millisecond) // 等待Show完成

		// 尝试聚焦窗口
		c.mainWindow.Focus()

		// 再次确保显示（防止被其他窗口遮挡）
		time.Sleep(50 * time.Millisecond)
		c.mainWindow.Show()

	}()

	return nil
}

// setWindowMinSize 设置窗口最小尺寸的辅助函数
func (c *ChromeService) setWindowMinSize() {
	if c.mainWindow == nil {
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("设置窗口最小尺寸失败: %v", r)
			}
		}()

		time.Sleep(100 * time.Millisecond) // 等待窗口状态稳定

		// 使用反射检查SetMinSize方法是否存在
		val := reflect.ValueOf(c.mainWindow)
		method := val.MethodByName("SetMinSize")

		if method.IsValid() {
			args := []reflect.Value{
				reflect.ValueOf(config.DefaultWindowMinWidth),
				reflect.ValueOf(config.DefaultWindowMinHeight),
			}
			results := method.Call(args)

			if len(results) > 0 && !results[0].IsNil() {
				if err, ok := results[0].Interface().(error); ok {
					log.Printf("设置窗口最小尺寸失败: %v", err)
				}
			}
		}
	}()
}

// MinimizeToTray 最小化应用程序到系统托盘
func (c *ChromeService) MinimizeToTray() error {

	if c.mainWindow == nil {
		return fmt.Errorf("main window not found")
	}

	defer func() {
		if r := recover(); r != nil {
		}
	}()

	// 隐藏主窗口
	c.mainWindow.Hide()

	return nil
}

// CloseApplication 关闭应用程序
func (c *ChromeService) CloseApplication() error {

	if c.app == nil {
		return fmt.Errorf("app reference is nil")
	}

	// 在应用程序退出前保存窗口位置
	if err := c.saveWindowPosition(); err != nil {
	} else {
	}

	// 清理系统托盘
	// 系统托盘会随应用退出自动清理

	c.app.Quit()
	return nil
}

// AutoArrangeWindows 自动排列选中的窗口
func (c *ChromeService) AutoArrangeWindows(windowIds []int) (*utils.WindowArrangeResult, error) {

	if len(windowIds) == 0 {
		return nil, fmt.Errorf("没有选中的窗口")
	}

	// 强制对传入的 ID 进行排序，确保物理排列顺序与编号一致
	sort.Ints(windowIds)

	var hwndList []uintptr
	for _, windowId := range windowIds {
		if windowInfo := c.syncManager.GetWindowByID(windowId); windowInfo != nil {
			hwndList = append(hwndList, windowInfo.HWND)
		}
	}

	if len(hwndList) == 0 {
		return nil, fmt.Errorf("未找到有效的窗口句柄")
	}

	// 读取用户选择的屏幕
	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	// 获取所有屏幕信息
	screens, err := c.windowArranger.GetScreensInfo()
	if err != nil {
		return nil, fmt.Errorf("获取屏幕信息失败: %v", err)
	}

	// 查找用户选择的屏幕（如果没有选择或找不到，使用主屏幕）
	screenIndex := 0
	foundSelectedScreen := false
	if settings.ScreenSelection != "" {
		for i, screen := range screens {
			if screen.Name == settings.ScreenSelection {
				screenIndex = i
				foundSelectedScreen = true
				break
			}
		}
	}

	// 如果没有找到匹配的屏幕名称，查找主屏幕
	if settings.ScreenSelection != "" && !foundSelectedScreen {
		for i, screen := range screens {
			if screen.Primary {
				screenIndex = i
				break
			}
		}
	}

	// 获取目标屏幕
	if screenIndex >= len(screens) {
		return nil, fmt.Errorf("屏幕索引超出范围")
	}
	targetScreen := screens[screenIndex]

	// 计算最佳布局
	count := len(hwndList)
	cols := int(math.Sqrt(float64(count)))
	if cols*cols < count {
		cols++
	}

	// 使用目标屏幕的工作区域计算窗口大小
	windowWidth := targetScreen.WorkWidth / cols
	windowHeight := targetScreen.WorkHeight / ((count + cols - 1) / cols)

	// 自动排列参数（使用目标屏幕的工作区域坐标）
	params := utils.WindowArrangeParams{
		StartX:            targetScreen.WorkLeft,
		StartY:            targetScreen.WorkTop,
		Width:             windowWidth,
		Height:            windowHeight,
		HorizontalSpacing: 0,
		VerticalSpacing:   0,
		WindowsPerRow:     cols,
	}

	// 执行自定义排列（不保存参数到配置文件，避免覆盖用户的自定义参数）
	return c.windowArranger.CustomArrangeWindows(hwndList, params)
}

// CustomArrangeWindows 自定义排列选中的窗口
func (c *ChromeService) CustomArrangeWindows(windowIds []int, params map[string]interface{}) (*utils.WindowArrangeResult, error) {

	if len(windowIds) == 0 {
		return nil, fmt.Errorf("没有选中的窗口")
	}

	arrangeParams := utils.WindowArrangeParams{
		StartX:            getIntParam(params, "startX", 0),
		StartY:            getIntParam(params, "startY", 0),
		Width:             getIntParam(params, "width", 500), // 默认宽度改为500
		Height:            getIntParam(params, "height", 400),
		HorizontalSpacing: getIntParam(params, "horizontalSpacing", 0),
		VerticalSpacing:   getIntParam(params, "verticalSpacing", 0),
		WindowsPerRow:     getIntParam(params, "windowsPerRow", 5),
	}

	var hwndList []uintptr
	for _, windowId := range windowIds {
		if windowInfo := c.syncManager.GetWindowByID(windowId); windowInfo != nil {
			hwndList = append(hwndList, windowInfo.HWND)
		}
	}

	if len(hwndList) == 0 {
		return nil, fmt.Errorf("未找到有效的窗口句柄")
	}

	// 执行自定义排列
	settings, loadErr := c.loadSettings()
	if loadErr != nil {
		settings = c.getDefaultSettings()
	}

	absoluteParams := arrangeParams
	if settings.ScreenSelection != "" {
		if screens, screenErr := c.windowArranger.GetScreensInfo(); screenErr == nil {
			for _, screen := range screens {
				if screen.Name == settings.ScreenSelection {
					absoluteParams.StartX += screen.WorkLeft
					absoluteParams.StartY += screen.WorkTop
					break
				}
			}
		}
	}

	result, err := c.windowArranger.CustomArrangeWindows(hwndList, absoluteParams)
	if err != nil {
		return nil, err
	}

	settings.CustomArrangeParams = config.CustomArrangeParams{
		Width:             arrangeParams.Width,
		Height:            arrangeParams.Height,
		StartX:            arrangeParams.StartX,
		StartY:            arrangeParams.StartY,
		HorizontalSpacing: arrangeParams.HorizontalSpacing,
		VerticalSpacing:   arrangeParams.VerticalSpacing,
		WindowsPerRow:     arrangeParams.WindowsPerRow,
	}

	if saveErr := c.saveSettings(settings); saveErr != nil {
	} else {
		c.settingsMux.Lock()
		c.cachedSettings = settings
		c.settingsMux.Unlock()
	}

	return result, nil
}

// GetScreensInfo 获取屏幕信息
func (c *ChromeService) GetScreensInfo() ([]utils.ScreenInfo, error) {
	return c.windowArranger.GetScreensInfo()
}

// ArrangeWindowsOnScreen 在指定屏幕上排列窗口
func (s *ChromeService) ArrangeWindowsOnScreen(windowIDs []int, screenIndex int, params utils.WindowArrangeParams) error {

	var hwndList []uintptr
	for _, id := range windowIDs {
		windowInfo := s.syncManager.GetWindowByID(id)
		if windowInfo == nil {
			continue
		}
		hwndList = append(hwndList, windowInfo.HWND)
	}

	if len(hwndList) == 0 {
		return fmt.Errorf("未找到有效的窗口句柄")
	}

	err := s.windowArranger.ArrangeWindowsOnScreen(hwndList, screenIndex, params)
	if err != nil {
		return err
	}

	// ArrangeWindowsOnScreen 函数不保存配置，因为它被自动排列使用
	// 自定义排列应该使用 CustomArrangeWindows 函数来保存配置

	return nil
}

// BatchOpenURL 批量打开网页到指定窗口 - 优化版，直接使用窗口信息
func (c *ChromeService) BatchOpenURL(url string, windowNumbers []int) error {

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 直接从SyncManager获取窗口信息，避免重新扫描进程
	var windowProcesses []utils.ChromeProcessInfo
	for _, windowNumber := range windowNumbers {
		windowInfo := c.syncManager.GetWindowByID(windowNumber)
		if windowInfo != nil && windowInfo.IsRunning {
			// 构建ChromeProcessInfo结构
			chromeProcess := utils.ChromeProcessInfo{
				PID:         windowInfo.PID,
				HWND:        windowInfo.HWND,
				Title:       windowInfo.Title,
				Number:      windowInfo.Number,
				UserDataDir: windowInfo.UserDataDir,
				DebugPort:   windowInfo.DebugPort,
			}
			windowProcesses = append(windowProcesses, chromeProcess)
		} else {
		}
	}

	if len(windowProcesses) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口，请先导入窗口")
	}

	if c.syncManager.IsRunning() {
		_ = c.syncManager.PauseSync()
		defer func() {
			time.Sleep(250 * time.Millisecond)
			_ = c.syncManager.ResumeSync()
		}()
	}

	return c.batchOpenURLOptimized(url, windowProcesses)
}

// batchOpenURLOptimized 优化的批量打开网页实现
func (c *ChromeService) batchOpenURLOptimized(url string, windowProcesses []utils.ChromeProcessInfo) error {
	if url == "" {
		return fmt.Errorf("网址不能为空")
	}

	if len(windowProcesses) == 0 {
		return fmt.Errorf("窗口列表为空")
	}

	normalizedURL := c.normalizeURL(url)

	// 并行打开网页到多个窗口
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successCount int
	var errors []string

	for _, process := range windowProcesses {
		if process.DebugPort == 0 {
			mu.Lock()
			errors = append(errors, fmt.Sprintf("窗口 %d 没有调试端口", process.Number))
			mu.Unlock()
			continue
		}

		wg.Add(1)
		go func(proc utils.ChromeProcessInfo) {
			defer wg.Done()

			err := c.openURLByDevToolsOptimized(normalizedURL, proc.DebugPort)

			mu.Lock()
			if err != nil {
				errors = append(errors, fmt.Sprintf("窗口 %d: %v", proc.Number, err))
			} else {
				successCount++
			}
			mu.Unlock()
		}(process)

		// 添加小延迟避免同时发送太多请求
		time.Sleep(50 * time.Millisecond)
	}

	// 等待所有操作完成
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("没有成功打开任何网页: %v", errors)
	}

	return nil
}

// openURLByDevToolsOptimized 优化的DevTools API调用 - 使用原生HTTP请求
func (c *ChromeService) openURLByDevToolsOptimized(url string, debugPort int) error {
	client := &http.Client{
		Timeout: 2 * time.Second, // 2秒超时
	}

	// 构建DevTools API URL
	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, url)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败 (端口 %d): %v", debugPort, err)
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败 (端口 %d): %v", debugPort, err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != 200 {
		return fmt.Errorf("DevTools API返回错误状态 %d (端口 %d)", resp.StatusCode, debugPort)
	}

	return nil
}

// normalizeURL 规范化URL
func (c *ChromeService) normalizeURL(url string) string {
	url = strings.TrimSpace(url)

	// 如果没有协议，添加https://
	if !strings.Contains(url, "://") {
		// 特殊处理一些常见情况
		if strings.HasPrefix(url, "localhost") ||
			strings.HasPrefix(url, "127.0.0.1") ||
			strings.Contains(url, ":") && !strings.Contains(url, ".") {
			url = "http://" + url
		} else {
			url = "https://" + url
		}
	}

	return url
}

// BatchOpenURLToSelectedWindows 批量打开网页到选中的窗口 - 优化版，避免重新扫描进程
func (c *ChromeService) BatchOpenURLToSelectedWindows(url string) error {

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 直接从SyncManager获取所有已导入的窗口信息，无需重新扫描进程
	allWindows := c.syncManager.GetAllImportedWindows()
	if len(allWindows) == 0 {
		return fmt.Errorf("没有已导入的窗口，请先导入窗口")
	}

	// 过滤出正在运行的窗口
	var runningWindows []int
	for _, window := range allWindows {
		if window.IsRunning {
			runningWindows = append(runningWindows, window.Number)
		}
	}

	if len(runningWindows) == 0 {
		return fmt.Errorf("没有正在运行的窗口")
	}

	// 直接调用优化后的批量打开方法
	return c.BatchOpenURL(url, runningWindows)
}

// GetPresetURLs 获取预设网址列表
func (c *ChromeService) GetPresetURLs() (map[string]string, error) {

	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	batchOps := utils.NewBatchOperations(settings)

	return batchOps.GetPresetURLs(), nil
}

// SaveCustomURL 保存自定义网址
func (c *ChromeService) SaveCustomURL(name, url string) error {

	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	batchOps := utils.NewBatchOperations(settings)

	if err := batchOps.SaveCustomURL(name, url); err != nil {
		return err
	}

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	return nil
}

// DeleteCustomURL 删除自定义网址
func (c *ChromeService) DeleteCustomURL(name string) error {

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	batchOps := utils.NewBatchOperations(settings)

	// 删除自定义网址
	if err := batchOps.DeleteCustomURL(name); err != nil {
		return err
	}

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	return nil
}

// CloseWindows safely closes selected Chrome windows.
func (s *ChromeService) CloseWindows(windowIDs []int) error {

	if len(windowIDs) == 0 {
		return nil
	}

	if s.syncManager != nil && s.syncManager.IsRunning() {
		log.Println("sync is running, stopping it before closing windows")

		if s.masterStyler != nil {
			state := s.syncManager.GetState()
			if state.MasterWindowInfo != nil {
				_ = s.masterStyler.ClearMasterWindowStyle(state.MasterWindowInfo.HWND)
			}
		}

		if err := s.syncManager.StopSync(); err != nil {
			return fmt.Errorf("failed to stop sync before closing windows: %w", err)
		}

		if s.app != nil {
			s.app.EmitEvent(syncAutoStoppedEvent)
		}

		time.Sleep(closeSyncDrainDelay)
	}

	var windowsToClose []struct {
		ID   int
		HWND uintptr
		PID  int32
	}

	for _, id := range windowIDs {
		windowInfo := s.syncManager.GetWindowByID(id)
		if windowInfo == nil {
			continue
		}

		windowsToClose = append(windowsToClose, struct {
			ID   int
			HWND uintptr
			PID  int32
		}{
			ID:   id,
			HWND: windowInfo.HWND,
			PID:  windowInfo.PID,
		})
	}

	if len(windowsToClose) == 0 {
		return nil
	}

	closeInterval := normalizeWindowCloseInterval(defaultWindowCloseInterval)
	if settings, err := s.loadSettings(); err == nil {
		closeInterval = normalizeWindowCloseInterval(settings.WindowCloseInterval)
	}

	failedCloseIDs := make([]int, 0)
	pendingCloseIDs := make([]int, 0)

	type closeResult struct {
		id      int
		failed  bool
		pending bool
		err     error
	}
	results := make(chan closeResult, len(windowsToClose))
	var closeWg sync.WaitGroup

	for index, window := range windowsToClose {
		log.Printf("safe-closing window %d (%d/%d)", window.ID, index+1, len(windowsToClose))

		closeWg.Add(1)
		go func(window struct {
			ID   int
			HWND uintptr
			PID  int32
		}) {
			defer closeWg.Done()

			err := s.provider.CloseWindowGracefully(common.WindowHandle(window.HWND), gracefulCloseTimeoutUnits)
			if err != nil {
				if strings.Contains(err.Error(), "still shutting down") {
					log.Printf("window %d is still shutting down, waiting up to %v in background", window.ID, closeWindowSettleTimeout)
					results <- closeResult{
						id:      window.ID,
						pending: s.waitForWindowClose(window.HWND, window.PID, closeWindowSettleTimeout),
						err:     err,
					}
					return
				}

				results <- closeResult{id: window.ID, failed: true, err: err}
				return
			}

			results <- closeResult{
				id:      window.ID,
				pending: s.waitForWindowClose(window.HWND, window.PID, closeWindowSettleTimeout),
			}
		}(window)

		if index < len(windowsToClose)-1 {
			time.Sleep(closeInterval)
		}
	}
	closeWg.Wait()
	close(results)

	for result := range results {
		if result.failed {
			failedCloseIDs = append(failedCloseIDs, result.id)
			log.Printf("graceful close failed for window %d: %v", result.id, result.err)
			continue
		}
		if result.pending {
			pendingCloseIDs = append(pendingCloseIDs, result.id)
			log.Printf("window %d is still closing; moving on after background wait", result.id)
		}
	}

	sort.Ints(failedCloseIDs)
	sort.Ints(pendingCloseIDs)

	closedWindowIDs := make([]int, 0, len(windowsToClose))
	stillRunningIDs := make([]int, 0)
	for _, window := range windowsToClose {
		if s.isWindowClosePending(window.HWND, window.PID) {
			stillRunningIDs = append(stillRunningIDs, window.ID)
		} else {
			closedWindowIDs = append(closedWindowIDs, window.ID)
		}
	}

	for _, id := range stillRunningIDs {
		if !slices.Contains(pendingCloseIDs, id) {
			pendingCloseIDs = append(pendingCloseIDs, id)
		}
	}
	sort.Ints(pendingCloseIDs)

	if s.syncManager != nil {
		_ = s.syncManager.RemoveImportedWindows(closedWindowIDs)

		remainingWindows := s.syncManager.GetAllImportedWindows()
		if s.app != nil {
			log.Printf("emitting windows.updated after close: %d", len(remainingWindows))
			s.app.EmitEvent(windowsUpdatedEvent, remainingWindows)
		}
	}

	isRunning := s.syncManager.IsRunning()
	if !isRunning {
		if len(pendingCloseIDs) > 0 {
			log.Printf("safe close is still in progress for windows: %v", pendingCloseIDs)
		}
		if len(failedCloseIDs) > 0 {
			return fmt.Errorf("graceful close failed: %v", failedCloseIDs)
		}
		return nil
	}

	state := s.syncManager.GetState()
	var masterWindowID int
	if state.MasterWindowInfo != nil {
		masterWindowID = state.MasterWindowInfo.Number
	}

	masterWindowClosed := false
	for _, closedID := range closedWindowIDs {
		if closedID == masterWindowID {
			masterWindowClosed = true
			break
		}
	}

	if masterWindowClosed {
		log.Println("master window closed, stopping sync")
		go func() {
			if s.masterStyler != nil {
				if mInfo := s.syncManager.GetWindowByID(masterWindowID); mInfo != nil {
					s.masterStyler.ClearMasterWindowStyle(mInfo.HWND)
				}
			}

			_ = s.syncManager.StopSync()

			if s.app != nil {
				s.app.EmitEvent(syncAutoStoppedEvent)
			}
		}()
	}

	if len(pendingCloseIDs) > 0 {
		log.Printf("safe close is still in progress for windows: %v", pendingCloseIDs)
	}
	if len(failedCloseIDs) > 0 {
		return fmt.Errorf("graceful close failed: %v", failedCloseIDs)
	}

	return nil
}

// KeepOnlyCurrentTab 仅保留当前标签页，关闭其他标签页
func (c *ChromeService) KeepOnlyCurrentTab(windowNumbers []int) error {
	if c.syncManager != nil && c.syncManager.IsRunning() {
		_ = c.syncManager.PauseSync()
		defer func() {
			time.Sleep(250 * time.Millisecond)
			_ = c.syncManager.ResumeSync()
		}()
	}
	return c.tabManager.KeepOnlyCurrentTab(windowNumbers, c)
}

// KeepOnlyNewTab 仅保留新标签页，关闭其他标签页
func (c *ChromeService) KeepOnlyNewTab(windowNumbers []int) error {
	if c.syncManager != nil && c.syncManager.IsRunning() {
		_ = c.syncManager.PauseSync()
		defer func() {
			time.Sleep(250 * time.Millisecond)
			_ = c.syncManager.ResumeSync()
		}()
	}
	browserType := config.BrowserTypeChrome
	c.settingsMux.RLock()
	if c.cachedSettings != nil && c.cachedSettings.BrowserType != "" {
		browserType = c.cachedSettings.BrowserType
	}
	c.settingsMux.RUnlock()
	return c.tabManager.KeepOnlyNewTab(windowNumbers, c, browserType)
}

// getIntParam 从参数映射中获取整数值
func getIntParam(params map[string]interface{}, key string, defaultValue int) int {
	if val, ok := params[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case float64:
			return int(v)
		case string:
			if intVal, err := fmt.Sscanf(v, "%d", new(int)); err == nil && intVal == 1 {
				var result int
				fmt.Sscanf(v, "%d", &result)
				return result
			}
		}
	}
	return defaultValue
}

// setupSystemTray 设置系统托盘
func (c *ChromeService) setupSystemTray() error {

	defer func() {
		if r := recover(); r != nil {
			c.systray = nil // 清空systray引用
		}
	}()

	c.systray = c.app.NewSystemTray()

	// 设置托盘图标（暂时跳过，避免Windows API兼容性问题）
	// 暂时注释掉图标设置，使用系统默认图标
	// } else {
	// }

	// 设置标题（Windows下SetLabel相当于SetTooltip）
	c.systray.SetLabel("ChromeManager V4.0 - Chrome窗口管理工具")

	menu := c.app.NewMenu()

	// 添加"打开程序面板"菜单项
	openItem := menu.Add("打开程序面板")
	openItem.OnClick(func(ctx *application.Context) {
		if err := c.ShowMainWindow(); err != nil {
		}
	})

	// 添加分隔符
	menu.AddSeparator()

	// 添加"退出"菜单项
	quitItem := menu.Add("退出")
	quitItem.OnClick(func(ctx *application.Context) {
		c.CloseApplication()
	})

	// 设置菜单到系统托盘
	c.systray.SetMenu(menu)

	// 添加左键单击事件处理 - 单击托盘图标显示主界面
	c.systray.OnClick(func() {
		if err := c.ShowMainWindow(); err != nil {
		}
	})

	// 添加双击事件处理 - 双击托盘图标显示主界面
	c.systray.OnDoubleClick(func() {
		if err := c.ShowMainWindow(); err != nil {
		}
	})

	// 尝试关联主窗口（可选，如果AttachWindow有问题，手动OnClick处理会起作用）
	if c.mainWindow != nil {
		// 先尝试AttachWindow，如果失败也不影响手动处理
		defer func() {
			if r := recover(); r != nil {
			}
		}()
		c.systray.AttachWindow(c.mainWindow)
	} else {
	}

	return nil
}

// cleanupPortsOnExit 在程序退出时清理端口（只在强制退出时使用）
func cleanupPortsOnExit() {

	// 给Wails一些时间正常关闭
	time.Sleep(1 * time.Second)

	if runtime.GOOS == "windows" {
		cleanupWindowsPortsOnExit()
	} else {
		// Unix/Linux/Mac系统端口清理
		ports := []string{"9245"}
		for _, port := range ports {
			cmd := exec.Command("sh", "-c", fmt.Sprintf("lsof -ti:%s | xargs -r kill -9", port))
			if err := cmd.Run(); err != nil {
			} else {
			}
		}
	}
}

// cleanupWindowsPortsOnExit 强制退出时的Windows端口清理（更温和）
func cleanupWindowsPortsOnExit() {

	// 只清理可能残留的Node.js进程，不强制清理端口
	exec.Command("taskkill", "/F", "/IM", "node.exe").Run()
	// 不清理ChromeManager.exe，因为这就是当前进程
}

// setupGracefulShutdown 设置优雅关闭信号处理
func setupGracefulShutdown() {
	c := make(chan os.Signal, 1)
	// This app is usually run with a visible console in dev builds. Pressing
	// Ctrl+C while copying console logs sends os.Interrupt and should not kill
	// the GUI process.
	signal.Ignore(os.Interrupt)
	signal.Notify(c, syscall.SIGTERM)

	go func() {
		<-c
		// 延迟一下，让Wails先正常关闭
		// time.Sleep(2 * time.Second)
		cleanupPortsOnExit()
		os.Exit(0)
	}()
}

// bringMainWindowToTop 将软件主窗口置顶
func (c *ChromeService) bringMainWindowToTop() error {
	if c.mainWindow == nil {
		return nil
	}

	// 稍微延迟一下，确保Chrome窗口排列完成
	time.Sleep(300 * time.Millisecond)

	// 正规且安全的显示方式，避免之前循环执行 SetAlwaysOnTop(true->false) 导致底层 webview2 栈崩溃
	c.mainWindow.Show()
	c.mainWindow.Focus()
	if hwnd, err := c.mainWindow.NativeWindowHandle(); err == nil && hwnd != 0 {
		if err := utils.BringWindowToTop(hwnd); err != nil {
			log.Printf("排列完成后置顶主窗口失败: %v", err)
		}
	}
	c.mainWindow.Focus()

	return nil
}

func hasSavedWindowPosition(windowConfig config.WindowConfig) bool {
	return windowConfig.X != -1 && windowConfig.Y != -1
}

func isSavedWindowPositionVisible(windowConfig config.WindowConfig, arranger *utils.WindowArranger) bool {
	if !hasSavedWindowPosition(windowConfig) {
		return false
	}
	if arranger == nil {
		return true
	}

	screens, err := arranger.GetScreensInfo()
	if err != nil || len(screens) == 0 {
		return true
	}

	width := windowConfig.Width
	if width <= 0 {
		width = config.DefaultWindowWidth
	}
	height := windowConfig.Height
	if height <= 0 {
		height = config.DefaultWindowHeight
	}

	left := windowConfig.X
	top := windowConfig.Y
	right := left + width
	bottom := top + height

	for _, screen := range screens {
		workRight := screen.WorkLeft + screen.WorkWidth
		workBottom := screen.WorkTop + screen.WorkHeight
		if right > screen.WorkLeft && left < workRight && bottom > screen.WorkTop && top < workBottom {
			return true
		}
	}

	return false
}

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {
	defer func() {
		if r := recover(); r != nil {
			showStartupError("ChromeManager V4.0 启动异常", fmt.Errorf("程序启动时发生异常：%v", r))
			os.Exit(1)
		}
	}()

	// 设置信号处理，确保程序退出时清理资源
	setupGracefulShutdown()

	if err := ensureStartupDependencies(); err != nil {
		showStartupError("ChromeManager V4.0 启动失败", err)
		os.Exit(1)
	}

	chromeService := NewChromeService()
	defer chromeService.ShutdownHotkeys()

	// 检查管理员权限
	if chromeService.provider != nil {
		chromeService.provider.CheckAdminPrivileges()
	}

	settings, err := chromeService.loadSettings()
	if err != nil {
		settings = chromeService.getDefaultSettings()
	}

	// Create a new Wails application by providing the necessary options.
	app := application.New(application.Options{
		Name:        "ChromeManager V4.0",
		Description: "Chrome window management tool",
		Services: []application.Service{
			application.NewService(chromeService),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// 将app引用传递给服务
	chromeService.app = app

	hasCustomPosition := isSavedWindowPositionVisible(settings.WindowConfig, chromeService.windowArranger)

	// Create the main window
	windowOptions := application.WebviewWindowOptions{
		Name:                "main",
		Title:               "ChromeManager V4.0",
		Width:               settings.WindowConfig.Width,
		Height:              settings.WindowConfig.Height,
		MinWidth:            config.DefaultWindowMinWidth,  // 使用配置文件的最小宽度常量
		MinHeight:           config.DefaultWindowMinHeight, // 使用配置文件的最小高度常量
		DisableResize:       false,
		Frameless:           true,
		URL:                 "/",
		DevToolsEnabled:     true,
		MinimiseButtonState: application.ButtonEnabled,
		MaximiseButtonState: application.ButtonEnabled,
		CloseButtonState:    application.ButtonEnabled,
		Hidden:              false, // 总是显示窗口，然后再设置位置
	}
	if hasCustomPosition {
		windowOptions.InitialPosition = application.WindowXY
		windowOptions.X = settings.WindowConfig.X
		windowOptions.Y = settings.WindowConfig.Y
	}

	mainWindow := app.NewWebviewWindowWithOptions(windowOptions)

	// 设置窗口最小尺寸（在窗口创建后确保生效）
	chromeService.setWindowMinSize()

	// 如果有自定义位置，设置位置
	if hasCustomPosition {
		go func() {
			time.Sleep(150 * time.Millisecond)
			mainWindow.SetPosition(settings.WindowConfig.X, settings.WindowConfig.Y)
		}()
	}

	// 如果窗口应该最大化，则在窗口显示后最大化
	if settings.WindowConfig.Maximized {
		go func() {
			time.Sleep(500 * time.Millisecond) // 等待窗口完全加载
			mainWindow.Maximise()
		}()
	}

	chromeService.mainWindow = mainWindow

	// 添加窗口关闭事件监听器来保存窗口位置
	mainWindow.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		_ = chromeService.saveWindowPosition()
	})

	// 设置系统托盘（延迟初始化避免DPI权限问题）
	go func() {
		// 等待主窗口完全加载后再初始化系统托盘
		time.Sleep(1 * time.Second)
		_ = chromeService.setupSystemTray()
	}()

	// 启动 OpenClaw API 服务器（仅在设置中开启时）
	if settings.EnableAPIServer {
		apiToken := settings.APIToken
		if apiToken == "" {
			// 首次开启：自动生成并保存 token
			apiToken = api.GenerateToken()
			settings.APIToken = apiToken
			_ = chromeService.saveSettings(settings)
		}
		apiPort := settings.APIPort
		if apiPort == 0 {
			apiPort = 18923
		}
		adapter := NewAPIAdapter(chromeService)
		apiServer := api.NewServer(adapter, apiPort, apiToken)
		if err := apiServer.Start(); err != nil {
			log.Printf("⚠️ OpenClaw API 服务器启动失败: %v", err)
		} else {
			defer apiServer.Stop()
		}
	}

	// Run the application
	err = app.Run()

	if err != nil {
		showStartupError("ChromeManager V4.0 启动失败", fmt.Errorf("程序界面启动失败：%w", err))
		os.Exit(1)
	}
}

// GetAllImportedWindows 实现WindowInfoProvider接口，适配syncmanager的WindowInfo到utils的WindowInfo
func (c *ChromeService) GetAllImportedWindows() []utils.WindowInfo {
	if c.syncManager == nil {
		return []utils.WindowInfo{}
	}

	syncManagerWindows := c.syncManager.GetAllImportedWindows()
	var utilsWindows []utils.WindowInfo

	for _, smWindow := range syncManagerWindows {
		utilsWindows = append(utilsWindows, utils.WindowInfo{
			Number:    smWindow.Number,
			Title:     smWindow.Title,
			HWND:      smWindow.HWND,
			PID:       smWindow.PID,
			DebugPort: smWindow.DebugPort,
			IsRunning: smWindow.IsRunning,
			IsMaster:  smWindow.IsMaster,
		})
	}

	return utilsWindows
}

// InputRandomNumbers 批量输入随机数字
func (c *ChromeService) InputRandomNumbers(windowNumbers []int, config utils.RandomInputConfig) error {

	if len(windowNumbers) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 验证配置
	if err := c.inputManager.ValidateRandomConfig(config); err != nil {
		return fmt.Errorf("配置无效: %v", err)
	}

	var inputTargets []utils.InputTarget
	allWindows := c.syncManager.GetAllImportedWindows()
	windowMap := make(map[int]*syncmanager.WindowInfo)

	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists && windowInfo.IsRunning {
			inputTargets = append(inputTargets, utils.InputTarget{
				HWND:      windowInfo.HWND,
				DebugPort: windowInfo.DebugPort,
			})
		} else {
		}
	}

	if len(inputTargets) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// 如果同步功能开启，在执行批量输入时挂起事件广播以避免单点触发全局复制
	if c.syncManager.IsRunning() {
		c.syncManager.PauseSync()
		defer c.syncManager.ResumeSync()
	}

	// 调用输入管理器
	return c.inputManager.InputRandomNumbersToTargets(inputTargets, config)
}

// InputTextFromFile 从文件批量输入文本
func (c *ChromeService) InputTextFromFile(windowNumbers []int, config utils.TextInputConfig) error {

	if len(windowNumbers) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	// 验证配置（如果是直接内容则跳过文件验证）
	if config.FilePath != "DIRECT_CONTENT" {
		if err := c.inputManager.ValidateTextConfig(config); err != nil {
			return fmt.Errorf("配置无效: %v", err)
		}
	}

	var inputTargets []utils.InputTarget
	allWindows := c.syncManager.GetAllImportedWindows()
	windowMap := make(map[int]*syncmanager.WindowInfo)

	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists && windowInfo.IsRunning {
			inputTargets = append(inputTargets, utils.InputTarget{
				HWND:      windowInfo.HWND,
				DebugPort: windowInfo.DebugPort,
			})
		} else {
		}
	}

	if len(inputTargets) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// 如果同步功能开启，在执行批量输入时挂起事件广播以避免单点触发全局复制
	if c.syncManager.IsRunning() {
		c.syncManager.PauseSync()
		defer c.syncManager.ResumeSync()
	}

	// 调用输入管理器
	return c.inputManager.InputTextFromFileToTargets(inputTargets, config)
}

// InputTextFromLines 从文本行数组批量输入文本
func (c *ChromeService) InputTextFromLines(windowNumbers []int, lines []string, inputMethod string, overwrite, delayed bool) error {

	if len(windowNumbers) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	if len(lines) == 0 {
		return fmt.Errorf("没有提供文本内容")
	}

	if c.syncManager == nil {
		return fmt.Errorf("sync manager not initialized")
	}

	var inputTargets []utils.InputTarget
	allWindows := c.syncManager.GetAllImportedWindows()
	windowMap := make(map[int]*syncmanager.WindowInfo)

	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists && windowInfo.IsRunning {
			inputTargets = append(inputTargets, utils.InputTarget{
				HWND:      windowInfo.HWND,
				DebugPort: windowInfo.DebugPort,
			})
		} else {
		}
	}

	if len(inputTargets) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// 如果同步功能开启，在执行批量输入时挂起事件广播以避免单点触发全局复制
	if c.syncManager.IsRunning() {
		c.syncManager.PauseSync()
		defer c.syncManager.ResumeSync()
	}

	// 调用输入管理器的直接行输入方法
	return c.inputManager.InputTextFromLinesToTargets(inputTargets, lines, inputMethod, overwrite, delayed)
}

// GetFilePreview 获取文件预览内容
func (c *ChromeService) GetFilePreview(filePath string) ([]string, error) {
	return c.inputManager.GetFilePreview(filePath)
}

// ValidateRandomInputConfig 验证随机数输入配置
func (c *ChromeService) ValidateRandomInputConfig(config utils.RandomInputConfig) error {
	return c.inputManager.ValidateRandomConfig(config)
}

// ValidateTextInputConfig 验证文本输入配置
func (c *ChromeService) ValidateTextInputConfig(config utils.TextInputConfig) error {
	return c.inputManager.ValidateTextConfig(config)
}

// SaveRandomInputConfig 保存随机输入配置到设置文件
func (c *ChromeService) SaveRandomInputConfig(utilsConfig utils.RandomInputConfig) error {

	c.settingsMux.Lock()
	defer c.settingsMux.Unlock()

	// 直接使用缓存的设置，避免死锁
	settings := c.cachedSettings
	if settings == nil {
		settings = c.getDefaultSettings()
	}

	// 转换utils.RandomInputConfig到config.RandomInputConfig
	configConfig := config.RandomInputConfig{
		MinValue:      utilsConfig.MinValue,
		MaxValue:      utilsConfig.MaxValue,
		IsFloat:       utilsConfig.IsFloat,
		DecimalPlaces: utilsConfig.DecimalPlaces,
		Overwrite:     utilsConfig.Overwrite,
		Delayed:       utilsConfig.Delayed,
	}

	settings.RandomInputConfig = configConfig

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存配置失败: %v", err)
	}

	c.cachedSettings = settings

	return nil
}

// GetRandomInputConfig 获取随机输入配置
func (c *ChromeService) GetRandomInputConfig() (*config.RandomInputConfig, error) {

	c.settingsMux.RLock()
	defer c.settingsMux.RUnlock()

	settings := c.GetSettings()

	// 如果没有保存的配置，返回默认配置
	if settings.RandomInputConfig.MinValue == 0 && settings.RandomInputConfig.MaxValue == 0 {
		defaultConfig := &config.RandomInputConfig{
			MinValue:      1,
			MaxValue:      100,
			IsFloat:       false,
			DecimalPlaces: 2,
			Overwrite:     true,
			Delayed:       false,
		}
		return defaultConfig, nil
	}

	return &settings.RandomInputConfig, nil
}

// ForceResetInputManager 强制重置输入管理器状态（紧急恢复功能）
func (c *ChromeService) ForceResetInputManager() error {

	if c.inputManager == nil {
		return fmt.Errorf("input manager not initialized")
	}

	c.inputManager.ForceReset()
	return nil
}

// GetInputManagerStatus 获取输入管理器状态
func (c *ChromeService) GetInputManagerStatus() map[string]interface{} {
	if c.inputManager == nil {
		return map[string]interface{}{
			"initialized": false,
			"active":      false,
		}
	}

	return map[string]interface{}{
		"initialized": true,
		"active":      c.inputManager.IsActive(),
	}
}

// CreateEnvironments 批量创建Chrome环境
func (c *ChromeService) CreateEnvironments(numbersStr string) error {

	if c.envCreator == nil {
		return fmt.Errorf("环境创建器未初始化")
	}

	c.settingsMux.RLock()
	currentSettings := c.cachedSettings
	c.settingsMux.RUnlock()

	groupCacheDir := config.GetGroupCacheDir(currentSettings)
	groupShortcutPath := config.GetGroupShortcutPath(currentSettings)
	currentGroup := config.GetCurrentGroup(currentSettings)

	// 输出当前配置用于调试

	// 确保环境创建器使用最新的设置
	c.envCreator.CacheDir = groupCacheDir
	c.envCreator.ShortcutDir = groupShortcutPath

	// 核心修改: 在这里解析浏览器路径
	// 如果用户设置了自定义路径，直接使用
	// 如果未设置，尝试自动查找
	resolvedBrowserPath := currentSettings.ChromePath
	if resolvedBrowserPath == "" {
		if c.provider != nil {
			foundPath, err := c.provider.FindBrowserPath(currentSettings.BrowserType)
			if err == nil {
				resolvedBrowserPath = foundPath
			}
		}
	}
	c.envCreator.ChromePath = resolvedBrowserPath
	c.envCreator.BrowserType = currentSettings.BrowserType

	// 验证目录配置
	if groupCacheDir == "" || groupShortcutPath == "" {
		return fmt.Errorf("请先在设置中配置缓存目录和快捷方式目录，当前分组: %s, 缓存目录: %s, 快捷方式目录: %s",
			currentGroup.Name, groupCacheDir, groupShortcutPath)
	}

	if err := c.envCreator.ValidateDirectories(); err != nil {
		return fmt.Errorf("目录配置验证失败: %v", err)
	}

	return c.envCreator.CreateEnvironments(numbersStr)
}

// ValidateEnvironmentConfig 验证环境配置
func (c *ChromeService) ValidateEnvironmentConfig() error {

	if c.envCreator == nil {
		return fmt.Errorf("环境创建器未初始化")
	}

	return c.envCreator.ValidateDirectories()
}

// GetEnvironmentInfo 获取环境信息
func (c *ChromeService) GetEnvironmentInfo(windowNumber int) (map[string]string, error) {

	if c.envCreator == nil {
		return nil, fmt.Errorf("环境创建器未初始化")
	}

	return c.envCreator.GetEnvironmentInfo(windowNumber)
}

// FindBrowserPath 查找浏览器安装路径
func (c *ChromeService) FindBrowserPath() (string, error) {
	if c.provider == nil {
		return "", fmt.Errorf("provider not initialized")
	}

	c.settingsMux.RLock()
	browserType := c.cachedSettings.BrowserType
	c.settingsMux.RUnlock()

	return c.provider.FindBrowserPath(browserType)
}

// TestEnvironmentCreation 测试环境创建（用于调试）
func (c *ChromeService) TestEnvironmentCreation(testNumber int) error {

	if c.envCreator == nil {
		return fmt.Errorf("环境创建器未初始化")
	}

	numbersStr := fmt.Sprintf("%d", testNumber)
	return c.envCreator.CreateEnvironments(numbersStr)
}

// GetEnvironmentCreationDebugInfo 获取环境创建调试信息
func (c *ChromeService) GetEnvironmentCreationDebugInfo() (map[string]string, error) {

	c.settingsMux.RLock()
	currentSettings := c.cachedSettings
	c.settingsMux.RUnlock()

	groupCacheDir := config.GetGroupCacheDir(currentSettings)
	groupShortcutPath := config.GetGroupShortcutPath(currentSettings)
	currentGroup := config.GetCurrentGroup(currentSettings)

	result := make(map[string]string)
	result["currentGroup"] = currentGroup.Name
	result["cacheDir"] = groupCacheDir
	result["shortcutPath"] = groupShortcutPath
	result["enableGroupMode"] = fmt.Sprintf("%v", currentSettings.EnableGroupMode)

	if c.envCreator != nil {
		result["envCreatorCacheDir"] = c.envCreator.CacheDir
		result["envCreatorShortcutDir"] = c.envCreator.ShortcutDir
	} else {
		result["envCreatorStatus"] = "未初始化"
	}

	// 检查浏览器路径
	if c.provider != nil {
		if browserPath, err := c.provider.FindBrowserPath(currentSettings.BrowserType); err != nil {
			result["browserPathError"] = err.Error()
		} else {
			result["browserPath"] = browserPath
		}
	} else {
		result["browserPathError"] = "provider not initialized"
	}

	// 检查目录是否存在
	if groupCacheDir != "" {
		if _, err := os.Stat(groupCacheDir); err != nil {
			result["cacheDirStatus"] = "不存在或不可访问: " + err.Error()
		} else {
			result["cacheDirStatus"] = "存在"
		}
	}

	if groupShortcutPath != "" {
		if _, err := os.Stat(groupShortcutPath); err != nil {
			result["shortcutDirStatus"] = "不存在或不可访问: " + err.Error()
		} else {
			result["shortcutDirStatus"] = "存在"
		}
	}

	return result, nil
}

// === 分组管理 API ===

// GetGroups 获取所有分组列表
func (c *ChromeService) GetGroups() ([]config.GroupConfig, error) {

	c.settingsMux.RLock()
	settings := c.cachedSettings
	c.settingsMux.RUnlock()

	if !settings.EnableGroupMode {
		// 如果未启用分组模式，返回从原有配置构建的默认分组
		return []config.GroupConfig{
			{
				Name:         "默认分组",
				ShortcutPath: settings.ShortcutPath,
				CacheDir:     settings.CacheDir,
			},
		}, nil
	}

	return settings.Groups, nil
}

// AddGroup 添加新分组
func (c *ChromeService) AddGroup(groupData map[string]interface{}) error {
	name, _ := groupData["name"].(string)
	shortcutPath, _ := groupData["shortcutPath"].(string)
	cacheDir, _ := groupData["cacheDir"].(string)

	if name == "" {
		return fmt.Errorf("分组名称不能为空")
	}

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	newGroup := config.GroupConfig{
		Name:         name,
		ShortcutPath: shortcutPath,
		CacheDir:     cacheDir,
	}

	if err := config.AddGroup(settings, newGroup); err != nil {
		return err
	}
	settings.EnableGroupMode = true
	if settings.CurrentGroup == "" {
		settings.CurrentGroup = newGroup.Name
	}
	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()
	c.updateEnvironmentCreatorSettings()

	return nil
}

// RemoveGroup 删除分组
func (c *ChromeService) RemoveGroup(groupName string) error {
	if groupName == "" {
		return fmt.Errorf("分组名称不能为空")
	}

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	if err := config.RemoveGroup(settings, groupName); err != nil {
		return err
	}
	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()
	c.updateEnvironmentCreatorSettings()

	return nil
}

// UpdateGroup 更新分组
func (c *ChromeService) UpdateGroup(oldName string, groupData map[string]interface{}) error {
	newName, _ := groupData["name"].(string)
	shortcutPath, _ := groupData["shortcutPath"].(string)
	cacheDir, _ := groupData["cacheDir"].(string)

	if oldName == "" || newName == "" {
		return fmt.Errorf("分组名称不能为空")
	}

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	newGroup := config.GroupConfig{
		Name:         newName,
		ShortcutPath: shortcutPath,
		CacheDir:     cacheDir,
	}

	if err := config.UpdateGroup(settings, oldName, newGroup); err != nil {
		return err
	}
	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()
	c.updateEnvironmentCreatorSettings()

	return nil
}

// SetCurrentGroup 设置当前分组
func (c *ChromeService) SetCurrentGroup(groupName string) error {
	if groupName == "" {
		return fmt.Errorf("分组名称不能为空")
	}

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	if err := config.SetCurrentGroup(settings, groupName); err != nil {
		return err
	}
	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	c.updateEnvironmentCreatorSettings()

	return nil
}

// SetGroupMode 设置分组模式开关
func (c *ChromeService) SetGroupMode(enabled bool) error {

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	settings.EnableGroupMode = enabled

	// 如果启用分组模式但没有分组，从原有配置创建默认分组
	if enabled && len(settings.Groups) == 0 {
		defaultGroup := config.GroupConfig{
			Name:         "默认分组",
			ShortcutPath: settings.ShortcutPath,
			CacheDir:     settings.CacheDir,
		}
		settings.Groups = []config.GroupConfig{defaultGroup}
		settings.CurrentGroup = "默认分组"
	}

	syncActiveBrowserProfile(settings)

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	c.updateEnvironmentCreatorSettings()

	return nil
}

// updateEnvironmentCreatorSettings 更新环境创建器设置（内部函数）
func (c *ChromeService) updateEnvironmentCreatorSettings() {
	if c.envCreator != nil {
		c.settingsMux.RLock()
		settings := c.cachedSettings
		c.settingsMux.RUnlock()

		groupCacheDir := config.GetGroupCacheDir(settings)
		groupShortcutPath := config.GetGroupShortcutPath(settings)

		c.envCreator.CacheDir = groupCacheDir
		c.envCreator.ShortcutDir = groupShortcutPath
		c.envCreator.ChromePath = settings.ChromePath
		c.envCreator.BrowserType = settings.BrowserType

	}
}

// ResetCloseBehavior 重置关闭行为设置
func (c *ChromeService) ResetCloseBehavior() error {
	c.settingsMux.Lock()
	defer c.settingsMux.Unlock()

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	settings.CloseBehavior = ""

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.cachedSettings = settings
	return nil
}

// SetCloseBehavior 设置关闭行为
func (c *ChromeService) SetCloseBehavior(behavior string) error {
	c.settingsMux.Lock()
	defer c.settingsMux.Unlock()

	settings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("加载设置失败: %v", err)
	}

	if behavior != "close" && behavior != "minimize" && behavior != "" {
		return fmt.Errorf("无效的关闭行为类型: %s", behavior)
	}

	settings.CloseBehavior = behavior

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.cachedSettings = settings
	return nil
}

// GetCloseBehavior 获取关闭行为设置
func (c *ChromeService) GetCloseBehavior() string {
	c.settingsMux.RLock()
	defer c.settingsMux.RUnlock()

	if c.cachedSettings != nil {
		return c.cachedSettings.CloseBehavior
	}

	settings, err := c.loadSettings()
	if err != nil {
		return ""
	}

	return settings.CloseBehavior
}

// ClearAllData 清理所有应用数据，删除配置文件并重启应用
func (c *ChromeService) ClearAllData() error {
	c.settingsMux.Lock()
	defer c.settingsMux.Unlock()

	// 获取配置文件路径
	configPath, err := config.GetSettingsFilePath()
	if err != nil {
		return fmt.Errorf("获取配置文件路径失败: %v", err)
	}

	// 删除配置文件
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除配置文件失败: %v", err)
	}

	// 使用 goroutine 延迟重启，让前端有时间显示成功消息
	go func() {
		time.Sleep(1 * time.Second)

		// 获取当前可执行文件路径
		exePath, err := os.Executable()
		if err != nil {
			return
		}

		// 启动新进程
		cmd := exec.Command(exePath)
		if err := cmd.Start(); err != nil {
			return
		}

		// 退出当前进程
		os.Exit(0)
	}()

	return nil
}

// RestartApplication 重启应用程序
func (c *ChromeService) RestartApplication() error {

	// 使用 goroutine 延迟重启，让前端有时间显示成功消息
	go func() {
		time.Sleep(1 * time.Second)

		// 获取当前可执行文件路径
		exePath, err := os.Executable()
		if err != nil {
			return
		}

		// 启动新进程
		cmd := exec.Command(exePath)
		if err := cmd.Start(); err != nil {
			return
		}

		// 退出当前进程
		os.Exit(0)
	}()

	return nil
}

// ExportSettings 导出配置文件
func (c *ChromeService) ExportSettings() (map[string]interface{}, error) {
	c.settingsMux.RLock()
	defer c.settingsMux.RUnlock()

	settings, err := c.loadSettings()
	if err != nil {
		return nil, fmt.Errorf("加载设置失败: %v", err)
	}

	// 转换为map以便前端处理
	result := map[string]interface{}{
		"ShortcutPath":           settings.ShortcutPath,
		"CacheDir":               settings.CacheDir,
		"ChromePath":             settings.ChromePath,
		"BrowserType":            settings.BrowserType,
		"ScreenSelection":        settings.ScreenSelection,
		"AutoModifyShortcutIcon": settings.AutoModifyShortcutIcon,
		"WindowOpenSpeed":        settings.WindowOpenSpeed,
		"WindowCloseInterval":    settings.WindowCloseInterval,
		"LastWindowNumbers":      settings.LastWindowNumbers,
		"WindowNumbersHistory":   settings.WindowNumbersHistory,
		"LastEnvCreationNumbers": settings.LastEnvCreationNumbers,
		"CustomArrangeParams":    settings.CustomArrangeParams,
		"CustomURLs":             settings.CustomURLs,
		"SyncToggleHotkey":       settings.SyncToggleHotkey,
		"WindowConfig":           settings.WindowConfig,
		"SyncConfig":             settings.SyncConfig,
		"RandomInputConfig":      settings.RandomInputConfig,
		"Groups":                 settings.Groups,
		"CurrentGroup":           settings.CurrentGroup,
		"EnableGroupMode":        settings.EnableGroupMode,
		"CloseBehavior":          settings.CloseBehavior,
		"Profiles":               settings.Profiles,
		"EnableAPIServer":        settings.EnableAPIServer,
		"APIPort":                settings.APIPort,
		"APIToken":               settings.APIToken,
	}

	return result, nil
}

// ImportSettings 导入配置文件
func (c *ChromeService) ImportSettings(settingsData map[string]interface{}) error {

	// 将 map 转换为 JSON 字节
	jsonData, err := json.Marshal(settingsData)
	if err != nil {
		return fmt.Errorf("序列化导入数据失败: %v", err)
	}

	// 反序列化为 Settings 结构体
	var settings Settings
	if err := json.Unmarshal(jsonData, &settings); err != nil {
		return fmt.Errorf("解析导入数据失败: %v", err)
	}

	settings.SyncToggleHotkey = strings.TrimSpace(settings.SyncToggleHotkey)

	if settings.BrowserType != "" {
		if settings.Profiles == nil {
			settings.Profiles = make(map[string]config.BrowserProfile)
		}
		if _, exists := settings.Profiles[settings.BrowserType]; !exists {
			settings.Profiles[settings.BrowserType] = config.BrowserProfile{
				ShortcutPath:   settings.ShortcutPath,
				CacheDir:       settings.CacheDir,
				ExecutablePath: settings.ChromePath,
				Groups:         settings.Groups,
				CurrentGroup:   settings.CurrentGroup,
			}
		}
	}

	// 保存设置
	if err := c.saveSettings(&settings); err != nil {
		return fmt.Errorf("保存导入的设置失败: %v", err)
	}

	normalizedSettings, err := c.loadSettings()
	if err != nil {
		return fmt.Errorf("归一化导入设置失败: %v", err)
	}
	if err := c.saveSettings(normalizedSettings); err != nil {
		return fmt.Errorf("保存归一化设置失败: %v", err)
	}

	// 更新缓存设置（需要加锁）
	c.settingsMux.Lock()
	c.cachedSettings = normalizedSettings
	c.settingsMux.Unlock()

	// 在锁外调用 updateEnvironmentCreatorSettings，避免死锁
	// 因为 updateEnvironmentCreatorSettings 内部会尝试获取读锁
	c.updateEnvironmentCreatorSettings()

	return nil
}

// SelectFile 打开文件选择对话框
func (c *ChromeService) SelectFile(title, filter string) (string, error) {
	if c.app == nil {
		return "", fmt.Errorf("app not initialized")
	}

	dialog := application.OpenFileDialog()
	dialog.CanChooseDirectories(false)
	dialog.CanChooseFiles(true)
	dialog.SetTitle(title)

	if filter != "" {
		parts := strings.Split(filter, "|")
		if len(parts) == 2 {
			dialog.AddFilter(parts[0], parts[1])
		}
	}

	filePath, err := dialog.PromptForSingleSelection()
	if err != nil {
		return "", fmt.Errorf("文件选择失败: %v", err)
	}
	if filePath == "" {
		return "", fmt.Errorf("user cancelled file selection")
	}

	return filePath, nil
}

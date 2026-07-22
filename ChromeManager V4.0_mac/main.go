package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"chromemanager/config"
	"chromemanager/platform"
	"chromemanager/platform/common"
	"chromemanager/syncmanager"
	"chromemanager/utils"
	"embed"

	"github.com/pkg/browser"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIconData []byte

//go:embed build/chrome.png
var chromeIconData []byte

type Settings = config.Settings

type WindowConfig = config.WindowConfig

// ChromeService 提供Chrome窗口管理功能
type ChromeService struct {
	app            *application.App
	systray        *application.SystemTray
	mainWindow     *application.WebviewWindow
	syncManager    *syncmanager.SyncManager
	settingsMux    sync.RWMutex
	cachedSettings *config.Settings

	// API Server and Selected Windows State
	apiServer          *APIServer
	selectedWindows    []int
	selectedWindowsMux sync.RWMutex

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
)

const (
	gracefulCloseTimeoutUnits  = 30
	closeSyncDrainDelay        = 350 * time.Millisecond
	defaultWindowCloseInterval = 100 * time.Millisecond
	closeWindowSettleTimeout   = 2 * time.Second
	closeWindowSettlePoll      = 200 * time.Millisecond
)

// NewChromeService 创建新的ChromeService实例
func NewChromeService() (*ChromeService, error) {
	// 初始化平台抽象层 Provider
	provider, err := platform.NewProvider()
	if err != nil {
		return nil, err
	}

	service := &ChromeService{
		provider:        provider,
		windowArranger:  utils.NewWindowArranger(provider),
		tabManager:      utils.NewTabManager(),
		inputManager:    utils.NewInputManager(),
		selectedWindows: make([]int, 0),
	}

	service.apiServer = NewAPIServer(service)

	if settings, err := service.loadSettings(); err == nil {
		service.cachedSettings = settings

		// 迁移旧的绝对坐标到相对坐标
		if settings.CustomArrangeParams.StartX != 0 || settings.CustomArrangeParams.StartY != 0 {
			// 初始化 windowArranger 以便获取屏幕信息
			if service.windowArranger == nil {
				service.windowArranger = utils.NewWindowArranger(service.provider)
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
	service.envCreator = utils.NewEnvironmentCreator(service.cachedSettings.CacheDir, service.cachedSettings.ShortcutPath, service.cachedSettings.ChromePath, service.cachedSettings.AutoModifyShortcutIcon)

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

	return service, nil
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

	c.app.Event.Emit(syncToggleHotkeyEvent)
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

// ensureAppIcon 返回应用图标路径。正式 App 使用只读 bundle 资源，开发运行使用用户缓存。
func ensureAppIcon() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("获取程序路径失败: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	if filepath.Base(exeDir) == "MacOS" && filepath.Base(filepath.Dir(exeDir)) == "Contents" {
		iconPath := filepath.Join(filepath.Dir(exeDir), "Resources", "icons", "appicon.png")
		if _, err := os.Stat(iconPath); err != nil {
			return "", fmt.Errorf("应用包图标资源不可用: %w", err)
		}
		return iconPath, nil
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("获取用户缓存目录失败: %w", err)
	}
	iconsDir := filepath.Join(cacheDir, "ChromeManager", "icons")
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		return "", fmt.Errorf("创建 icons 目录失败: %w", err)
	}

	iconPath := filepath.Join(iconsDir, "appicon.png")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		if err := os.WriteFile(iconPath, appIconData, 0644); err != nil {
			return "", fmt.Errorf("写入图标文件失败: %w", err)
		}
	}

	chromePath := filepath.Join(iconsDir, "chrome.png")
	if _, err := os.Stat(chromePath); os.IsNotExist(err) {
		if err := os.WriteFile(chromePath, chromeIconData, 0644); err != nil {
			log.Printf("⚠️ 警告: 写入 Chrome 模板图标失败: %v", err)
		}
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
		AutoModifyShortcutIcon: false,
		WindowOpenSpeed:        0.1,
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
		CurrentGroup:    "", // 空字符串表示使用全局默认路径
		Groups:          []config.GroupConfig{},
	}
}

// loadSettings 从文件加载设置
func (c *ChromeService) loadSettings() (*Settings, error) {
	configPath := c.getConfigPath()

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if legacyPath, legacyErr := config.GetLegacySettingsFilePath(); legacyErr == nil && legacyPath != "" && legacyPath != configPath {
			if legacyData, readErr := os.ReadFile(legacyPath); readErr == nil {
				if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(configPath, legacyData, 0644); err != nil {
					return nil, err
				}
			}
		}
	}

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		settings := c.getDefaultSettings()
		_ = c.saveSettings(settings)
		return settings, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	// 先加载默认设置，确保所有字段都有合理的初始值
	defaultSettings := c.getDefaultSettings()
	var settings Settings = *defaultSettings

	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	if settings.WindowConfig.Width == 0 {
		settings.WindowConfig.Width = config.DefaultWindowWidth
		settings.WindowConfig.Height = config.DefaultWindowHeight
	}

	if settings.SyncConfig.WheelEventThreshold == 0 {
		defaultSyncConfig := config.GetDefaultSettings().SyncConfig
		settings.SyncConfig = defaultSyncConfig
	}

	// 移除对 0,0 坐标的重置逻辑，允许窗口在屏幕左上角
	// 只有当坐标显式为 -1 时才视为未设置
	if settings.WindowConfig.X == -1 && settings.WindowConfig.Y == -1 {
		// 确实未设置，保持为 -1
	}
	// Default values if needed
	if len(settings.Groups) == 0 {
		settings.Groups = []config.GroupConfig{
			{
				Name:         "",
				ShortcutPath: settings.ShortcutPath,
				CacheDir:     settings.CacheDir,
			},
		}
	}

	return &settings, nil
}

// saveSettings 保存设置到文件
func (c *ChromeService) saveSettings(settings *Settings) error {
	configPath := c.getConfigPath()

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
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
		return nil
	}

	// 使用 Native CGO 获取准确的窗口位置 (逻辑坐标)
	// 这避免了 Wails v3 在 macOS 上 GetPosition 返回物理像素 SetPosition 需要逻辑点的问题
	// 也避免了 manually applying scale correction 的不确定性
	var x, y, width, height int
	var isMaximized bool

	// 尝试使用 Native 方法 (通过类型断言检查是否支持 AppWindowProvider)
	if c.provider != nil {
		if appProv, ok := c.provider.(common.AppWindowProvider); ok {
			// Use PID-based lookup (pass 0 as handle is ignored by provider implementation)
			nativeRect, err := appProv.GetAppWindowPosition(0)
			if err == nil {
				x, y, width, height = nativeRect.Left, nativeRect.Top, nativeRect.Width, nativeRect.Height
			} else {
				// Native method failed
				x, y = c.mainWindow.Position()
				width, height = c.mainWindow.Size()
			}
		} else {
			// Provider does not support AppWindowProvider (e.g. Windows currently)
			x, y = c.mainWindow.Position()
			width, height = c.mainWindow.Size()
		}
	} else {
		// Fallback if provider null
		x, y = c.mainWindow.Position()
		width, height = c.mainWindow.Size()
	}

	isMaximized = c.mainWindow.IsMaximised()

	// 如果窗口最小化了或者隐藏了，不要保存位置
	if width <= 0 || height <= 0 {
		return nil
	}

	settings.WindowConfig.X = x
	settings.WindowConfig.Y = y
	settings.WindowConfig.Width = width
	settings.WindowConfig.Height = height
	settings.WindowConfig.Maximized = isMaximized

	err = c.saveSettings(settings)
	if err != nil {
		log.Printf("[WindowPos] [Save] ERROR Saving: %v", err)
		return err
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

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
	var failedReasons []string

	for _, number := range numbers {
		shortcut, exists := shortcutMap[number]
		if !exists {
			notFoundWindows = append(notFoundWindows, number)
			continue
		}

		windowInfo, err := c.launchChromeFromShortcut(shortcut, settings)
		if err != nil {
			failedWindows = append(failedWindows, number)
			failedReasons = append(failedReasons, fmt.Sprintf("%d: %v", number, err))
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
		if len(failedWindows) > 0 {
			if errMsg != "" {
				errMsg += "; "
			}
			errMsg += fmt.Sprintf("启动失败的窗口: %v", failedWindows)
			if len(failedReasons) > 0 {
				errMsg += fmt.Sprintf("。失败原因: %s", strings.Join(failedReasons, "；"))
			}
		}
		return fmt.Errorf("%s", errMsg)
	} else if len(notFoundWindows) > 0 || len(failedWindows) > 0 {
		// 如果部分窗口打开成功，部分失败，返回包含详细信息的错误
		var warnMsg string
		warnMsg = fmt.Sprintf("成功打开 %d 个窗口", len(openedWindows))
		if len(notFoundWindows) > 0 {
			warnMsg += fmt.Sprintf("，未找到以下窗口: %v", notFoundWindows)
		}
		if len(failedWindows) > 0 {
			warnMsg += fmt.Sprintf("，启动失败: %v", failedWindows)
			if len(failedReasons) > 0 {
				warnMsg += fmt.Sprintf("。失败原因: %s", strings.Join(failedReasons, "；"))
			}
		}
		return fmt.Errorf("%s", warnMsg)
	}

	return nil
}

// launchChromeFromShortcut 从快捷方式启动Chrome窗口
func (c *ChromeService) launchChromeFromShortcut(shortcut common.ShortcutInfo, settings *Settings) (*syncmanager.WindowInfo, error) {
	// 分配调试端口
	debugPort := config.BaseDebugPort + shortcut.Number

	if runtime.GOOS == "darwin" {
		return c.launchChromeFromShortcutDarwin(shortcut, settings, debugPort)
	}

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

	// 直接启动Chrome，不使用快捷方式

	args, err := parseArguments(newArgs)
	if err != nil {
		return nil, fmt.Errorf("failed to parse arguments: %v", err)
	}

	// 直接启动Chrome进程
	cmd := exec.Command(shortcut.TargetPath, args...)
	cmd.Dir = shortcut.WorkingDir // 设置工作目录

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Chrome: %v", err)
	}

	// 等待进程启动
	time.Sleep(time.Duration(settings.WindowOpenSpeed*1000) * time.Millisecond)

	windowInfo := &syncmanager.WindowInfo{
		Number:      shortcut.Number,
		DebugPort:   debugPort,
		UserDataDir: userDataDir,
	}

	return windowInfo, nil
}

func (c *ChromeService) launchChromeFromShortcutDarwin(shortcut common.ShortcutInfo, settings *Settings, debugPort int) (*syncmanager.WindowInfo, error) {
	chromeAppPath, err := resolveDarwinChromeAppPath(settings.ChromePath, shortcut)
	if err != nil {
		return nil, err
	}

	userDataDir, err := resolveDarwinShortcutUserDataDir(shortcut, settings)
	if err != nil {
		return nil, err
	}
	if err := ensureDarwinTranslateDisabled(userDataDir); err != nil {
		log.Printf("failed to disable Chrome translate for %s: %v", userDataDir, err)
	}
	c.cleanupStaleChromeSingletonFiles(userDataDir)

	args := []string{
		"--user-data-dir=" + userDataDir,
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--remote-allow-origins=*",
	}
	if zoomExtensionDir, err := ensureDarwinZoomExtension(); err == nil {
		args = appendDarwinLoadExtensionArg(args, zoomExtensionDir)
	} else {
		log.Printf("failed to prepare ChromeManager zoom helper extension: %v", err)
	}
	for _, defaultArg := range config.ChromeDefaultArgs {
		if !containsStringArg(args, defaultArg) {
			args = append(args, defaultArg)
		}
	}

	cmdArgs := append([]string{"-n", "-a", chromeAppPath, "--args"}, args...)
	cmd := exec.Command("open", cmdArgs...)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start Chrome: %v", err)
	}
	_ = cmd.Process.Release()

	time.Sleep(time.Duration(settings.WindowOpenSpeed*1000) * time.Millisecond)

	return &syncmanager.WindowInfo{
		Number:      shortcut.Number,
		DebugPort:   debugPort,
		UserDataDir: userDataDir,
	}, nil
}

func ensureDarwinTranslateDisabled(userDataDir string) error {
	if userDataDir == "" {
		return nil
	}

	defaultProfileDir := filepath.Join(userDataDir, "Default")
	if err := os.MkdirAll(defaultProfileDir, 0755); err != nil {
		return err
	}

	prefsPath := filepath.Join(defaultProfileDir, "Preferences")
	prefs := make(map[string]interface{})
	if data, err := os.ReadFile(prefsPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &prefs); err != nil {
			return fmt.Errorf("read Chrome Preferences: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	translatePrefs, _ := prefs["translate"].(map[string]interface{})
	if translatePrefs == nil {
		translatePrefs = make(map[string]interface{})
	}
	translatePrefs["enabled"] = false
	prefs["translate"] = translatePrefs

	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	tmpPath := prefsPath + ".chromemanager-tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, prefsPath)
}

func ensureDarwinZoomExtension() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		if err == nil {
			err = fmt.Errorf("empty user cache dir")
		}
		return "", fmt.Errorf("resolve user cache dir: %w", err)
	}

	extensionDir := filepath.Join(cacheDir, "ChromeManager", "zoom-extension")
	if err := os.MkdirAll(extensionDir, 0755); err != nil {
		return "", err
	}

	files := map[string]string{
		"manifest.json": fmt.Sprintf(`{
  "manifest_version": 3,
	  "name": "ChromeManager Browser Controller",
	  "version": "1.1.0",
	  "description": "Local controller for ChromeManager tab and native zoom sync.",
  "key": %q,
  "permissions": ["tabs", "debugger"],
  "background": {
    "service_worker": "chromemanager_zoom_worker.js"
  }
}
`, config.ChromeManagerZoomExtensionKey),
		"chromemanager_zoom_worker.js": `self.chromeManagerZoomWorkerReady = true;
chrome.runtime.onInstalled.addListener(() => {});
chrome.runtime.onStartup.addListener(() => {});
`,
		"chromemanager_zoom_controller.html": `<!doctype html>
<html>
<head><meta charset="utf-8"><title>ChromeManager Browser Controller</title></head>
<body><script src="chromemanager_zoom_controller.js"></script></body>
</html>
`,
		"chromemanager_zoom_controller.js": `window.chromeManagerZoomControllerReady = true;
`,
	}

	for name, content := range files {
		path := filepath.Join(extensionDir, name)
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return "", err
		}
	}
	return extensionDir, nil
}

func appendDarwinLoadExtensionArg(args []string, extensionDir string) []string {
	if extensionDir == "" {
		return args
	}
	loadArgPrefix := "--load-extension="
	for i, arg := range args {
		if !strings.HasPrefix(arg, loadArgPrefix) {
			continue
		}
		existing := strings.TrimPrefix(arg, loadArgPrefix)
		parts := strings.Split(existing, ",")
		for _, part := range parts {
			if filepath.Clean(strings.TrimSpace(part)) == filepath.Clean(extensionDir) {
				return args
			}
		}
		parts = append(parts, extensionDir)
		args[i] = loadArgPrefix + strings.Join(parts, ",")
		return args
	}
	return append(args, loadArgPrefix+extensionDir)
}

func resolveDarwinShortcutUserDataDir(shortcut common.ShortcutInfo, settings *Settings) (string, error) {
	cacheDir := config.GetGroupCacheDir(settings)
	if cacheDir == "" {
		return "", fmt.Errorf("缓存目录未配置，请先在设置中选择缓存目录")
	}

	if script, err := os.ReadFile(shortcut.TargetPath); err == nil {
		if userDataDir := extractUserDataDirFromArgs(string(script)); userDataDir != "" {
			if _, err := os.Stat(userDataDir); err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("窗口 %d 的 .app 指向的缓存目录不存在: %s。当前设置的缓存目录是: %s。请重新批量创建该窗口以覆盖旧 .app", shortcut.Number, userDataDir, cacheDir)
				}
				return "", fmt.Errorf("窗口 %d 的缓存目录不可访问: %s: %v", shortcut.Number, userDataDir, err)
			}
			if !isPathInsideDir(userDataDir, cacheDir) {
				return "", fmt.Errorf("窗口 %d 的 .app 缓存目录与当前设置不一致: %s。当前设置的缓存目录是: %s。请重新批量创建该窗口以覆盖旧 .app", shortcut.Number, userDataDir, cacheDir)
			}
			return userDataDir, nil
		}
	}

	fallback := filepath.Join(cacheDir, fmt.Sprintf("%d", shortcut.Number))
	if _, err := os.Stat(fallback); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("窗口 %d 缺少缓存目录: %s。请重新批量创建该窗口", shortcut.Number, fallback)
		}
		return "", fmt.Errorf("窗口 %d 的缓存目录不可访问: %s: %v", shortcut.Number, fallback, err)
	}
	return fallback, nil
}

func resolveDarwinChromeAppPath(chromePath string, shortcut common.ShortcutInfo) (string, error) {
	if script, err := os.ReadFile(shortcut.TargetPath); err == nil {
		if appPath := extractDarwinChromeAppPathFromScript(string(script)); appPath != "" {
			if info, err := os.Stat(appPath); err == nil && info.IsDir() {
				return appPath, nil
			} else {
				return "", fmt.Errorf("窗口 %d 的 .app 指向的 Chrome 不存在: %s。请在设置中重新选择 Chrome 路径，或重新批量创建该窗口以覆盖旧 .app", shortcut.Number, appPath)
			}
		}
	}

	executable, err := resolveDarwinChromeExecutable(chromePath)
	if err != nil {
		return "", err
	}
	appPath := darwinChromeExecutableToAppPath(executable)
	if appPath == "" {
		return "", fmt.Errorf("未找到 Google Chrome.app，请在设置中重新选择 Chrome 路径")
	}
	return appPath, nil
}

func extractDarwinChromeAppPathFromScript(script string) string {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`open\s+-n\s+-a\s+"([^"]+\.app)"`),
		regexp.MustCompile(`open\s+-n\s+-a\s+'([^']+\.app)'`),
	}
	for _, pattern := range patterns {
		if matches := pattern.FindStringSubmatch(script); len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}

func darwinChromeExecutableToAppPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasSuffix(path, ".app") {
		return path
	}
	if idx := strings.Index(path, ".app/"); idx >= 0 {
		return path[:idx+len(".app")]
	}
	return ""
}

func containsStringArg(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
}

func isPathInsideDir(path string, dir string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func (c *ChromeService) cleanupStaleChromeSingletonFiles(userDataDir string) {
	lockPath := filepath.Join(userDataDir, "SingletonLock")
	target, err := os.Readlink(lockPath)
	if err != nil {
		return
	}

	dash := strings.LastIndex(target, "-")
	if dash < 0 || dash == len(target)-1 {
		return
	}

	pidValue, err := strconv.Atoi(target[dash+1:])
	if err != nil || pidValue <= 0 {
		return
	}

	if c.provider != nil && c.provider.IsProcessRunning(int32(pidValue)) {
		return
	}

	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(userDataDir, name))
	}
	log.Printf("removed stale Chrome singleton files for %s (dead pid %d)", userDataDir, pidValue)
}

func resolveDarwinChromeExecutable(chromePath string) (string, error) {
	candidates := []string{}
	if chromePath != "" {
		candidates = append(candidates, chromePath)
	}
	if found, err := utils.FindChromePath(); err == nil && found != "" {
		candidates = append(candidates, found)
	}

	for _, candidate := range candidates {
		executable := normalizeDarwinChromeExecutable(candidate)
		if executable == "" {
			continue
		}
		if info, err := os.Stat(executable); err == nil && !info.IsDir() {
			return executable, nil
		}
	}

	return "", fmt.Errorf("未找到 Google Chrome 浏览器，请在设置中重新选择 Chrome 路径")
}

func normalizeDarwinChromeExecutable(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasSuffix(path, ".app") {
		return filepath.Join(path, "Contents", "MacOS", "Google Chrome")
	}
	if strings.Contains(path, ".app/") {
		return path
	}
	return path
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

// ImportWindows 导入现有Chrome窗口
func (c *ChromeService) ImportWindows() ([]syncmanager.WindowInfo, error) {

	chromeWindows, err := c.provider.EnumChromeWindows()
	if err != nil {
		return nil, fmt.Errorf("failed to find Chrome windows: %v", err)
	}

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

	return windowInfos, nil
}

// SetMasterWindow 设置主控窗口（公共接口）
func (c *ChromeService) SetMasterWindow(windowNumber int) error {
	return c.setMasterWindowInternal(windowNumber, true)
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

func (c *ChromeService) isWindowClosePending(hwnd uintptr, pid int32) bool {
	if pid != 0 {
		return c.provider.IsProcessRunning(pid)
	}

	if hwnd != 0 {
		if valid, _ := c.provider.IsWindowValid(common.WindowHandle(hwnd)); valid {
			return true
		}
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

func containsWindowID(ids []int, id int) bool {
	for _, current := range ids {
		if current == id {
			return true
		}
	}
	return false
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
	if len(actualSlaveNumbers) == 0 {
		return fmt.Errorf("无法开始同步: 至少需要一个从控窗口。请至少选择两个窗口，或先指定主控窗口后再选择从控窗口")
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

		// 收集从属窗口句柄 (Use PID for SyncManager, as it needs Process ID for events/CDP)
		var slaveHandles []common.WindowHandle
		for _, slaveNumber := range actualSlaveNumbers {
			slaveWindow := c.syncManager.GetWindowByID(slaveNumber)
			if slaveWindow != nil {
				slaveHandles = append(slaveHandles, common.WindowHandle(slaveWindow.PID))
			}
		}

		// 设置主控窗口样式
		if err := c.setMasterWindowInternal(actualMasterNumber, false); err != nil {
			log.Printf("设置主控窗口失败: %v", err)
			if c.app != nil {
				c.app.Event.Emit(syncStartFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// Set Progress Callback for Playwright Installation (Darwin only)
		if runtime.GOOS == "darwin" {
			// c.syncManager is *syncmanager.SyncManager which has SetProgressCallback wrapper
			c.syncManager.SetProgressCallback(func(msg string) {
				if c.app != nil {
					c.app.Event.Emit("sync:installing_progress", map[string]string{"message": msg})
					log.Printf("[SyncProgress] %s", msg)
				}
			})
		}

		// 启动同步 (Use PID for Master too)
		if err := c.syncManager.Start(common.WindowHandle(masterWindow.PID), slaveHandles); err != nil {
			log.Printf("启动同步失败: %v", err)
			if c.app != nil {
				c.app.Event.Emit(syncStartFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// 发送成功事件
		if c.app != nil {
			c.app.Event.Emit(syncStartedEvent, map[string]interface{}{
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
				c.app.Event.Emit(syncStopFailedEvent, map[string]interface{}{
					"error": err.Error(),
				})
			}
			return
		}

		// 发送成功事件
		if c.app != nil {
			c.app.Event.Emit(syncStoppedEvent)
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

	var windowInfos []syncmanager.WindowInfo
	for _, chromeWindow := range chromeWindows {
		windowInfo := syncmanager.WindowInfo{
			Number:      chromeWindow.Number,
			PID:         chromeWindow.ProcessID,
			HWND:        chromeWindow.HWND,
			Title:       chromeWindow.Title,
			DebugPort:   chromeWindow.DebugPort,
			UserDataDir: chromeWindow.UserDataDir,
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

	result, err := c.app.Dialog.OpenFile().
		SetTitle("请选择文件夹").
		CanChooseDirectories(true).
		CanChooseFiles(false).
		PromptForSingleSelection()

	if err != nil {
		return "", fmt.Errorf("文件夹选择失败: %v", err)
	}

	return result, nil
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
				IsMaster:    false,
				IsRunning:   true,
				ClientRect: syncmanager.Rect{
					Left:   wp.Position.Left,
					Top:    wp.Position.Top,
					Right:  wp.Position.Left + wp.Position.Width,
					Bottom: wp.Position.Top + wp.Position.Height,
				},
				WindowRect: syncmanager.Rect{
					Left:   wp.Position.Left,
					Top:    wp.Position.Top,
					Right:  wp.Position.Left + wp.Position.Width,
					Bottom: wp.Position.Top + wp.Position.Height,
				},
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

	if c.syncManager != nil {
		_ = c.syncManager.NotifyWindowsImported(windowInfos)
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
		"ScreenSelection":        settings.ScreenSelection,
		"AutoModifyShortcutIcon": settings.AutoModifyShortcutIcon,
		"WindowOpenSpeed":        settings.WindowOpenSpeed,
		"LastWindowNumbers":      settings.LastWindowNumbers,
		"LastEnvCreationNumbers": settings.LastEnvCreationNumbers, // 添加环境创建编号历史
		"CustomArrangeParams":    settings.CustomArrangeParams,    // 添加自定义排列参数
		"CustomURLs":             settings.CustomURLs,
		"SyncToggleHotkey":       settings.SyncToggleHotkey,
		"EnableAPI":              settings.EnableAPI,
		"APIPort":                settings.APIPort,
		"APIToken":               settings.APIToken,
		"WindowConfig":           filteredWindowConfig, // 使用过滤后的配置
		// 分组相关字段
		"EnableGroupMode": settings.EnableGroupMode,
		"Groups":          settings.Groups,
		"CurrentGroup":    settings.CurrentGroup,
	}, nil
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
	if value, ok := settingsMap["ScreenSelection"]; ok {
		if str, ok := value.(string); ok {
			settings.ScreenSelection = str
		}
	}
	if value, ok := settingsMap["AutoModifyShortcutIcon"]; ok {
		if b, ok := value.(bool); ok {
			settings.AutoModifyShortcutIcon = b
			if c.envCreator != nil {
				c.envCreator.AutoModifyShortcutIcon = b
			}
		}
	}
	if value, ok := settingsMap["WindowOpenSpeed"]; ok {
		if f, ok := value.(float64); ok {
			settings.WindowOpenSpeed = f
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

	if value, ok := settingsMap["EnableAPI"]; ok {
		if b, ok := value.(bool); ok {
			settings.EnableAPI = b
		}
	}
	if value, ok := settingsMap["APIPort"]; ok {
		// Float64 is commonly unmarshaled from JSON for numbers
		if num, ok := value.(float64); ok {
			settings.APIPort = int(num)
		} else if num, ok := value.(int); ok {
			settings.APIPort = num
		}
	}
	if value, ok := settingsMap["APIToken"]; ok {
		if str, ok := value.(string); ok {
			settings.APIToken = str
		}
	}
	if settings.EnableAPI && (settings.APIPort < 1024 || settings.APIPort > 65535) {
		return fmt.Errorf("API 端口必须在 1024 到 65535 之间")
	}
	if settings.EnableAPI && strings.TrimSpace(settings.APIToken) == "" {
		settings.APIToken = generateAPIToken()
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
				}
			}
		}
	}

	if hotkeyProvided {
		if err := c.applySyncToggleHotkey(newHotkey); err != nil {
			return err
		}
		settings.SyncToggleHotkey = strings.TrimSpace(newHotkey)
	}

	if err := c.saveSettings(settings); err != nil {
		return err
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

	// 使用分组感知的方法更新环境创建器路径，而不是直接使用全局settings.CacheDir
	c.updateEnvironmentCreatorSettings()

	// 重新启动 API 服务（如果状态或端口变更会自动重载）
	if c.apiServer != nil {
		if err := c.apiServer.Restart(context.Background()); err != nil {
			return err
		}
	}

	return nil
}

// UpdateSelectedWindows is called by the frontend to sync which windows are checked.
func (c *ChromeService) UpdateSelectedWindows(numbers []int) {
	c.selectedWindowsMux.Lock()
	defer c.selectedWindowsMux.Unlock()
	c.selectedWindows = numbers
}

// MinimizeMainWindow 最小化主窗口
func (c *ChromeService) MinimizeMainWindow() error {

	if c.mainWindow == nil {
		return fmt.Errorf("main window not found")
	}

	// Try Native Minimize (via Interface Casting)
	type NativeMinimizer interface {
		NativeMinimize()
	}
	if c.provider != nil {
		if minimizer, ok := c.provider.(NativeMinimizer); ok {
			minimizer.NativeMinimize()
			return nil
		}
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
				if _, ok := results[0].Interface().(error); ok {
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

	// Try Native Terminate (via Interface Casting)
	type NativeTerminator interface {
		NativeTerminate()
	}
	if c.provider != nil {
		if terminator, ok := c.provider.(NativeTerminator); ok {
			terminator.NativeTerminate()
			return nil
		}
	}

	c.app.Quit()
	return nil
}

// AutoArrangeWindows 自动排列选中的窗口
func (c *ChromeService) AutoArrangeWindows(windowIds []int) error {

	if len(windowIds) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	var hwndList []uintptr
	for _, windowId := range windowIds {
		if windowInfo := c.syncManager.GetWindowByID(windowId); windowInfo != nil {
			hwndList = append(hwndList, windowInfo.HWND)
		}
	}

	if len(hwndList) == 0 {
		return fmt.Errorf("未找到有效的窗口句柄")
	}

	// 读取用户选择的屏幕
	settings, err := c.loadSettings()
	if err != nil {
		settings = c.getDefaultSettings()
	}

	// 获取所有屏幕信息
	screens, err := c.windowArranger.GetScreensInfo()
	if err != nil {
		return fmt.Errorf("获取屏幕信息失败: %v", err)
	}

	// 查找用户选择的屏幕（如果没有选择或找不到，使用主屏幕）
	screenIndex := 0
	if settings.ScreenSelection != "" {
		for i, screen := range screens {
			if screen.Name == settings.ScreenSelection {
				screenIndex = i
				break
			}
		}
	}

	// 如果没有找到匹配的屏幕名称，查找主屏幕
	if screenIndex == 0 && settings.ScreenSelection != "" {
		for i, screen := range screens {
			if screen.Primary {
				screenIndex = i
				break
			}
		}
	}

	// 获取目标屏幕
	if screenIndex >= len(screens) {
		return fmt.Errorf("屏幕索引超出范围")
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
func (c *ChromeService) CustomArrangeWindows(windowIds []int, params map[string]interface{}) error {

	if len(windowIds) == 0 {
		return fmt.Errorf("没有选中的窗口")
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
		return fmt.Errorf("未找到有效的窗口句柄")
	}

	// 执行自定义排列
	err := c.windowArranger.CustomArrangeWindows(hwndList, arrangeParams)
	if err != nil {
		return err
	}

	// 保存参数时需要转换为相对坐标
	settings, loadErr := c.loadSettings()
	if loadErr != nil {
		settings = c.getDefaultSettings()
	}

	// 获取当前选择的屏幕信息，计算相对坐标
	screens, screenErr := c.windowArranger.GetScreensInfo()
	relativeStartX := arrangeParams.StartX
	relativeStartY := arrangeParams.StartY

	if screenErr == nil && len(screens) > 0 {
		// 查找用户选择的屏幕
		screenIndex := 0
		if settings.ScreenSelection != "" {
			for i, screen := range screens {
				if screen.Name == settings.ScreenSelection {
					screenIndex = i
					break
				}
			}
		}

		// 如果找到了屏幕，将绝对坐标转换为相对坐标
		if screenIndex < len(screens) {
			targetScreen := screens[screenIndex]
			relativeStartX = arrangeParams.StartX - targetScreen.WorkLeft
			relativeStartY = arrangeParams.StartY - targetScreen.WorkTop
		}
	}

	settings.CustomArrangeParams = config.CustomArrangeParams{
		Width:             arrangeParams.Width,
		Height:            arrangeParams.Height,
		StartX:            relativeStartX, // 保存相对坐标
		StartY:            relativeStartY, // 保存相对坐标
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

	return nil
}

// GetScreensInfo 获取屏幕信息
func (c *ChromeService) GetScreensInfo() ([]utils.ScreenInfo, error) {
	if c.provider != nil {
		commonScreens, err := c.provider.GetScreensInfo()
		if err != nil {
			return nil, err
		}
		var utilsScreens []utils.ScreenInfo
		for _, s := range commonScreens {
			utilsScreens = append(utilsScreens, utils.ScreenInfo{
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
				DeviceName: s.DeviceName,
			})
		}
		return utilsScreens, nil
	}
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

	// 只使用已导入且仍有原生窗口的多开 Chrome。手动关闭 Chrome 后，
	// 导入记录中的 IsRunning 不会自动变化，不能把它当成实时状态。
	var windowProcesses []utils.ChromeProcessInfo
	var staleWindowNumbers []int
	for _, windowNumber := range windowNumbers {
		windowInfo := c.syncManager.GetWindowByID(windowNumber)
		if windowInfo == nil {
			continue
		}
		windowValid := windowInfo.PID > 0 && c.provider.IsProcessRunning(windowInfo.PID)
		if windowValid {
			windowHandle := common.WindowHandle(windowInfo.HWND)
			if runtime.GOOS == "darwin" {
				windowHandle = common.WindowHandle(windowInfo.PID)
			}
			windowValid, _ = c.provider.IsWindowValid(windowHandle)
		}
		if !windowValid {
			staleWindowNumbers = append(staleWindowNumbers, windowNumber)
			continue
		}
		windowProcesses = append(windowProcesses, utils.ChromeProcessInfo{
			PID:         windowInfo.PID,
			HWND:        windowInfo.HWND,
			Title:       windowInfo.Title,
			Number:      windowInfo.Number,
			UserDataDir: windowInfo.UserDataDir,
			DebugPort:   windowInfo.DebugPort,
		})
	}

	if len(staleWindowNumbers) > 0 {
		if c.syncManager.IsRunning() {
			_ = c.syncManager.StopSync()
			if c.app != nil {
				c.app.Event.Emit(syncAutoStoppedEvent)
			}
		}
		_ = c.syncManager.RemoveImportedWindows(staleWindowNumbers)
		c.removeSelectedWindowNumbers(staleWindowNumbers)
		if c.app != nil {
			c.app.Event.Emit(windowsUpdatedEvent, c.syncManager.GetAllImportedWindows())
		}
		return fmt.Errorf("窗口 %v 已关闭，已从列表移除；请重新导入新打开的窗口后再批量打开网页", staleWindowNumbers)
	}

	if len(windowProcesses) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口，请先导入窗口")
	}

	syncWasRunning := c.syncManager.IsRunning()
	masterDebugPort := 0
	masterPID := int32(0)
	if syncWasRunning {
		if masterWindow := c.syncManager.GetMasterWindow(); masterWindow != nil {
			masterDebugPort = masterWindow.DebugPort
			masterPID = masterWindow.PID
		}
		_ = c.syncManager.PauseSync()
		defer func() {
			_ = c.syncManager.ResumeSync()
		}()
	}

	return c.batchOpenURLOptimized(url, windowProcesses, syncWasRunning, masterDebugPort, masterPID)
}

// batchOpenURLOptimized 优化的批量打开网页实现
func (c *ChromeService) batchOpenURLOptimized(url string, windowProcesses []utils.ChromeProcessInfo, syncWasRunning bool, masterDebugPort int, masterPID int32) error {
	if url == "" {
		return fmt.Errorf("网址不能为空")
	}

	if len(windowProcesses) == 0 {
		return fmt.Errorf("窗口列表为空")
	}

	normalizedURL := c.normalizeURL(url)
	masterSelected := false
	if syncWasRunning {
		for _, process := range windowProcesses {
			if (masterDebugPort > 0 && process.DebugPort == masterDebugPort) || (masterPID > 0 && process.PID == masterPID) {
				masterSelected = true
				break
			}
		}
		if masterSelected {
			c.syncManager.BeginProgrammaticMasterTarget(normalizedURL)
		}
	}

	// 并行打开网页到多个窗口
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successCount int
	var errors []string
	createdTargets := make(map[int]string, len(windowProcesses))
	masterTargetID := ""

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

			targetID, err := c.openURLByDevToolsOptimized(normalizedURL, proc.DebugPort)
			isMaster := syncWasRunning &&
				((masterDebugPort > 0 && proc.DebugPort == masterDebugPort) || (masterPID > 0 && proc.PID == masterPID))

			mu.Lock()
			if err != nil {
				errors = append(errors, fmt.Sprintf("窗口 %d: %v", proc.Number, err))
			} else {
				successCount++
				createdTargets[int(proc.PID)] = targetID
				if isMaster {
					masterTargetID = targetID
				}
			}
			mu.Unlock()
		}(process)

		// 添加小延迟避免同时发送太多请求
		time.Sleep(50 * time.Millisecond)
	}

	// 等待所有操作完成
	wg.Wait()

	if successCount == 0 {
		if masterSelected {
			c.syncManager.CancelProgrammaticMasterTarget()
		}
		return fmt.Errorf("没有成功打开任何网页: %v", errors)
	}
	if masterSelected {
		if masterTargetID == "" {
			c.syncManager.CancelProgrammaticMasterTarget()
			errors = append(errors, "主控窗口未能创建新标签页")
		} else {
			c.syncManager.ConfirmProgrammaticMasterTarget(masterTargetID)
			if err := c.syncManager.MapProgrammaticPageTargets(masterTargetID, normalizedURL, createdTargets); err != nil {
				errors = append(errors, fmt.Sprintf("同步目标绑定失败: %v", err))
			}
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("部分窗口打开失败: %s", strings.Join(errors, "；"))
	}

	return nil
}

// openURLByDevToolsOptimized 优化的DevTools API调用 - 使用原生HTTP请求
func (c *ChromeService) openURLByDevToolsOptimized(url string, debugPort int) (string, error) {
	client := &http.Client{
		Timeout: 2 * time.Second, // 2秒超时
	}

	// 构建DevTools API URL
	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, url)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("创建HTTP请求失败 (端口 %d): %v", debugPort, err)
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP请求失败 (端口 %d): %v", debugPort, err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("DevTools API返回错误状态 %d (端口 %d)", resp.StatusCode, debugPort)
	}

	var createdTarget struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createdTarget); err != nil {
		return "", fmt.Errorf("解析DevTools响应失败 (端口 %d): %v", debugPort, err)
	}
	if createdTarget.ID == "" {
		return "", fmt.Errorf("DevTools响应缺少Target ID (端口 %d)", debugPort)
	}
	return createdTarget.ID, nil
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

	c.selectedWindowsMux.RLock()
	selectedWindows := append([]int(nil), c.selectedWindows...)
	c.selectedWindowsMux.RUnlock()
	if len(selectedWindows) == 0 {
		for _, window := range allWindows {
			selectedWindows = append(selectedWindows, window.Number)
		}
	}

	if len(selectedWindows) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	// 直接调用优化后的批量打开方法
	return c.BatchOpenURL(url, selectedWindows)
}

func (c *ChromeService) removeSelectedWindowNumbers(windowNumbers []int) {
	removed := make(map[int]bool, len(windowNumbers))
	for _, number := range windowNumbers {
		removed[number] = true
	}
	c.selectedWindowsMux.Lock()
	filtered := c.selectedWindows[:0]
	for _, number := range c.selectedWindows {
		if !removed[number] {
			filtered = append(filtered, number)
		}
	}
	c.selectedWindows = filtered
	c.selectedWindowsMux.Unlock()
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

	if s.syncManager == nil {
		return nil
	}

	if s.syncManager.IsRunning() {
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
			s.app.Event.Emit(syncAutoStoppedEvent)
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
		log.Printf("safe-closing mac window %d (%d/%d)", window.ID, index+1, len(windowsToClose))

		closeWg.Add(1)
		go func(window struct {
			ID   int
			HWND uintptr
			PID  int32
		}) {
			defer closeWg.Done()

			err := s.provider.CloseWindowGracefully(common.WindowHandle(window.PID), gracefulCloseTimeoutUnits)
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
			time.Sleep(defaultWindowCloseInterval)
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
		if !containsWindowID(pendingCloseIDs, id) {
			pendingCloseIDs = append(pendingCloseIDs, id)
		}
	}
	sort.Ints(pendingCloseIDs)

	_ = s.syncManager.RemoveImportedWindows(closedWindowIDs)
	remainingWindows := s.syncManager.GetAllImportedWindows()
	if s.app != nil {
		log.Printf("emitting windows.updated after close: %d", len(remainingWindows))
		s.app.Event.Emit(windowsUpdatedEvent, remainingWindows)
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
	masterNumber := 0
	state := c.syncManager.GetState()
	if state.MasterWindowInfo != nil {
		masterNumber = state.MasterWindowInfo.Number
	}

	syncWasRunning := c.syncManager.IsRunning()
	preferredTargets := make(map[int]string, len(windowNumbers))
	if syncWasRunning {
		if err := c.syncManager.PauseSync(); err != nil {
			return err
		}
		defer func() {
			_ = c.syncManager.ResumeSync()
		}()
		for _, windowNumber := range windowNumbers {
			windowInfo := c.syncManager.GetWindowByID(windowNumber)
			if windowInfo == nil || windowInfo.PID <= 0 {
				continue
			}
			if targetID := c.GetActiveTargetID(windowInfo.PID); targetID != "" {
				preferredTargets[int(windowInfo.PID)] = targetID
			}
		}
	}

	if err := c.tabManager.KeepOnlyCurrentTab(windowNumbers, c, masterNumber); err != nil {
		return err
	}
	if syncWasRunning {
		if err := c.syncManager.RefreshPageTargetMappings(preferredTargets); err != nil {
			return fmt.Errorf("当前标签页已保留，但同步目标重新绑定失败: %w", err)
		}
	}
	return nil
}

// KeepOnlyNewTab 仅保留新标签页，关闭其他标签页
func (c *ChromeService) KeepOnlyNewTab(windowNumbers []int) error {
	syncWasRunning := c.syncManager != nil && c.syncManager.IsRunning()
	masterPID := 0
	trackMasterTarget := false
	if syncWasRunning {
		if master := c.syncManager.GetMasterWindow(); master != nil {
			for _, number := range windowNumbers {
				if number == master.Number {
					masterPID = int(master.PID)
					trackMasterTarget = masterPID > 0
					break
				}
			}
		}
		if trackMasterTarget {
			c.syncManager.BeginProgrammaticMasterTarget("chrome://newtab/")
		}
		if err := c.syncManager.PauseSync(); err != nil {
			if trackMasterTarget {
				c.syncManager.CancelProgrammaticMasterTarget()
			}
			return err
		}
		defer func() {
			_ = c.syncManager.ResumeSync()
		}()
	}

	createdTargets, err := c.tabManager.KeepOnlyNewTab(windowNumbers, c)
	if err != nil {
		if trackMasterTarget {
			c.syncManager.CancelProgrammaticMasterTarget()
		}
		return err
	}
	if syncWasRunning {
		if trackMasterTarget {
			masterTargetID := createdTargets[masterPID]
			if masterTargetID == "" {
				c.syncManager.CancelProgrammaticMasterTarget()
				return fmt.Errorf("新标签页已创建，但主控窗口 Target ID 缺失")
			}
			c.syncManager.ConfirmProgrammaticMasterTarget(masterTargetID)
		}
		if err := c.syncManager.RefreshPageTargetMappings(createdTargets); err != nil {
			if trackMasterTarget {
				c.syncManager.CancelProgrammaticMasterTarget()
			}
			return fmt.Errorf("新标签页已创建，但同步目标重新绑定失败: %w", err)
		}
	}
	return nil
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

	c.systray = c.app.SystemTray.New()

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
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

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

	// 多次尝试确保窗口置顶
	for i := 0; i < 3; i++ {
		// 显示并激活主窗口（Wails v3的Show方法可能不返回error）
		c.mainWindow.Show()

		// 将主窗口置为前台
		c.mainWindow.Focus()

		// 临时设置主窗口为总是置顶，然后取消，这样可以让它出现在所有窗口前面
		c.mainWindow.SetAlwaysOnTop(true)

		// 稍微延迟后取消置顶状态
		time.Sleep(50 * time.Millisecond)
		c.mainWindow.SetAlwaysOnTop(false)

		// 每次尝试间隔一点时间
		if i < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return nil
}

// main function serves as the application's entry point. It initializes the application, creates a window,
// and starts a goroutine that emits a time-based event every second. It subsequently runs the application and
// logs any error that might occur.
func main() {

	// 添加全局 panic 恢复机制,防止程序崩溃
	defer func() {
		if r := recover(); r != nil {
			_ = r
			time.Sleep(5 * time.Second)
		}
	}()

	// 设置信号处理，确保程序退出时清理资源
	setupGracefulShutdown()

	chromeService, err := NewChromeService()
	if err != nil {
		// 如果初始化失败，显示错误提示并退出
		log.Printf("Startup Error: %v", err)
		// 在没有 app 的情况下，我们只能依赖终端或简单的 Native 提示
		// 这里暂且记录日志，后续如果进入了 app.Run 失败我们可以用 Wails 对话框
		// 但 NewChromeService 失败通常意味着核心组件不可用
		log.Printf("CRITICAL: Failed to initialize ChromeService: %v", err)
		return
	}
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

	// 检查是否有保存的窗口位置 (只要不等于 -1 就视为有效，包括 0 和负数)
	hasCustomPosition := settings.WindowConfig.X != -1 && settings.WindowConfig.Y != -1

	// Create the main window
	windowOptions := application.WebviewWindowOptions{
		Name:                "main",
		Title:               "ChromeManager V4.0",
		Width:               settings.WindowConfig.Width,
		Height:              settings.WindowConfig.Height,
		MinWidth:            config.DefaultWindowMinWidth,  // 使用配置文件的最小宽度常量
		MinHeight:           config.DefaultWindowMinHeight, // 使用配置文件的最小高度常量
		DisableResize:       false,
		Frameless:           true, // 恢复无边框模式
		URL:                 "/",
		DevToolsEnabled:     false,
		MinimiseButtonState: application.ButtonEnabled,
		MaximiseButtonState: application.ButtonEnabled,
		CloseButtonState:    application.ButtonEnabled,
		Hidden:              false, // 确保窗口默认可见
	}

	mainWindow := app.Window.NewWithOptions(windowOptions)

	// 设置窗口最小尺寸
	chromeService.setWindowMinSize()

	// 使用 goroutine 延迟设置位置和焦点，确保主循环已启动
	// 这是一个安全措施，避免在主循环启动前操作窗口导致的问题
	go func() {
		// 短暂等待，让窗口完成初始化
		time.Sleep(300 * time.Millisecond)

		if hasCustomPosition {
			// Native Restore Logic (Avoid Wails coordinate issues)
			restored := false
			if chromeService.provider != nil {
				if appProv, ok := chromeService.provider.(common.AppWindowProvider); ok {
					w := settings.WindowConfig.Width
					h := settings.WindowConfig.Height
					if w > 0 && h > 0 {
						r := common.Rect{
							Left:   settings.WindowConfig.X,
							Top:    settings.WindowConfig.Y,
							Width:  w,
							Height: h,
						}
						err := appProv.SetAppWindowPosition(0, r)
						if err == nil {
							restored = true
						}
					}
				}
			}

			if !restored {
				// Fallback to Wails SetPosition
				mainWindow.SetPosition(settings.WindowConfig.X, settings.WindowConfig.Y)
			}
		} else {
			mainWindow.Center()
		}

		if settings.WindowConfig.Maximized {
			mainWindow.Maximise()
		}

		// 强制聚焦窗口
		mainWindow.Focus()
	}()

	chromeService.mainWindow = mainWindow

	// 添加窗口关闭事件监听器來保存窗口位置
	mainWindow.OnWindowEvent(events.Common.WindowClosing, func(e *application.WindowEvent) {
		_ = chromeService.saveWindowPosition()
	})

	// 设置系统托盘（延迟初始化避免DPI权限问题）
	go func() {
		// 等待主窗口完全加载后再初始化系统托盘
		time.Sleep(1 * time.Second)
		_ = chromeService.setupSystemTray()
		if err := chromeService.apiServer.Start(context.Background()); err != nil {
			log.Printf("API server startup failed: %v", err)
		}
	}()

	// Run the application
	err = app.Run()

	if err != nil {
		log.Printf("application run failed: %v", err)
		os.Exit(1)
	}
}

// GetAllImportedWindows returns all currently parsed/imported windows across Windows & Mac.
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

// GetAPIWindows returns the raw syncmanager.WindowInfo which contains UserDataDir
func (c *ChromeService) GetAPIWindows() []syncmanager.WindowInfo {
	if c.syncManager == nil {
		return []syncmanager.WindowInfo{}
	}
	return c.syncManager.GetAllImportedWindows()
}

// GetActiveTargetID returns the active target ID for a specified process
func (c *ChromeService) GetActiveTargetID(pid int32) string {
	if c.provider != nil {
		return c.provider.GetActiveTargetID(int(pid))
	}
	return ""
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

	var windowHWNDs []uintptr
	chromeProcesses, err := utils.FindChromeProcesses()
	if err != nil || len(chromeProcesses) == 0 {
		return fmt.Errorf("没有找到正在运行的Chrome窗口")
	}

	windowMap := make(map[int]*utils.ChromeProcessInfo)
	for i := range chromeProcesses {
		windowMap[chromeProcesses[i].Number] = &chromeProcesses[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists {
			windowHWNDs = append(windowHWNDs, windowInfo.HWND)
		}
	}

	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// For macOS, use CDP instead of InputManager
	if runtime.GOOS == "darwin" {
		// Prepare list of PIDs to send text to
		var targetPIDs []int
		for _, windowNumber := range windowNumbers {
			if windowInfo, exists := windowMap[windowNumber]; exists {
				targetPIDs = append(targetPIDs, int(windowInfo.PID))
			}
		}

		// Generate random numbers for each window and dispatch
		for _, pid := range targetPIDs {
			// Generate number
			var valStr string
			if config.IsFloat {
				val := config.MinValue + rand.Float64()*(config.MaxValue-config.MinValue)
				format := fmt.Sprintf("%%.%df", config.DecimalPlaces)
				valStr = fmt.Sprintf(format, val)
			} else {
				val := int(config.MinValue) + rand.Intn(int(config.MaxValue-config.MinValue)+1)
				valStr = fmt.Sprintf("%d", val)
			}

			// Simulate Input sequence
			c.syncManager.ExecuteBatchInput([]int{pid}, valStr, config.Delayed, config.Overwrite)
		}
		return nil
	}

	// 调用输入管理器 (Windows)
	return c.inputManager.InputRandomNumbers(windowHWNDs, config)
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

	var windowHWNDs []uintptr
	chromeProcesses, err := utils.FindChromeProcesses()
	if err != nil || len(chromeProcesses) == 0 {
		return fmt.Errorf("没有找到正在运行的Chrome窗口")
	}

	windowMap := make(map[int]*utils.ChromeProcessInfo)
	for i := range chromeProcesses {
		windowMap[chromeProcesses[i].Number] = &chromeProcesses[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists {
			windowHWNDs = append(windowHWNDs, windowInfo.HWND)
		}
	}

	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// For macOS, handle file reading here and delegate to InputTextFromLines
	if runtime.GOOS == "darwin" {
		lines, err := c.inputManager.GetFilePreview(config.FilePath)
		if err != nil {
			return fmt.Errorf("读取文件失败: %v", err)
		}
		return c.InputTextFromLines(windowNumbers, lines, config.InputMethod, true, true) // Default overwrite=true, delayed=true for file input
	}

	// 调用输入管理器 (Windows)
	return c.inputManager.InputTextFromFile(windowHWNDs, config)
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

	var windowHWNDs []uintptr
	chromeProcesses, err := utils.FindChromeProcesses()
	if err != nil || len(chromeProcesses) == 0 {
		return fmt.Errorf("没有找到正在运行的Chrome窗口")
	}

	windowMap := make(map[int]*utils.ChromeProcessInfo)
	for i := range chromeProcesses {
		windowMap[chromeProcesses[i].Number] = &chromeProcesses[i]
	}

	for _, windowNumber := range windowNumbers {
		if windowInfo, exists := windowMap[windowNumber]; exists {
			windowHWNDs = append(windowHWNDs, windowInfo.HWND)
		}
	}

	if len(windowHWNDs) == 0 {
		return fmt.Errorf("没有找到有效的运行中窗口")
	}

	// For macOS, use CDP instead of InputManager
	if runtime.GOOS == "darwin" {
		var targetPIDs []int
		for _, windowNumber := range windowNumbers {
			if windowInfo, exists := windowMap[windowNumber]; exists {
				// Remember, on macOS, ProcessID is used in utils.ChromeProcessInfo but we already established PID == ProcessID above. Ah wait, in utils_darwin.go it is PID, in utils.go it is PID... wait, I fixed process.ProcessID back in main.go, but ChromeProcessInfo actually has PID. Let me ensure it is correct.
				targetPIDs = append(targetPIDs, int(windowInfo.PID))
			}
		}

		totalLines := len(lines)

		// Create a shuffled slice of lines for "random" mode
		randomLines := make([]string, totalLines)
		if inputMethod == "random" {
			copy(randomLines, lines)
			rand.Shuffle(totalLines, func(i, j int) {
				randomLines[i], randomLines[j] = randomLines[j], randomLines[i]
			})
		}

		for i, pid := range targetPIDs {
			var text string
			switch inputMethod {
			case "sequence":
				text = lines[i%totalLines]
			case "random":
				// Reshuffle if we've used all lines
				if i > 0 && i%totalLines == 0 {
					rand.Shuffle(totalLines, func(i, j int) {
						randomLines[i], randomLines[j] = randomLines[j], randomLines[i]
					})
				}
				text = randomLines[i%totalLines]
			case "fixed":
				fallthrough
			default:
				text = lines[0]
			}
			c.syncManager.ExecuteBatchInput([]int{pid}, text, delayed, overwrite)
		}
		return nil
	}

	// 调用输入管理器的直接行输入方法 (Windows/macOS)
	return c.inputManager.InputTextFromLines(windowHWNDs, lines, inputMethod, overwrite, delayed)
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

	// 确保环境创建器使用最新的设置
	c.envCreator.CacheDir = groupCacheDir
	c.envCreator.ShortcutDir = groupShortcutPath

	// 核心修改: 在这里解析浏览器路径
	// 如果用户设置了自定义路径，直接使用
	// 如果未设置，尝试自动查找
	resolvedBrowserPath := currentSettings.ChromePath
	if resolvedBrowserPath == "" {
		if c.provider != nil {
			foundPath, err := c.provider.FindBrowserPath()
			if err == nil {
				resolvedBrowserPath = foundPath
			}
		}
	}
	c.envCreator.ChromePath = resolvedBrowserPath

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

	return c.provider.FindBrowserPath()
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
		if browserPath, err := c.provider.FindBrowserPath(); err != nil {
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

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

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

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

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

	if err := c.saveSettings(settings); err != nil {
		return fmt.Errorf("保存设置失败: %v", err)
	}

	c.settingsMux.Lock()
	c.cachedSettings = settings
	c.settingsMux.Unlock()

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
		"ScreenSelection":        settings.ScreenSelection,
		"AutoModifyShortcutIcon": settings.AutoModifyShortcutIcon,
		"WindowOpenSpeed":        settings.WindowOpenSpeed,
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

	// 保存设置
	if err := c.saveSettings(&settings); err != nil {
		return fmt.Errorf("保存导入的设置失败: %v", err)
	}

	// 更新缓存设置（需要加锁）
	c.settingsMux.Lock()
	c.cachedSettings = &settings
	c.settingsMux.Unlock()

	// 在锁外调用 updateEnvironmentCreatorSettings，避免死锁
	// 因为 updateEnvironmentCreatorSettings 内部会尝试获取读锁
	c.updateEnvironmentCreatorSettings()

	return nil
}

// SelectFile 打开文件选择对话框
func (c *ChromeService) SelectFile(title, filter string) (string, error) {
	if c.provider == nil {
		return "", fmt.Errorf("provider not initialized")
	}

	return c.provider.SelectFileDialog(title, filter)
}

// ExportSettingsToFile 导出配置文件到文件（使用原生对话框）
func (c *ChromeService) ExportSettingsToFile() error {
	settings, err := c.ExportSettings()
	if err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %v", err)
	}

	if c.app == nil {
		return fmt.Errorf("app not initialized")
	}

	defaultName := fmt.Sprintf("ChromeManager_Settings_%s.json", time.Now().Format("2006-01-02"))
	filepath, err := c.app.Dialog.SaveFile().
		SetMessage("导出设置").
		SetFilename(defaultName).
		AddFilter("JSON 配置文件 (*.json)", "*.json").
		PromptForSingleSelection()

	if err != nil {
		return err // 对话框错误
	}
	if filepath == "" {
		return nil // 用户取消
	}

	if !strings.HasSuffix(strings.ToLower(filepath), ".json") {
		filepath += ".json"
	}

	if err := os.WriteFile(filepath, jsonData, 0644); err != nil {
		return fmt.Errorf("保存文件失败: %v", err)
	}
	return nil
}

// ImportSettingsFromFile 从文件导入配置文件（使用原生对话框）
func (c *ChromeService) ImportSettingsFromFile() error {
	if c.app == nil {
		return fmt.Errorf("app not initialized")
	}

	filepath, err := c.app.Dialog.OpenFile().
		SetTitle("导入配置文件").
		AddFilter("JSON 配置文件 (*.json)", "*.json").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		PromptForSingleSelection()

	if err != nil {
		return err // 对话框取消或错误
	}
	if filepath == "" {
		return nil // 用户取消
	}

	data, err := os.ReadFile(filepath)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %v", err)
	}

	var settingsData map[string]interface{}
	if err := json.Unmarshal(data, &settingsData); err != nil {
		return fmt.Errorf("解析配置文件失败: %v", err)
	}

	if err := c.ImportSettings(settingsData); err != nil {
		return err
	}

	// 导入成功后可以直接重启
	c.RestartApplication()
	return nil
}

// ClearAllDataAndRestart 清除所有配置并重启（恢复出厂）
func (c *ChromeService) ClearAllDataAndRestart() error {
	configPath, err := config.GetSettingsFilePath()
	if err == nil {
		if _, err := os.Stat(configPath); err == nil {
			os.Remove(configPath)
		}
	}
	c.RestartApplication()
	return nil
}

// OpenAccessibilitySettings 打开 macOS 辅助功能设置页面
func (c *ChromeService) OpenAccessibilitySettings() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("该功能仅支持 macOS")
	}

	cmd := exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开辅助功能设置失败: %v", err)
	}
	return nil
}

// RevealChromeManagerAppInFinder 在 Finder 中定位 ChromeManager.app（或当前可执行文件）
func (c *ChromeService) RevealChromeManagerAppInFinder() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("该功能仅支持 macOS")
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败: %v", err)
	}

	revealPath := exePath
	if idx := strings.Index(exePath, ".app/"); idx != -1 {
		revealPath = exePath[:idx+len(".app")]
	}

	cmd := exec.Command("open", "-R", revealPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("定位 ChromeManager 失败: %v", err)
	}
	return nil
}

// RevealTerminalAppInFinder 在 Finder 中定位 Terminal.app
func (c *ChromeService) RevealTerminalAppInFinder() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("该功能仅支持 macOS")
	}

	terminalCandidates := []string{
		"/System/Applications/Utilities/Terminal.app",
		"/Applications/Utilities/Terminal.app",
	}

	var terminalPath string
	for _, p := range terminalCandidates {
		if _, err := os.Stat(p); err == nil {
			terminalPath = p
			break
		}
	}

	if terminalPath == "" {
		return fmt.Errorf("未找到 Terminal.app")
	}

	cmd := exec.Command("open", "-R", terminalPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("定位 Terminal.app 失败: %v", err)
	}
	return nil
}

// OpenExternalURL 在系统默认浏览器中打开外部链接
func (c *ChromeService) OpenExternalURL(url string) {
	_ = browser.OpenURL(url)
}

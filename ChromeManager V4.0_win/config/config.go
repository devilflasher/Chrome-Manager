package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	BaseDebugPort = 9222

	DWMBorderColorMaster = 0x00FF0000 //

	//
	DefaultWindowWidth     = 720
	DefaultWindowHeight    = 460
	DefaultWindowMinWidth  = 720
	DefaultWindowMinHeight = 460

	ChromeDefaultProfile    = "Default"
	ChromeUserDataDirPrefix = "ChromeProfile_"

	BrowserTypeChrome = "chrome"
	BrowserTypeEdge   = "edge"
	BrowserTypeOpera  = "opera"
	BrowserTypeBrave  = "brave"
)

var ChromeDefaultArgs = []string{
	"--no-default-browser-check",               //
	"--no-first-run",                           // ?	"--disable-translate",                      //
	"--disable-features=Translate,TranslateUI", // I
	"--disable-background-timer-throttling",    // ?	"--disable-backgrounding-occluded-windows", // ?	"--disable-renderer-backgrounding",         //
	"--force-renderer-accessibility",
	"--disable-ipc-flooding-protection",
}

type WindowConfig struct {
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	MinWidth  int  `json:"minWidth"`
	MinHeight int  `json:"minHeight"`
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Maximized bool `json:"maximized"` //
}

type RandomInputConfig struct {
	MinValue      float64 `json:"minValue"`
	MaxValue      float64 `json:"maxValue"`
	IsFloat       bool    `json:"isFloat"`
	DecimalPlaces int     `json:"decimalPlaces"`
	Overwrite     bool    `json:"overwrite"`
	Delayed       bool    `json:"delayed"`
}

type CustomArrangeParams struct {
	Width             int `json:"width"`
	Height            int `json:"height"`
	StartX            int `json:"startX"`
	StartY            int `json:"startY"`
	HorizontalSpacing int `json:"horizontalSpacing"`
	VerticalSpacing   int `json:"verticalSpacing"`
	WindowsPerRow     int `json:"windowsPerRow"`
}

type GroupConfig struct {
	Name         string `json:"name"`         //
	ShortcutPath string `json:"shortcutPath"` //
	CacheDir     string `json:"cacheDir"`     //
}

type SyncConfig struct {
	MouseMoveThreshold       int           `json:"mouseMoveThreshold"`
	MouseMoveInterval        time.Duration `json:"mouseMoveInterval"`
	KeyboardInterval         time.Duration `json:"keyboardInterval"`
	WheelEventThreshold      time.Duration `json:"wheelEventThreshold"`
	EnablePopupSync          bool          `json:"enablePopupSync"`
	EnableZoomSync           bool          `json:"enableZoomSync"`
	EnableScrollSync         bool          `json:"enableScrollSync"`
	EnableKeyboardSync       bool          `json:"enableKeyboardSync"`
	HighPrecisionMode        bool          `json:"highPrecisionMode"`
	MaxConcurrentEvents      int           `json:"maxConcurrentEvents"`
	EventBufferSize          int           `json:"eventBufferSize"`
	MaxRetryAttempts         int           `json:"maxRetryAttempts"`
	RetryDelay               time.Duration `json:"retryDelay"`
	ErrorThreshold           int           `json:"errorThreshold"`
	EnableEventDrivenMode    bool          `json:"enableEventDrivenMode"`
	DisablePollingOperations bool          `json:"disablePollingOperations"`
	FastResponseMode         bool          `json:"fastResponseMode"`
	CacheUpdateStrategy      int           `json:"cacheUpdateStrategy"`
}

type BrowserProfile struct {
	ShortcutPath   string        `json:"shortcutPath"`
	CacheDir       string        `json:"cacheDir"`
	ExecutablePath string        `json:"executablePath"` // Was ChromePath
	Groups         []GroupConfig `json:"groups"`
	CurrentGroup   string        `json:"currentGroup"`
}

type Settings struct {
	// Legacy fields (kept for migration)
	ShortcutPath string        `json:"shortcutPath,omitempty"`
	ChromePath   string        `json:"chromePath,omitempty"`
	CacheDir     string        `json:"cacheDir,omitempty"`
	Groups       []GroupConfig `json:"groups,omitempty"`
	CurrentGroup string        `json:"currentGroup,omitempty"`

	// New Browser Profiles
	Profiles               map[string]BrowserProfile `json:"profiles"`
	BrowserType            string                    `json:"browserType"`
	ScreenSelection        string                    `json:"screenSelection"`
	AutoModifyShortcutIcon bool                      `json:"autoModifyShortcutIcon"`
	WindowOpenSpeed        float64                   `json:"windowOpenSpeed"`
	WindowCloseInterval    float64                   `json:"windowCloseInterval"`
	LastWindowNumbers      string                    `json:"lastWindowNumbers"`
	WindowNumbersHistory   string                    `json:"windowNumbersHistory"`
	LastEnvCreationNumbers string                    `json:"lastEnvCreationNumbers"`
	CustomArrangeParams    CustomArrangeParams       `json:"customArrangeParams"`
	CustomURLs             map[string]string         `json:"customURLs"`
	SyncToggleHotkey       string                    `json:"syncToggleHotkey"`
	WindowConfig           WindowConfig              `json:"windowConfig"`
	SyncConfig             SyncConfig                `json:"syncConfig"`
	RandomInputConfig      RandomInputConfig         `json:"randomInputConfig"`
	CloseBehavior          string                    `json:"closeBehavior"`

	// Configuration flags
	EnableGroupMode bool `json:"enableGroupMode"`

	// OpenClaw API 集成
	EnableAPIServer bool   `json:"enableAPIServer"` // 是否启用 API 服务（默认关闭）
	APIPort         int    `json:"apiPort"`         // API 端口（默认 18923）
	APIToken        string `json:"apiToken"`        // 访问令牌
}

func GetDefaultSettings() *Settings {
	return &Settings{
		ShortcutPath:           "",
		CacheDir:               "",
		ChromePath:             "",                //
		BrowserType:            BrowserTypeChrome, // Default to Chrome
		ScreenSelection:        "",
		AutoModifyShortcutIcon: true,
		WindowOpenSpeed:        0.1,
		WindowCloseInterval:    0.1,
		LastWindowNumbers:      "",
		WindowNumbersHistory:   "",
		LastEnvCreationNumbers: "",
		CustomArrangeParams: CustomArrangeParams{
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
			Width:     DefaultWindowWidth,
			Height:    DefaultWindowHeight,
			MinWidth:  0, //
			MinHeight: 0, //
			X:         -1,
			Y:         -1,
			Maximized: false,
		},
		SyncConfig: SyncConfig{ //
			// ?			MouseMoveThreshold:  2,                    //
			MouseMoveInterval: 5 * time.Millisecond, //
			KeyboardInterval:  3 * time.Millisecond, // ?			WheelEventThreshold: 5 * time.Millisecond, // ?
			// ?			EnablePopupSync:    true,
			EnableZoomSync:     true,
			EnableScrollSync:   true,
			EnableKeyboardSync: true,

			//
			HighPrecisionMode: true, // ?			MaxConcurrentEvents: 500,  //
			EventBufferSize:   5000, // ?
			// ?			MaxRetryAttempts: 2,                     // ?			RetryDelay:       20 * time.Millisecond, //
			ErrorThreshold: 20, // ?
			//  -
			EnableEventDrivenMode:    true,
			DisablePollingOperations: true, // ?			FastResponseMode:         true, //
			CacheUpdateStrategy:      0,
		},

		//
		Groups: []GroupConfig{
			{
				Name:         "",
				ShortcutPath: "",
				CacheDir:     "",
			},
		},
		CurrentGroup:    "",
		EnableGroupMode: false, //
	}
}

func GetSettingsFilePath() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		// in
		return "bin/settings.json", nil
	}

	isDevelopment := filepath.Ext(execPath) != ".exe"

	if isDevelopment {
		return filepath.Join(filepath.Dir(execPath), "bin", "settings.json"), nil
	}

	execDir := filepath.Dir(execPath)
	return filepath.Join(execDir, "settings.json"), nil
}

func GetIconsDirPath() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "icons", nil
	}

	execDir := filepath.Dir(execPath)
	return filepath.Join(execDir, "icons"), nil
}

func GetCacheBasePath(settings *Settings) string {
	if settings.CacheDir != "" {
		return settings.CacheDir
	}

	execPath, err := os.Executable()
	if err != nil {
		return "cache"
	}

	execDir := filepath.Dir(execPath)
	return filepath.Join(execDir, "cache")
}

func GetChromeUserDataDir(settings *Settings, windowNumber int) string {
	basePath := GetCacheBasePath(settings)
	return filepath.Join(basePath, ChromeUserDataDirPrefix+fmt.Sprintf("%d", windowNumber))
}

func GetCurrentGroup(settings *Settings) *GroupConfig {
	if settings.CurrentGroup == "" || settings.CurrentGroup == "默认分组" {
		return &GroupConfig{
			Name:         "默认分组",
			ShortcutPath: settings.ShortcutPath,
			CacheDir:     settings.CacheDir,
		}
	}

	for i := range settings.Groups {
		if settings.Groups[i].Name == settings.CurrentGroup {
			return &settings.Groups[i]
		}
	}

	return &GroupConfig{
		Name:         "默认分组",
		ShortcutPath: settings.ShortcutPath,
		CacheDir:     settings.CacheDir,
	}
}

func GetGroupShortcutPath(settings *Settings) string {
	currentGroup := GetCurrentGroup(settings)
	return currentGroup.ShortcutPath
}

func GetGroupCacheDir(settings *Settings) string {
	currentGroup := GetCurrentGroup(settings)
	if currentGroup.CacheDir != "" {
		return currentGroup.CacheDir
	}

	// Use default cache path when the group cache directory is empty
	return GetCacheBasePath(settings)
}

func AddGroup(settings *Settings, group GroupConfig) error {
	if group.Name == "默认分组" {
		return fmt.Errorf("group '%s' is reserved", group.Name)
	}

	for _, existing := range settings.Groups {
		if existing.Name == group.Name {
			return fmt.Errorf("group '%s' already exists", group.Name)
		}
	}

	settings.Groups = append(settings.Groups, group)
	return nil
}

func RemoveGroup(settings *Settings, groupName string) error {
	if groupName == "" || groupName == "默认分组" {
		return fmt.Errorf("default group cannot be removed")
	}

	for i, group := range settings.Groups {
		if group.Name == groupName {
			settings.Groups = append(settings.Groups[:i], settings.Groups[i+1:]...)

			if settings.CurrentGroup == groupName {
				if len(settings.Groups) > 0 {
					settings.CurrentGroup = settings.Groups[0].Name
				} else {
					settings.CurrentGroup = "默认分组"
				}
			}

			return nil
		}
	}

	return fmt.Errorf("group '%s' not found", groupName)
}

func UpdateGroup(settings *Settings, oldName string, newGroup GroupConfig) error {
	if oldName == "默认分组" || newGroup.Name == "默认分组" {
		return fmt.Errorf("default group cannot be modified")
	}

	for i, group := range settings.Groups {
		if group.Name == oldName {
			if oldName != newGroup.Name {
				for _, existing := range settings.Groups {
					if existing.Name == newGroup.Name {
						return fmt.Errorf("group '%s' already exists", newGroup.Name)
					}
				}

				if settings.CurrentGroup == oldName {
					settings.CurrentGroup = newGroup.Name
				}
			}

			settings.Groups[i] = newGroup
			return nil
		}
	}

	return fmt.Errorf("group '%s' not found", oldName)
}

func SetCurrentGroup(settings *Settings, groupName string) error {
	if groupName == "" || groupName == "默认分组" {
		settings.CurrentGroup = "默认分组"
		return nil
	}

	for _, group := range settings.Groups {
		if group.Name == groupName {
			settings.CurrentGroup = groupName
			return nil
		}
	}

	return fmt.Errorf("group '%s' not found", groupName)
}

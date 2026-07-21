package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"chromemanager/platform/common"
)

const (
	BaseDebugPort = 9222

	DWMBorderColorMaster = 0x00FF0000 //

	//
	DefaultWindowWidth     = 720
	DefaultWindowHeight    = 460
	DefaultWindowMinWidth  = 720
	DefaultWindowMinHeight = 460

	ChromeDefaultProfile = "Default"

	BrowserTypeChrome = "chrome"
	BrowserTypeEdge   = "edge"
	BrowserTypeOpera  = "opera"
	BrowserTypeBrave  = "brave"

	ChromeManagerZoomExtensionID  = "hlinnlnngjhcpibekaodmdfjadppoddm"
	ChromeManagerZoomExtensionKey = "MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAq9E7eck/P8AYpWjNXbxlj3gYcqjgDxWlaZj30I9TiKvU1zDPFZEAssR1dEb0cZTLkw6VVsGdrnMLc4c1ugJZhGtaj8ij4E7vkjrivVT4U0E1GiWV//orGf6cyE9U8oO9USQlBxffxJUdjr7CEejsIr+SRtRCeGpebZF0iTCP63z0AJ3O5w9nufEAPzePfaxYhI3UO/dkFwzuo6zhKPFrfNoTVDgC+YXFIPfTuj5WIndUx6jUIJfy76TIvYEG1YI4FTKAglsxykvlPNDjNZU3U7rg0tP/70OhADhGOEydBqQlRT6lvccSaOoqSmqRkqYYRY0CWu650ehwaMiOOdmvlQIDAQAB"
)

var ChromeDefaultArgs = []string{
	"--no-default-browser-check",
	"--no-first-run",
	"--disable-translate",
	"--disable-features=Translate,TranslateUI",
	"--disable-background-timer-throttling",
	"--disable-backgrounding-occluded-windows",
	"--disable-renderer-backgrounding",
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

type Settings struct {
	ShortcutPath string        `json:"shortcutPath,omitempty"`
	ChromePath   string        `json:"chromePath,omitempty"`
	CacheDir     string        `json:"cacheDir,omitempty"`
	Groups       []GroupConfig `json:"groups,omitempty"`
	CurrentGroup string        `json:"currentGroup,omitempty"`

	ScreenSelection        string              `json:"screenSelection"`
	AutoModifyShortcutIcon bool                `json:"autoModifyShortcutIcon"`
	WindowOpenSpeed        float64             `json:"windowOpenSpeed"`
	LastWindowNumbers      string              `json:"lastWindowNumbers"`
	WindowNumbersHistory   string              `json:"windowNumbersHistory"`
	LastEnvCreationNumbers string              `json:"lastEnvCreationNumbers"`
	CustomArrangeParams    CustomArrangeParams `json:"customArrangeParams"`
	CustomURLs             map[string]string   `json:"customURLs"`
	SyncToggleHotkey       string              `json:"syncToggleHotkey"`
	WindowConfig           WindowConfig        `json:"windowConfig"`
	SyncConfig             SyncConfig          `json:"syncConfig"`
	RandomInputConfig      RandomInputConfig   `json:"randomInputConfig"`
	CloseBehavior          string              `json:"closeBehavior"`

	// API Configuration
	EnableAPI bool   `json:"enableAPI"`
	APIPort   int    `json:"apiPort"`
	APIToken  string `json:"apiToken"`

	// Configuration flags
	EnableGroupMode bool `json:"enableGroupMode"`
}

func GetDefaultSettings() *Settings {
	return &Settings{
		ShortcutPath:           "",
		CacheDir:               "",
		ChromePath:             "", //
		ScreenSelection:        "",
		AutoModifyShortcutIcon: false,
		WindowOpenSpeed:        0.1,
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
		APIPort:          18923,
		APIToken:         "",
		EnableAPI:        false,
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

	if runtime.GOOS == "darwin" && (isDarwinAppBundle(execPath) || !isTemporaryExecutable(execPath)) {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		return filepath.Join(configDir, "ChromeManager", "settings.json"), nil
	}
	isTemp := isTemporaryExecutable(execPath)

	// Also check for Windows temp
	if !isTemp && os.PathSeparator == '\\' {
		// Windows temp check
	}

	// Just use standard logic: Settings always next to executable, unless we detect we are in dev/temp
	dir := filepath.Dir(execPath)

	// Avoid "bin/bin" issue: if we are already in "bin", don't append "bin"
	if filepath.Base(dir) == "bin" {
		return filepath.Join(dir, "settings.json"), nil
	}

	// If running via go run (temp), fallback to relative CWD bin/settings.json
	if isTemp {
		wd, _ := os.Getwd()
		return filepath.Join(wd, "bin", "settings.json"), nil
	}

	// Default production: settings next to binary
	return filepath.Join(dir, "settings.json"), nil
}

// GetLegacySettingsFilePath returns the previous macOS location beside the app binary.
func GetLegacySettingsFilePath() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}

	execPath, err := os.Executable()
	if err != nil || (isTemporaryExecutable(execPath) && !isDarwinAppBundle(execPath)) {
		return "", err
	}
	return filepath.Join(filepath.Dir(execPath), "settings.json"), nil
}

func isDarwinAppBundle(execPath string) bool {
	return filepath.Base(filepath.Dir(execPath)) == "MacOS" && filepath.Base(filepath.Dir(filepath.Dir(execPath))) == "Contents" && filepath.Ext(filepath.Dir(filepath.Dir(filepath.Dir(execPath)))) == ".app"
}

func isTemporaryExecutable(execPath string) bool {
	return len(execPath) > 5 && (execPath[:5] == "/var/" || execPath[:5] == "/tmp/" || len(execPath) > 13 && execPath[:13] == "/private/var/")
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
	return filepath.Join(basePath, common.ChromeUserDataDirPrefix+fmt.Sprintf("%d", windowNumber))
}

func GetCurrentGroup(settings *Settings) *GroupConfig {
	// Empty string OR "默认分组" both mean "use global settings paths"
	if settings.CurrentGroup == "" || settings.CurrentGroup == "默认分组" {
		return &GroupConfig{
			Name:         "",
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
		Name:         "",
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
	for _, existing := range settings.Groups {
		if existing.Name == group.Name {
			return fmt.Errorf("group '%s' already exists", group.Name)
		}
	}

	settings.Groups = append(settings.Groups, group)
	return nil
}

func RemoveGroup(settings *Settings, groupName string) error {
	if len(settings.Groups) <= 1 {
		return fmt.Errorf("at least one group must remain")
	}

	for i, group := range settings.Groups {
		if group.Name == groupName {
			settings.Groups = append(settings.Groups[:i], settings.Groups[i+1:]...)

			if settings.CurrentGroup == groupName {
				if len(settings.Groups) > 0 {
					settings.CurrentGroup = settings.Groups[0].Name
				} else {
					settings.CurrentGroup = ""
				}
			}

			return nil
		}
	}

	return fmt.Errorf("group '%s' not found", groupName)
}

func UpdateGroup(settings *Settings, oldName string, newGroup GroupConfig) error {
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
	// "默认分组" is the virtual default group - it maps to empty CurrentGroup,
	// which makes GetCurrentGroup fall back to global Settings paths.
	if groupName == "默认分组" {
		settings.CurrentGroup = ""
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

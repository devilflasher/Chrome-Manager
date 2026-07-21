package utils

import (
	"chromemanager/config"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type IconManager struct{}

func NewIconManager() (*IconManager, error) {
	return &IconManager{}, nil
}

func (im *IconManager) ApplyIconsToWindows(windows []ChromeProcessInfo, autoModifyShortcut bool, shortcutDir string) {
	// Mac Stub
}

func (im *IconManager) SetWindowIcon(hwnd uintptr, iconPath string, retries int, delay time.Duration) error {
	return fmt.Errorf("macOS 不支持动态设置窗口图标")
}

type ShortcutRestorer struct{}

func NewShortcutRestorer() *ShortcutRestorer {
	return &ShortcutRestorer{}
}

func (sr *ShortcutRestorer) RestoreToDefaultIcons() error {
	return nil
}

func (sr *ShortcutRestorer) RestoreToDefaultIconsAllGroups(settings *config.Settings) error {
	chromePath, err := FindChromePath()
	if err != nil {
		return fmt.Errorf("未找到Chrome安装路径: %v", err)
	}

	chromeAppPath := ""
	if strings.Contains(chromePath, ".app/") {
		parts := strings.Split(chromePath, ".app/")
		chromeAppPath = parts[0] + ".app"
	} else {
		chromeAppPath = filepath.Dir(filepath.Dir(filepath.Dir(chromePath)))
	}
	originalIcon := filepath.Join(chromeAppPath, "Contents", "Resources", "app.icns")

	var dirs []string
	if settings.ShortcutPath != "" {
		dirs = append(dirs, settings.ShortcutPath)
	}
	for _, group := range settings.Groups {
		if group.ShortcutPath != "" {
			dirs = append(dirs, group.ShortcutPath)
		}
	}

	for _, dir := range dirs {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() && strings.HasSuffix(entry.Name(), ".app") {
					appPath := filepath.Join(dir, entry.Name())
					destIcon := filepath.Join(appPath, "Contents", "Resources", "appicon.icns")
					if _, err := os.Stat(originalIcon); err == nil {
						_ = CopyFile(originalIcon, destIcon)
						exec.Command("touch", appPath).Run()
					}
				}
			}
		}
	}

	return nil
}

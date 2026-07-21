package utils

import (
	"chromemanager/config"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"chromemanager/platform/common"
)

func buildShortcutArguments(userDataDir string) string {
	args := []string{
		fmt.Sprintf(`--user-data-dir="%s"`, userDataDir),
		`--remote-allow-origins=\*`,
	}

	args = append(args, config.ChromeDefaultArgs...)
	return strings.Join(args, " ")
}

// FindChromePath 查找Chrome安装路径
func FindChromePath() (string, error) {
	// 1. 尝试使用 mdfind 查找 (Spotlight)
	cmd := exec.Command("mdfind", "kMDItemCFBundleIdentifier == 'com.google.Chrome'")
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && strings.HasSuffix(line, ".app") {
				// 验证是否存在
				if _, err := os.Stat(line); err == nil {
					return filepath.Join(line, "Contents", "MacOS", "Google Chrome"), nil
				}
			}
		}
	}

	// 2. 常见路径回退
	paths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Google Chrome.app",
		filepath.Join(os.Getenv("HOME"), "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		"/Volumes/fangxiaoxiao/程序/Google Chrome.app/Contents/MacOS/Google Chrome", // 已知用户路径作为备选
	}
	if matches, globErr := filepath.Glob("/Volumes/*/程序/Google Chrome.app/Contents/MacOS/Google Chrome"); globErr == nil {
		paths = append(paths, matches...)
	}
	if matches, globErr := filepath.Glob("/Volumes/*/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); globErr == nil {
		paths = append(paths, matches...)
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("未找到 Google Chrome 浏览器")
}

// CopyFile 简单的文件复制辅助函数
func CopyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

// CreateShortcut 创建macOS快捷方式 (.app 包)
// shortcutPath: 目标 .app 路径 (例如 .../1.app)
// targetPath: Chrome 可执行文件路径 (.../Google Chrome)
// arguments: 启动参数 --args 之后的部分
func CreateShortcut(shortcutPath, targetPath, arguments, workingDir string) error {
	// 确保扩展名为 .app
	if !strings.HasSuffix(shortcutPath, ".app") {
		shortcutPath += ".app"
	}

	// 1. 创建目录结构
	contentsDir := filepath.Join(shortcutPath, "Contents")
	macOSDir := filepath.Join(contentsDir, "MacOS")
	resourcesDir := filepath.Join(contentsDir, "Resources")

	if err := os.MkdirAll(macOSDir, 0755); err != nil {
		return fmt.Errorf("创建MacOS目录失败: %v", err)
	}
	if err := os.MkdirAll(resourcesDir, 0755); err != nil {
		return fmt.Errorf("创建Resources目录失败: %v", err)
	}

	// 推断 Chrome.app 路径和图标路径
	// targetPath 通常是 .../Google Chrome.app/Contents/MacOS/Google Chrome
	// 我们需要 .../Google Chrome.app
	var chromeAppPath string
	if strings.Contains(targetPath, ".app/") {
		parts := strings.Split(targetPath, ".app/")
		chromeAppPath = parts[0] + ".app"
	} else {
		// 无法推断，尝试直接搜索图标? 还是默认无图标
		chromeAppPath = filepath.Dir(filepath.Dir(filepath.Dir(targetPath)))
	}

	// 2. 尝试复制图标 (app.icns)
	iconSrc := filepath.Join(chromeAppPath, "Contents", "Resources", "app.icns")
	if _, err := os.Stat(iconSrc); err == nil {
		if err := CopyFile(iconSrc, filepath.Join(resourcesDir, "appicon.icns")); err != nil {
			fmt.Printf("复制图标失败(非致命): %v\n", err)
		}
	}

	// 3. 创建 Info.plist
	infoPlist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key>
    <string>run</string>
    <key>CFBundleIconFile</key>
    <string>appicon</string>
    <key>CFBundleIdentifier</key>
    <string>com.google.chrome.shortcut` + strconv.FormatInt(time.Now().UnixNano(), 10) + `</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>LSUIElement</key>
    <true/>
</dict>
</plist>`
	if err := os.WriteFile(filepath.Join(contentsDir, "Info.plist"), []byte(infoPlist), 0644); err != nil {
		return fmt.Errorf("创建 Info.plist 失败: %v", err)
	}

	// 4. 创建启动脚本
	// 使用 open -n -a "Chrome.app" --args ...
	// 这是一个更稳健的启动方式，通过 LaunchServices 创建新实例
	// 注意: 添加 "$@" 以传递额外的参数 (如 debug-port)
	scriptContent := fmt.Sprintf("#!/bin/bash\nopen -n -a \"%s\" --args %s \"$@\"", chromeAppPath, arguments)

	runPath := filepath.Join(macOSDir, "run")
	if err := os.WriteFile(runPath, []byte(scriptContent), 0755); err != nil {
		return fmt.Errorf("创建启动脚本失败: %v", err)
	}

	// 确保脚本可执行
	if err := os.Chmod(runPath, 0755); err != nil {
		return fmt.Errorf("设置脚本权限失败: %v", err)
	}

	return nil
}

func (ec *EnvironmentCreator) CreateEnvironments(numbersStr string) error {
	if ec.CacheDir == "" || ec.ShortcutDir == "" {
		return fmt.Errorf("请先配置缓存目录和快捷方式目录")
	}

	// 确保目录存在
	if err := os.MkdirAll(ec.CacheDir, 0755); err != nil {
		return fmt.Errorf("创建缓存目录失败: %v", err)
	}

	if err := os.MkdirAll(ec.ShortcutDir, 0755); err != nil {
		return fmt.Errorf("创建快捷方式目录失败: %v", err)
	}

	var chromePath string
	var err error

	if ec.ChromePath != "" {
		chromePath = ec.ChromePath
	} else {
		chromePath, err = FindChromePath()
		if err != nil {
			return fmt.Errorf("未找到Chrome浏览器: %v", err)
		}
	}

	// 解析窗口编号
	windowNumbers, err := ParseWindowNumbers(numbersStr)
	if err != nil {
		return fmt.Errorf("解析窗口编号失败: %v", err)
	}
	if len(windowNumbers) == 0 {
		return fmt.Errorf("未指定有效的窗口编号")
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errors []string

	// Initialize icon manager once, outside the loop
	iconManager, err := NewOptimizedIconManager()
	if err != nil {
		fmt.Printf("无法初始化图标管理器（将使用默认图标）: %v\n", err)
	}

	for _, num := range windowNumbers {
		wg.Add(1)

		go func(envNum int) {
			defer wg.Done()

			dataDirName := common.ChromeUserDataDirPrefix + strconv.Itoa(envNum)
			dataDir := filepath.Join(ec.CacheDir, dataDirName)

			if err := os.MkdirAll(dataDir, 0755); err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("环境%d: 创建数据目录失败 - %v", envNum, err))
				mu.Unlock()
				return
			}

			// 绝对路径转换
			absDataDir, absErr := filepath.Abs(dataDir)
			targetDataDir := dataDir
			if absErr == nil {
				targetDataDir = absDataDir
			}

			// macOS 参数: 这里的 arguments 仅仅是传递给 --args 的内容
			arguments := buildShortcutArguments(targetDataDir)
			shortcutPath := filepath.Join(ec.ShortcutDir, fmt.Sprintf("%d.app", envNum))

			if err := CreateShortcut(shortcutPath, chromePath, arguments, ""); err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("环境%d: %v", envNum, err))
				mu.Unlock()
				return
			}

			// Add custom icon!
			if iconManager != nil && ec.AutoModifyShortcutIcon {
				if err := iconManager.GenerateAndApplyMacEnvironmentIcon(envNum, shortcutPath); err != nil {
					fmt.Printf("生成环境图标失败（使用默认图标）: %v\n", err)
				}
			}

		}(num)
	}

	wg.Wait()

	if len(errors) > 0 {
		return fmt.Errorf("创建过程中出现错误:\n%s", strings.Join(errors, "\n"))
	}

	return nil
}

func (ec *EnvironmentCreator) refreshShellIcons() error {
	// macOS 不需要刷新图标缓存
	return nil
}

func (ec *EnvironmentCreator) ValidateDirectories() error {
	return nil
}

func (ec *EnvironmentCreator) GetEnvironmentInfo(envNum int) (map[string]string, error) {
	return nil, nil
}

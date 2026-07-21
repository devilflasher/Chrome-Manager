package utils

import (
	"chromemanager/config"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type EnvironmentCreator struct {
	CacheDir    string
	ShortcutDir string
	ChromePath  string // Optional: Custom Browser Path
	BrowserType string // Optional: Browser Type (chrome, edge, opera, brave)
}

func NewEnvironmentCreator(cacheDir, shortcutDir string) *EnvironmentCreator {
	return &EnvironmentCreator{
		CacheDir:    cacheDir,
		ShortcutDir: shortcutDir,
	}
}

func parseEnvironmentNumbers(numbersStr string) []int {
	numbersStr = strings.TrimSpace(numbersStr)
	if numbersStr == "" {
		result := make([]int, 48)
		for i := 0; i < 48; i++ {
			result[i] = i + 1
		}
		return result
	}

	var result []int
	uniqueNumbers := make(map[int]bool)

	parts := strings.Split(numbersStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "-") {
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
				if err1 == nil && err2 == nil && start <= end {
					for i := start; i <= end; i++ {
						if !uniqueNumbers[i] {
							uniqueNumbers[i] = true
							result = append(result, i)
						}
					}
				}
			}
		} else {
			num, err := strconv.Atoi(part)
			if err == nil {
				if !uniqueNumbers[num] {
					uniqueNumbers[num] = true
					result = append(result, num)
				}
			}
		}
	}
	sort.Ints(result)
	return result
}

func CreateShortcut(shortcutPath, targetPath, arguments, workingDir string) error {
	shortcutPath = strings.ReplaceAll(shortcutPath, "/", "\\")
	targetPath = strings.ReplaceAll(targetPath, "/", "\\")
	workingDir = strings.ReplaceAll(workingDir, "/", "\\")
	arguments = strings.ReplaceAll(arguments, "/", "\\")
	vbsScript := fmt.Sprintf(`
Set WshShell = CreateObject("WScript.Shell")
Set Shortcut = WshShell.CreateShortcut("%s")
Shortcut.TargetPath = "%s"
Shortcut.Arguments = "%s"
Shortcut.WorkingDirectory = "%s"
Shortcut.WindowStyle = 1
Shortcut.IconLocation = "%s,0"
Shortcut.Save
`,
		strings.ReplaceAll(shortcutPath, `"`, `""`), // 转义双引号
		strings.ReplaceAll(targetPath, `"`, `""`),   // 转义双引号
		strings.ReplaceAll(arguments, `"`, `""`),    // 转义双引号
		strings.ReplaceAll(workingDir, `"`, `""`),   // 转义双引号
		strings.ReplaceAll(targetPath, `"`, `""`),   // 转义双引号
	)

	uniqueID := fmt.Sprintf("%d_%d_%p", time.Now().UnixNano(), time.Now().Unix(), &shortcutPath)
	tempVBS := filepath.Join(os.TempDir(), fmt.Sprintf("create_shortcut_%s.vbs", uniqueID))
	if err := os.WriteFile(tempVBS, []byte(vbsScript), 0644); err != nil {
		return fmt.Errorf("create vbs failed: %v", err)
	}
	defer os.Remove(tempVBS)
	cmd := fmt.Sprintf(`cmd.exe /c cscript.exe //NoLogo "%s"`, tempVBS)
	kernel32 := windows.NewLazyDLL("kernel32.dll")
	procCreateProcess := kernel32.NewProc("CreateProcessW")

	var si windows.StartupInfo
	var pi windows.ProcessInformation
	si.Cb = uint32(unsafe.Sizeof(si))

	cmdLinePtr, _ := windows.UTF16PtrFromString(cmd)

	const CREATE_NO_WINDOW = 0x08000000

	ret, _, _ := procCreateProcess.Call(
		0,
		uintptr(unsafe.Pointer(cmdLinePtr)),
		0,
		0,
		0,
		CREATE_NO_WINDOW, // 隐藏窗口，静默执行
		0,
		0,
		uintptr(unsafe.Pointer(&si)),
		uintptr(unsafe.Pointer(&pi)),
	)

	if ret == 0 {
		return fmt.Errorf("执行VBScript创建快捷方式失败")
	}

	// 确保进程句柄被正确关闭
	defer func() {
		if pi.Process != 0 {
			windows.CloseHandle(pi.Process)
		}
		if pi.Thread != 0 {
			windows.CloseHandle(pi.Thread)
		}
	}()

	// 等待进程完成，使用更短的超时时间
	timeoutResult, _ := windows.WaitForSingleObject(pi.Process, 5000)

	var exitCode uint32
	windows.GetExitCodeProcess(pi.Process, &exitCode)

	if timeoutResult == uint32(windows.WAIT_TIMEOUT) {
		// 如果超时，强制终止进程
		windows.TerminateProcess(pi.Process, 1)
		return fmt.Errorf("VBScript执行超时，已强制终止")
	}

	if exitCode != 0 {
		return fmt.Errorf("VBScript执行失败，退出码: %d", exitCode)
	}

	// 等待一小段时间，确保文件写入完成
	time.Sleep(100 * time.Millisecond)

	if _, err := os.Stat(shortcutPath); err != nil {
		return fmt.Errorf("快捷方式文件创建失败: %v", err)
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
		browserType := ec.BrowserType
		if browserType == "" {
			browserType = config.BrowserTypeChrome
		}
		chromePath, err = FindBrowserPath(browserType)
		if err != nil {
			return fmt.Errorf("未找到Chrome浏览器: %v", err)
		}
	}

	if _, err := os.Stat(chromePath); err != nil {
		return fmt.Errorf("chrome可执行文件不可访问: %v", err)
	}

	// 解析窗口编号
	windowNumbers := parseEnvironmentNumbers(numbersStr)
	if len(windowNumbers) == 0 {
		return fmt.Errorf("未指定有效的窗口编号")
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	createdCount := 0
	var errors []string

	for _, num := range windowNumbers {
		wg.Add(1)

		go func(envNum int) {
			defer wg.Done()

			dataDirName := strconv.Itoa(envNum)
			dataDir := filepath.Join(ec.CacheDir, dataDirName)

			if err := os.MkdirAll(dataDir, 0755); err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("环境%d: 创建数据目录失败 - %v", envNum, err))
				mu.Unlock()
				return
			}

			// 统一使用基础参数
			arguments := fmt.Sprintf(`--user-data-dir="%s" --no-first-run`, dataDir)
			shortcutPath := filepath.Join(ec.ShortcutDir, fmt.Sprintf("%d.lnk", envNum))
			workingDir := filepath.Dir(chromePath)

			if err := CreateShortcut(shortcutPath, chromePath, arguments, workingDir); err != nil {
				mu.Lock()
				errors = append(errors, fmt.Sprintf("环境%d: %v", envNum, err))
				mu.Unlock()
				return
			}

			mu.Lock()
			createdCount++
			mu.Unlock()
		}(num)
	}

	wg.Wait()

	if err := ec.refreshShellIcons(); err == nil {
		// 刷新成功，静默
	}

	if len(errors) > 0 {
		return fmt.Errorf("创建失败:\n%s", strings.Join(errors, "\n"))
	}

	if createdCount == 0 {
		return fmt.Errorf("未创建任何环境")
	}

	return nil
}

func (ec *EnvironmentCreator) refreshShellIcons() error {
	shell32 := windows.NewLazyDLL("shell32.dll")
	procSHChangeNotify := shell32.NewProc("SHChangeNotify")

	procSHChangeNotify.Call(0x08000000, 0, 0, 0)

	return nil
}

func (ec *EnvironmentCreator) ValidateDirectories() error {
	if ec.CacheDir == "" {
		return fmt.Errorf("缓存目录未配置")
	}

	if ec.ShortcutDir == "" {
		return fmt.Errorf("快捷方式目录未配置")
	}

	testFile := filepath.Join(ec.CacheDir, "test_write_permission.tmp")
	if file, err := os.Create(testFile); err != nil {
		return fmt.Errorf("缓存目录不可写: %v", err)
	} else {
		file.Close()
		os.Remove(testFile)
	}

	testFile = filepath.Join(ec.ShortcutDir, "test_write_permission.tmp")
	if file, err := os.Create(testFile); err != nil {
		return fmt.Errorf("快捷方式目录不可写: %v", err)
	} else {
		file.Close()
		os.Remove(testFile)
	}

	return nil
}

func (ec *EnvironmentCreator) GetEnvironmentInfo(windowNumber int) (map[string]string, error) {
	dataDirName := strconv.Itoa(windowNumber)
	dataDir := filepath.Join(ec.CacheDir, dataDirName)
	shortcutPath := filepath.Join(ec.ShortcutDir, fmt.Sprintf("%d.lnk", windowNumber))

	info := map[string]string{
		"number":       strconv.Itoa(windowNumber),
		"dataDir":      dataDir,
		"shortcutPath": shortcutPath,
	}

	if _, err := os.Stat(dataDir); err == nil {
		info["dataDirExists"] = "true"
	} else {
		info["dataDirExists"] = "false"
	}

	if _, err := os.Stat(shortcutPath); err == nil {
		info["shortcutExists"] = "true"
	} else {
		info["shortcutExists"] = "false"
	}

	return info, nil
}

package utils

import (
	"chromemanager/config"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type BatchOperations struct {
	settings *config.Settings
}

func NewBatchOperations(settings *config.Settings) *BatchOperations {
	return &BatchOperations{
		settings: settings,
	}
}

func (bo *BatchOperations) BatchOpenURL(url string, windowNumbers []int) error {
	if url == "" || len(windowNumbers) == 0 {
		return fmt.Errorf("invalid input")
	}
	normalizedURL := bo.normalizeURL(url)
	chromeProcesses, err := FindChromeProcesses()
	if err != nil || len(chromeProcesses) == 0 {
		return fmt.Errorf("no chrome windows found")
	}
	windowMap := make(map[int]ChromeProcessInfo)
	for _, process := range chromeProcesses {
		windowMap[process.Number] = process
	}

	successCount := 0
	for i, windowNumber := range windowNumbers {
		if process, exists := windowMap[windowNumber]; exists {
			if err := bo.openURLInExistingWindow(normalizedURL, process); err == nil {
				successCount++
			}
		}
		if i < len(windowNumbers)-1 {
			delay := 300 * time.Millisecond
			if bo.settings != nil && bo.settings.WindowOpenSpeed > 0 {
				delay = time.Duration(bo.settings.WindowOpenSpeed*1000) * time.Millisecond
				if delay < 100*time.Millisecond {
					delay = 100 * time.Millisecond
				}
			}
			time.Sleep(delay)
		}
	}
	if successCount == 0 {
		return fmt.Errorf("failed to open URL in any window")
	}
	return nil
}

func (bo *BatchOperations) openURLInExistingWindow(url string, process ChromeProcessInfo) error {
	if process.DebugPort == 0 {
		return fmt.Errorf("no debug port")
	}
	return bo.openURLByDevTools(url, process.DebugPort)
}

func (bo *BatchOperations) openURLByDevTools(url string, debugPort int) error {
	script := fmt.Sprintf(`
		try {
			$uri = "http://localhost:%d/json/new?%s"
			$response = Invoke-RestMethod -Uri $uri -Method PUT -TimeoutSec 5
			if ($response) {
				Write-Host "Success: Created new tab with PUT method"
			} else {
				Write-Host "Error: No response from PUT method"
				exit 1
			}
		} catch {
			Write-Host "Error: $_"
			exit 1
		}
	`, debugPort, url)

	cmd := exec.Command("powershell", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		return bo.openURLByDevToolsDirectURL(url, debugPort)
	}

	if !strings.Contains(outputStr, "Success") {
		return bo.openURLByDevToolsDirectURL(url, debugPort)
	}

	return nil
}

func (bo *BatchOperations) openURLByDevToolsDirectURL(url string, debugPort int) error {
	script := fmt.Sprintf(`
		try {
			# 首先获取所有标签页列表
			$listUri = "http://localhost:%d/json"
			$tabs = Invoke-RestMethod -Uri $listUri -Method GET -TimeoutSec 5
			
			# 如果有标签页，使用第一个标签页导航到新URL
			if ($tabs -and $tabs.Count -gt 0) {
				$tabId = $tabs[0].id
				$navigateUri = "http://localhost:%d/json/runtime/evaluate"
				$body = @{
					expression = "window.location.href = '%s'"
				} | ConvertTo-Json
				$response = Invoke-RestMethod -Uri $navigateUri -Method POST -Body $body -ContentType "application/json" -TimeoutSec 5
				Write-Host "Success: Navigated existing tab to URL"
			} else {
				# 如果没有标签页，尝试创建新标签页
				$newTabUri = "http://localhost:%d/json/new"
				$response = Invoke-RestMethod -Uri $newTabUri -Method PUT -TimeoutSec 5
				if ($response) {
					# 创建成功后导航到指定URL
					$tabId = $response.id
					$navigateUri = "http://localhost:%d/json/runtime/evaluate"
					$body = @{
						expression = "window.location.href = '%s'"
					} | ConvertTo-Json
					$navResponse = Invoke-RestMethod -Uri $navigateUri -Method POST -Body $body -ContentType "application/json" -TimeoutSec 5
					Write-Host "Success: Created new tab and navigated to URL"
				} else {
					Write-Host "Error: Failed to create new tab"
					exit 1
				}
			}
		} catch {
			Write-Host "Error: $_"
			exit 1
		}
	`, debugPort, debugPort, strings.ReplaceAll(url, `'`, `\'`), debugPort, debugPort, strings.ReplaceAll(url, `'`, `\'`))

	cmd := exec.Command("powershell", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(output))

	if err != nil {
		return fmt.Errorf("通过DevTools打开网页失败 (端口 %d): %v, 输出: %s", debugPort, err, outputStr)
	}

	if !strings.Contains(outputStr, "Success") {
		return fmt.Errorf("DevTools API调用失败 (端口 %d): %s", debugPort, outputStr)
	}

	return nil
}

func (bo *BatchOperations) BatchOpenURLToAllWindows(url string) error {
	chromeProcesses, err := FindChromeProcesses()
	if err != nil {
		return fmt.Errorf("查找Chrome窗口失败: %v", err)
	}

	if len(chromeProcesses) == 0 {
		return fmt.Errorf("没有找到运行中的Chrome窗口，请先打开并导入Chrome窗口")
	}

	// 提取所有窗口编号
	var windowNumbers []int
	for _, process := range chromeProcesses {
		windowNumbers = append(windowNumbers, process.Number)
	}

	return bo.BatchOpenURL(url, windowNumbers)
}

func (bo *BatchOperations) normalizeURL(url string) string {
	url = strings.TrimSpace(url)

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

func (bo *BatchOperations) GetPresetURLs() map[string]string {
	if bo.settings != nil && bo.settings.CustomURLs != nil {
		return bo.settings.CustomURLs
	}

	// 返回默认预设网址
	return map[string]string{
		"Google":         "https://www.google.com",
		"百度":             "https://www.baidu.com",
		"GitHub":         "https://www.github.com",
		"Stack Overflow": "https://www.stackoverflow.com",
		"YouTube":        "https://www.youtube.com",
		"Bilibili":       "https://www.bilibili.com",
		"淘宝":             "https://www.taobao.com",
		"京东":             "https://www.jd.com",
	}
}

func (bo *BatchOperations) SaveCustomURL(name, url string) error {
	if name == "" || url == "" {
		return fmt.Errorf("名称和网址不能为空")
	}

	normalizedURL := bo.normalizeURL(url)

	// 如果设置为空，初始化
	if bo.settings.CustomURLs == nil {
		bo.settings.CustomURLs = make(map[string]string)
	}

	bo.settings.CustomURLs[name] = normalizedURL

	return nil
}

func (bo *BatchOperations) DeleteCustomURL(name string) error {
	if bo.settings.CustomURLs == nil {
		return fmt.Errorf("没有自定义网址")
	}

	if _, exists := bo.settings.CustomURLs[name]; !exists {
		return fmt.Errorf("网址 '%s' 不存在", name)
	}

	delete(bo.settings.CustomURLs, name)

	return nil
}

func (bo *BatchOperations) BatchOpenPresetURL(presetName string, windowNumbers []int) error {
	presetURLs := bo.GetPresetURLs()

	url, exists := presetURLs[presetName]
	if !exists {
		return fmt.Errorf("预设网址 '%s' 不存在", presetName)
	}

	return bo.BatchOpenURL(url, windowNumbers)
}

func (bo *BatchOperations) ValidateURL(url string) error {
	if url == "" {
		return fmt.Errorf("网址不能为空")
	}

	normalizedURL := bo.normalizeURL(url)

	if !strings.Contains(normalizedURL, "://") {
		return fmt.Errorf("无效的URL格式")
	}

	return nil
}

package utils

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

func (bo *BatchOperations) BatchOpenURL(url string, windowNumbers []int) error {
	if url == "" || len(windowNumbers) == 0 {
		return fmt.Errorf("invalid input")
	}
	normalizedURL := bo.normalizeURL(url)

	// 1. 获取所有 Chrome 窗口
	chromeProcesses, err := FindChromeProcesses()
	if err != nil || len(chromeProcesses) == 0 {
		return fmt.Errorf("no chrome windows found")
	}

	// 2. 建立映射 map[number]Info
	windowMap := make(map[int]ChromeProcessInfo)
	for _, process := range chromeProcesses {
		windowMap[process.Number] = process
	}

	successCount := 0

	// 3. 遍历请求
	for i, windowNumber := range windowNumbers {
		if process, exists := windowMap[windowNumber]; exists {
			if process.DebugPort > 0 {
				if err := bo.openURLByCDP(normalizedURL, process.DebugPort); err == nil {
					successCount++
				} else {
					log.Printf("[BatchOpen] Failed for window %d (Port %d): %v", windowNumber, process.DebugPort, err)
				}
			}
		}

		// 间隔
		if i < len(windowNumbers)-1 {
			delay := 100 * time.Millisecond
			if bo.settings != nil && bo.settings.WindowOpenSpeed > 0 {
				delay = time.Duration(bo.settings.WindowOpenSpeed*1000) * time.Millisecond
			}
			time.Sleep(delay)
		}
	}

	if successCount == 0 {
		return fmt.Errorf("failed to open URL in any window")
	}
	return nil
}

func (bo *BatchOperations) openURLByCDP(url string, debugPort int) error {
	// 使用 HTTP PUT /json/new?url 创建新标签页
	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, url)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("CDP returned status %d", resp.StatusCode)
	}
	return nil
}

func (bo *BatchOperations) BatchOpenURLToAllWindows(url string) error {
	chromeProcesses, err := FindChromeProcesses()
	if err != nil {
		return err
	}
	var nums []int
	for _, p := range chromeProcesses {
		nums = append(nums, p.Number)
	}
	return bo.BatchOpenURL(url, nums)
}

func (bo *BatchOperations) BatchOpenPresetURL(presetName string, windowNumbers []int) error {
	urls := bo.GetPresetURLs()
	if url, ok := urls[presetName]; ok {
		return bo.BatchOpenURL(url, windowNumbers)
	}
	return fmt.Errorf("preset not found")
}

func (bo *BatchOperations) GetPresetURLs() map[string]string {
	if bo.settings != nil && bo.settings.CustomURLs != nil {
		return bo.settings.CustomURLs
	}
	return map[string]string{
		"Google":         "https://www.google.com",
		"百度":             "https://www.baidu.com",
		"GitHub":         "https://www.github.com",
		"Stack Overflow": "https://www.stackoverflow.com",
		"YouTube":        "https://www.youtube.com",
		"Bilibili":       "https://www.bilibili.com",
	}
}

func (bo *BatchOperations) SaveCustomURL(name, url string) error {
	if bo.settings.CustomURLs == nil {
		bo.settings.CustomURLs = make(map[string]string)
	}
	bo.settings.CustomURLs[name] = bo.normalizeURL(url)
	return nil
}

func (bo *BatchOperations) DeleteCustomURL(name string) error {
	if bo.settings.CustomURLs != nil {
		delete(bo.settings.CustomURLs, name)
	}
	return nil
}

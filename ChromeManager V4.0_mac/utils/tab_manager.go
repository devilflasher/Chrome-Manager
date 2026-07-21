package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type WindowInfoProvider interface {
	GetAllImportedWindows() []WindowInfo
	GetActiveTargetID(pid int32) string
}

type TabManager struct {
	httpClient *http.Client
}

func NewTabManager() *TabManager {
	return &TabManager{
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func (tm *TabManager) KeepOnlyCurrentTab(windowNumbers []int, provider WindowInfoProvider, masterWindowNumber int) error {
	if len(windowNumbers) == 0 {
		return fmt.Errorf("没有选中的窗口")
	}

	if provider == nil {
		return fmt.Errorf("window info provider not initialized")
	}

	// 获取已导入的窗口信息
	allWindows := provider.GetAllImportedWindows()
	if len(allWindows) == 0 {
		return fmt.Errorf("没有已导入的窗口，请先导入窗口")
	}

	// 创建窗口映射
	windowMap := make(map[int]WindowInfo) // value is object not pointer to avoid issues
	for i := range allWindows {
		windowMap[allWindows[i].Number] = allWindows[i]
	}

	// First pass: Find Active URL from the Master Window - This logic is now removed as per instruction.

	// 处理每个选中的窗口
	successCount := 0
	for _, windowNumber := range windowNumbers {
		windowInfo, exists := windowMap[windowNumber]
		if !exists {
			log.Printf("窗口 %d 不存在或未导入，跳过", windowNumber)
			continue
		}

		if !windowInfo.IsRunning {
			log.Printf("窗口 %d 未运行，跳过", windowNumber)
			continue
		}

		if windowInfo.DebugPort == 0 {
			log.Printf("窗口 %d 没有调试端口，跳过", windowNumber)
			continue
		}

		// Fetch accurate Target ID from the Sync Manager via provider
		activeTargetID := provider.GetActiveTargetID(int32(windowInfo.PID))
		if activeTargetID == "" {
			log.Printf("未能通过 CDP 获取窗口 %d 的真实活动 Target ID，回退到无 ID 匹配", windowNumber)
		}

		// 为当前窗口处理标签页
		if err := tm.keepOnlyCurrentTabForWindow(windowInfo.DebugPort, windowNumber, activeTargetID); err != nil {
			log.Printf("窗口 %d 处理失败: %v", windowNumber, err)
		} else {
			successCount++
		}
	}

	if successCount == 0 {
		return fmt.Errorf("没有成功处理任何窗口")
	}

	return nil
}

func (tm *TabManager) KeepOnlyNewTab(windowNumbers []int, provider WindowInfoProvider) (map[int]string, error) {
	if len(windowNumbers) == 0 {
		return nil, fmt.Errorf("没有选中的窗口")
	}

	if provider == nil {
		return nil, fmt.Errorf("window info provider not initialized")
	}

	// 获取已导入的窗口信息
	allWindows := provider.GetAllImportedWindows()
	if len(allWindows) == 0 {
		return nil, fmt.Errorf("没有已导入的窗口，请先导入窗口")
	}

	// 创建窗口映射
	windowMap := make(map[int]*WindowInfo)
	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	// 处理每个选中的窗口
	successCount := 0
	createdTargets := make(map[int]string, len(windowNumbers))
	for _, windowNumber := range windowNumbers {
		windowInfo, exists := windowMap[windowNumber]
		if !exists {
			log.Printf("窗口 %d 不存在或未导入，跳过", windowNumber)
			continue
		}

		if !windowInfo.IsRunning {
			log.Printf("窗口 %d 未运行，跳过", windowNumber)
			continue
		}

		if windowInfo.DebugPort == 0 {
			log.Printf("窗口 %d 没有调试端口，跳过", windowNumber)
			continue
		}

		// 为当前窗口处理标签页
		targetID, err := tm.keepOnlyNewTabForWindow(windowInfo.DebugPort, windowNumber)
		if err != nil {
			log.Printf("窗口 %d 处理失败: %v", windowNumber, err)
		} else {
			successCount++
			createdTargets[int(windowInfo.PID)] = targetID
		}
	}

	if successCount == 0 {
		return nil, fmt.Errorf("没有成功处理任何窗口")
	}

	return createdTargets, nil
}

func (tm *TabManager) keepOnlyCurrentTabForWindow(debugPort int, windowNumber int, activeTargetID string) error {
	tabs, err := tm.getChromeTabsFromDevTools(debugPort)
	if err != nil {
		return fmt.Errorf("无法获取窗口 %d 的标签页: %v", windowNumber, err)
	}

	if len(tabs) <= 1 {
		return nil
	}

	var keepTabId string
	found := false

	// Try to match accurately by Target ID first
	if activeTargetID != "" {
		for _, tab := range tabs {
			if tabMap, ok := tab.(map[string]interface{}); ok {
				id, idExists := tabMap["id"].(string)

				if idExists && id == activeTargetID {
					keepTabId = id
					found = true
					break
				}
			}
		}
	}

	// Fallback to first tab (Chrome /json endpoint puts active tab first usually)
	if !found || keepTabId == "" {
		if keepTabMap, ok := tabs[0].(map[string]interface{}); ok {
			keepTabId, _ = keepTabMap["id"].(string)
		} else {
			return fmt.Errorf("窗口 %d 标签页数据格式错误", windowNumber)
		}
	}

	var tabsToClose []string

	for _, tab := range tabs {
		// We no longer skip i==0 automatically, since the tab to keep might not be index 0
		if tabMap, ok := tab.(map[string]interface{}); ok {
			if tabID, exists := tabMap["id"]; exists {
				// 确保不关闭要保留的标签页
				if fmt.Sprint(tabID) != fmt.Sprint(keepTabId) {
					tabsToClose = append(tabsToClose, fmt.Sprint(tabID))
				}
			}
		}
	}

	for _, tabID := range tabsToClose {
		if err := tm.closeChromeTab(debugPort, tabID); err != nil {
			log.Printf("关闭标签页 %v 失败: %v", tabID, err)
		}
		// 添加短暂延迟，避免过快操作
		time.Sleep(50 * time.Millisecond)
	}

	return nil
}

func (tm *TabManager) keepOnlyNewTabForWindow(debugPort, _ int) (string, error) {
	// 首先获取当前所有标签页 (Old Tabs)
	tabs, err := tm.getChromeTabsFromDevTools(debugPort)
	if err != nil {
		return "", fmt.Errorf("获取标签页列表失败: %v", err)
	}

	// 记录旧标签页的 ID 列表
	var oldTabIDs []string
	for _, tab := range tabs {
		if tabMap, ok := tab.(map[string]interface{}); ok {
			if tabID, exists := tabMap["id"]; exists {
				oldTabIDs = append(oldTabIDs, fmt.Sprint(tabID))
			}
		}
	}

	// 创建新标签页
	newTargetID, err := tm.createNewChromeTab(debugPort, "chrome://newtab/")
	if err != nil {
		return "", fmt.Errorf("创建新标签页失败: %v", err)
	}

	// 关闭所有前面记录的旧标签页，不能重新获取 tabs，否则会把刚新建的标签页也关了
	for _, tabID := range oldTabIDs {
		if err := tm.closeChromeTab(debugPort, tabID); err != nil {
			log.Printf("关闭标签页 %v 失败: %v", tabID, err)
		}
	}

	return newTargetID, nil
}

func (tm *TabManager) getChromeTabsFromDevTools(debugPort int) ([]interface{}, error) {
	// 获取标签页列表
	apiURL := fmt.Sprintf("http://localhost:%d/json", debugPort)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DevTools API返回错误状态 %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var allTabs []interface{}
	if err := json.Unmarshal(body, &allTabs); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	// Filter out only "page" type tabs
	var pageTabs []interface{}
	for _, tab := range allTabs {
		if tabMap, ok := tab.(map[string]interface{}); ok {
			if tabType, exists := tabMap["type"]; exists && tabType == "page" {
				pageTabs = append(pageTabs, tab)
			}
		}
	}

	return pageTabs, nil
}

func (tm *TabManager) closeChromeTab(debugPort int, tabID string) error {

	apiURL := fmt.Sprintf("http://localhost:%d/json/close/%s", debugPort, tabID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		if resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("DevTools API返回错误状态 %d", resp.StatusCode)
	}

	return nil
}

func (tm *TabManager) createNewChromeTab(debugPort int, url string) (string, error) {

	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, url)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("DevTools API返回错误状态 %d", resp.StatusCode)
	}

	var createdTarget struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&createdTarget); err != nil {
		return "", fmt.Errorf("解析DevTools响应失败: %v", err)
	}
	if createdTarget.ID == "" {
		return "", fmt.Errorf("DevTools响应缺少Target ID")
	}
	return createdTarget.ID, nil
}

func (tm *TabManager) GetChromeTabsInfo(debugPort int) ([]map[string]interface{}, error) {
	tabs, err := tm.getChromeTabsFromDevTools(debugPort)
	if err != nil {
		return nil, err
	}

	var tabInfos []map[string]interface{}
	for _, tab := range tabs {
		if tabMap, ok := tab.(map[string]interface{}); ok {
			tabInfos = append(tabInfos, tabMap)
		}
	}

	return tabInfos, nil
}

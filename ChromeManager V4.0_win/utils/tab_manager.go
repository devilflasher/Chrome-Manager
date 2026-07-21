package utils

import (
	"chromemanager/config"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	urlpkg "net/url"
	"sync"
	"time"
)

type WindowInfoProvider interface {
	GetAllImportedWindows() []WindowInfo
}

type WindowInfo struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	HWND      uintptr `json:"hwnd"`
	PID       int32   `json:"pid"`
	DebugPort int     `json:"debugPort"`
	IsRunning bool    `json:"isRunning"`
	IsMaster  bool    `json:"isMaster"`
}

type TabManager struct {
	httpClient *http.Client
}

type ChromeTabInfo struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl,omitempty"`
}

func NewTabManager() *TabManager {
	return &TabManager{
		httpClient: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

func (tm *TabManager) KeepOnlyCurrentTab(windowNumbers []int, provider WindowInfoProvider) error {
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
	windowMap := make(map[int]*WindowInfo)
	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
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

		wg.Add(1)
		go func(wInfo *WindowInfo, wNum int) {
			defer wg.Done()
			if err := tm.keepOnlyCurrentTabForWindow(wInfo.DebugPort, wNum); err != nil {
				log.Printf("窗口 %d 处理失败: %v", wNum, err)
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("窗口 %d 处理失败: %v", wNum, err)
				}
				mu.Unlock()
			} else {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(windowInfo, windowNumber)
	}

	// 等待所有窗口并发清理完毕
	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("没有成功处理任何窗口")
	}

	return nil
}

func (tm *TabManager) KeepOnlyNewTab(windowNumbers []int, provider WindowInfoProvider, browserType string) error {
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
	windowMap := make(map[int]*WindowInfo)
	for i := range allWindows {
		windowMap[allWindows[i].Number] = &allWindows[i]
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	successCount := 0
	newTabURL := browserInitialNewTabURL(browserType)

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

		wg.Add(1)
		go func(wInfo *WindowInfo, wNum int) {
			defer wg.Done()
			if err := tm.keepOnlyNewTabForWindow(wInfo.DebugPort, wNum, newTabURL); err != nil {
				log.Printf("窗口 %d 处理失败: %v", wNum, err)
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("窗口 %d 处理失败: %v", wNum, err)
				}
				mu.Unlock()
			} else {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(windowInfo, windowNumber)
	}

	wg.Wait()

	if successCount == 0 {
		return fmt.Errorf("没有成功处理任何窗口")
	}

	return nil
}

func (tm *TabManager) keepOnlyCurrentTabForWindow(debugPort, _ int) error {
	// 获取窗口的所有标签页
	tabs, err := tm.getChromeTabsFromDevTools(debugPort)
	if err != nil {
		return fmt.Errorf("获取标签页列表失败: %v", err)
	}

	if len(tabs) <= 1 {
		return nil
	}

	// 保留第一个标签页，关闭其他标签页
	keepTabMap, ok := tabs[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("无法解析第一个标签页信息")
	}

	keepTabID, exists := keepTabMap["id"]
	if !exists {
		return fmt.Errorf("无法获取要保留的标签页ID")
	}

	var tabsToClose []string

	for i, tab := range tabs {
		if i == 0 {
			continue // 跳过第一个标签页（要保留的）
		}

		if tabMap, ok := tab.(map[string]interface{}); ok {
			if tabID, exists := tabMap["id"]; exists {
				// 确保不关闭要保留的标签页
				if fmt.Sprint(tabID) != fmt.Sprint(keepTabID) {
					tabsToClose = append(tabsToClose, fmt.Sprint(tabID))
				}
			}
		}
	}

	var closeWg sync.WaitGroup
	for _, tabID := range tabsToClose {
		closeWg.Add(1)
		go func(tid string) {
			defer closeWg.Done()
			if err := tm.closeChromeTab(debugPort, tid); err != nil {
				log.Printf("关闭标签页 %v 失败: %v", tid, err)
			}
		}(tabID)
	}
	closeWg.Wait()

	return nil
}

func (tm *TabManager) keepOnlyNewTabForWindow(debugPort, _ int, newTabURL string) error {
	// 首先获取当前所有标签页
	tabs, err := tm.getChromeTabsFromDevTools(debugPort)
	if err != nil {
		return fmt.Errorf("获取标签页列表失败: %v", err)
	}

	// 创建新标签页
	if err := tm.createNewChromeTab(debugPort, newTabURL); err != nil {
		return fmt.Errorf("创建新标签页失败: %v", err)
	}

	var closeWg sync.WaitGroup
	for _, tab := range tabs {
		if tabMap, ok := tab.(map[string]interface{}); ok {
			if tabID, exists := tabMap["id"]; exists {
				closeWg.Add(1)
				go func(tid string) {
					defer closeWg.Done()
					if err := tm.closeChromeTab(debugPort, tid); err != nil {
						log.Printf("关闭标签页 %v 失败: %v", tid, err)
					}
				}(fmt.Sprint(tabID))
			}
		}
	}
	closeWg.Wait()

	return nil
}

func browserInitialNewTabURL(browserType string) string {
	switch browserType {
	case config.BrowserTypeEdge:
		return "edge://newtab/"
	case config.BrowserTypeOpera:
		return "opera://startpage/"
	case config.BrowserTypeBrave:
		return "brave://newtab/"
	default:
		return "chrome://newtab/"
	}
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

	// 解析响应
	var tabs []interface{}
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	// 只返回页面类型的标签页
	var pageTabs []interface{}
	for _, tab := range tabs {
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
		return fmt.Errorf("DevTools API返回错误状态 %d", resp.StatusCode)
	}

	return nil
}

func (tm *TabManager) createNewChromeTab(debugPort int, url string) error {

	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, urlpkg.QueryEscape(url))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("DevTools API返回错误状态 %d", resp.StatusCode)
	}

	return nil
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

func (tm *TabManager) GetPageTabs(debugPort int) ([]ChromeTabInfo, error) {
	return tm.GetPageTabsWithTimeout(debugPort, 2*time.Second)
}

func (tm *TabManager) GetPageTabsWithTimeout(debugPort int, timeout time.Duration) ([]ChromeTabInfo, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	apiURL := fmt.Sprintf("http://localhost:%d/json", debugPort)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create tabs request: %w", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request tabs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devtools tabs API returned status %d", resp.StatusCode)
	}

	var tabs []ChromeTabInfo
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, fmt.Errorf("failed to decode tabs response: %w", err)
	}

	pageTabs := make([]ChromeTabInfo, 0, len(tabs))
	for _, tab := range tabs {
		if tab.Type == "page" {
			pageTabs = append(pageTabs, tab)
		}
	}

	return pageTabs, nil
}

func (tm *TabManager) GetPageTabCount(debugPort int) (int, error) {
	return tm.GetPageTabCountWithTimeout(debugPort, 2*time.Second)
}

func (tm *TabManager) GetPageTabCountWithTimeout(debugPort int, timeout time.Duration) (int, error) {
	tabs, err := tm.GetPageTabsWithTimeout(debugPort, timeout)
	if err != nil {
		return 0, err
	}

	return len(tabs), nil
}

func (tm *TabManager) CreateNewTab(debugPort int, targetURL string) (*ChromeTabInfo, error) {
	if targetURL == "" {
		targetURL = "about:blank"
	}

	apiURL := fmt.Sprintf("http://localhost:%d/json/new?%s", debugPort, urlpkg.QueryEscape(targetURL))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "PUT", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create new tab request: %w", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to create new tab: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("devtools new tab API returned status %d", resp.StatusCode)
	}

	var tab ChromeTabInfo
	if err := json.NewDecoder(resp.Body).Decode(&tab); err != nil {
		return nil, fmt.Errorf("failed to decode new tab response: %w", err)
	}

	return &tab, nil
}

func (tm *TabManager) ActivateTab(debugPort int, tabID string) error {
	if tabID == "" {
		return fmt.Errorf("tab ID is empty")
	}

	apiURL := fmt.Sprintf("http://localhost:%d/json/activate/%s", debugPort, tabID)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create activate tab request: %w", err)
	}

	resp, err := tm.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to activate tab: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("devtools activate tab API returned status %d", resp.StatusCode)
	}

	return nil
}

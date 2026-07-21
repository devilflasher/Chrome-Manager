package main

import (
	"chromemanager/config"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type browserCacheTargetScope string

const (
	browserCacheTargetRoot    browserCacheTargetScope = "root"
	browserCacheTargetProfile browserCacheTargetScope = "profile"
)

type browserCacheTarget struct {
	Name         string
	RelativePath string
	Scope        browserCacheTargetScope
}

type BrowserCacheDetail struct {
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
	Bytes        int64  `json:"bytes"`
}

type BrowserCacheProfileResult struct {
	WindowNumber int                  `json:"windowNumber"`
	UserDataDir  string               `json:"userDataDir"`
	Bytes        int64                `json:"bytes"`
	Running      bool                 `json:"running"`
	Exists       bool                 `json:"exists"`
	CanClean     bool                 `json:"canClean"`
	Status       string               `json:"status"`
	Details      []BrowserCacheDetail `json:"details"`
}

type BrowserCacheScanResult struct {
	ScannedProfiles   int                         `json:"scannedProfiles"`
	CleanableProfiles int                         `json:"cleanableProfiles"`
	SkippedRunning    int                         `json:"skippedRunning"`
	MissingProfiles   int                         `json:"missingProfiles"`
	TotalBytes        int64                       `json:"totalBytes"`
	SafeTargets       []string                    `json:"safeTargets"`
	Profiles          []BrowserCacheProfileResult `json:"profiles"`
}

type BrowserCacheCleanResult struct {
	RequestedProfiles int                         `json:"requestedProfiles"`
	CleanedProfiles   int                         `json:"cleanedProfiles"`
	SkippedRunning    int                         `json:"skippedRunning"`
	MissingProfiles   int                         `json:"missingProfiles"`
	FailedProfiles    int                         `json:"failedProfiles"`
	FreedBytes        int64                       `json:"freedBytes"`
	Profiles          []BrowserCacheProfileResult `json:"profiles"`
}

type importedBrowserProfile struct {
	WindowNumber int
	UserDataDir  string
}

var browserCacheTargets = []browserCacheTarget{
	{Name: "页面缓存", RelativePath: "Cache", Scope: browserCacheTargetProfile},
	{Name: "代码缓存", RelativePath: "Code Cache", Scope: browserCacheTargetProfile},
	{Name: "GPU 缓存", RelativePath: "GPUCache", Scope: browserCacheTargetProfile},
	{Name: "Dawn 缓存", RelativePath: "DawnCache", Scope: browserCacheTargetProfile},
	{Name: "GrShader 缓存", RelativePath: "GrShaderCache", Scope: browserCacheTargetProfile},
	{Name: "Shader 缓存", RelativePath: "ShaderCache", Scope: browserCacheTargetProfile},
	{Name: "媒体缓存", RelativePath: "Media Cache", Scope: browserCacheTargetProfile},
	{Name: "崩溃报告缓存", RelativePath: "Crashpad", Scope: browserCacheTargetRoot},
	{Name: "浏览器指标缓存", RelativePath: "BrowserMetrics", Scope: browserCacheTargetRoot},
	{Name: "优化提示缓存", RelativePath: "OptimizationHints", Scope: browserCacheTargetRoot},
	{Name: "组件 CRX 缓存", RelativePath: "Component CRX Cache", Scope: browserCacheTargetRoot},
}

func browserCacheWorkerLimit() int {
	limit := runtime.NumCPU()
	if limit < 2 {
		limit = 2
	}
	if limit > 4 {
		limit = 4
	}
	return limit
}

func browserCacheSafeTargetList() []string {
	targets := make([]string, 0, len(browserCacheTargets))
	for _, target := range browserCacheTargets {
		if target.Scope == browserCacheTargetProfile {
			targets = append(targets, filepath.Join("<profile>", target.RelativePath))
			continue
		}
		targets = append(targets, target.RelativePath)
	}
	return targets
}

func isChromeProfileDirectoryName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	switch {
	case normalized == "default":
		return true
	case normalized == "guest profile":
		return true
	case normalized == "system profile":
		return true
	case strings.HasPrefix(normalized, "profile "):
		return true
	default:
		return false
	}
}

func resolveBrowserCacheLayout(userDataDir string) (string, []string) {
	rootDir := normalizeUserDataDir(userDataDir)
	if rootDir == "" {
		return "", nil
	}

	rootInfo, err := os.Stat(rootDir)
	if err != nil || !rootInfo.IsDir() {
		return rootDir, nil
	}

	baseName := filepath.Base(rootDir)
	if isChromeProfileDirectoryName(baseName) {
		return filepath.Dir(rootDir), []string{rootDir}
	}

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		defaultProfile := filepath.Join(rootDir, "Default")
		if info, statErr := os.Stat(defaultProfile); statErr == nil && info.IsDir() {
			return rootDir, []string{defaultProfile}
		}
		return rootDir, nil
	}

	profileDirs := make([]string, 0, 4)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isChromeProfileDirectoryName(entry.Name()) {
			profileDirs = append(profileDirs, filepath.Join(rootDir, entry.Name()))
		}
	}

	sort.Strings(profileDirs)
	return rootDir, profileDirs
}

func normalizeBrowserCacheRoot(userDataDir string) string {
	rootDir, _ := resolveBrowserCacheLayout(userDataDir)
	if rootDir != "" {
		return rootDir
	}
	return normalizeUserDataDir(userDataDir)
}

func directorySize(path string) (int64, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !info.IsDir() {
		return 0, false, nil
	}

	var total int64
	walkErr := filepath.Walk(path, func(_ string, fileInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if fileInfo.Mode().IsRegular() {
			total += fileInfo.Size()
		}
		return nil
	})
	if walkErr != nil {
		return 0, true, walkErr
	}

	return total, true, nil
}

func (c *ChromeService) listImportedBrowserProfiles() []importedBrowserProfile {
	if c.syncManager == nil {
		return nil
	}

	imported := c.syncManager.GetAllImportedWindows()
	if len(imported) == 0 {
		return nil
	}

	seen := make(map[string]importedBrowserProfile)
	for _, window := range imported {
		normalized := normalizeBrowserCacheRoot(window.UserDataDir)
		if normalized == "" {
			continue
		}
		current := importedBrowserProfile{
			WindowNumber: window.Number,
			UserDataDir:  normalized,
		}
		existing, exists := seen[normalized]
		if !exists || current.WindowNumber < existing.WindowNumber {
			seen[normalized] = current
		}
	}

	profiles := make([]importedBrowserProfile, 0, len(seen))
	for _, profile := range seen {
		profiles = append(profiles, profile)
	}

	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].WindowNumber == profiles[j].WindowNumber {
			return profiles[i].UserDataDir < profiles[j].UserDataDir
		}
		return profiles[i].WindowNumber < profiles[j].WindowNumber
	})

	return profiles
}

func (c *ChromeService) resolveGroupCacheDir(groupName string) (string, error) {
	settings, err := c.loadSettings()
	if err != nil {
		return "", err
	}

	trimmedName := strings.TrimSpace(groupName)
	if trimmedName == "" || trimmedName == "默认分组" {
		cacheDir := config.GetGroupCacheDir(settings)
		if cacheDir == "" {
			return "", fmt.Errorf("当前分组未配置缓存目录")
		}
		return cacheDir, nil
	}

	for _, group := range settings.Groups {
		if group.Name != trimmedName {
			continue
		}
		if strings.TrimSpace(group.CacheDir) == "" {
			return "", fmt.Errorf("分组 %s 未配置缓存目录", trimmedName)
		}
		return group.CacheDir, nil
	}

	return "", fmt.Errorf("未找到分组: %s", trimmedName)
}

func (c *ChromeService) resolveBrowserCacheProfiles(groupName, windowNumbers string) ([]importedBrowserProfile, error) {
	if c.provider == nil {
		return nil, fmt.Errorf("provider not initialized")
	}

	cacheDir, err := c.resolveGroupCacheDir(groupName)
	if err != nil {
		return nil, err
	}

	numbers, err := c.provider.ParseWindowNumbers(windowNumbers)
	if err != nil {
		return nil, fmt.Errorf("窗口编号格式无效: %v", err)
	}
	if len(numbers) == 0 {
		return nil, fmt.Errorf("请输入要扫描的窗口编号")
	}

	profiles := make([]importedBrowserProfile, 0, len(numbers))
	for _, number := range numbers {
		userDataDir := filepath.Join(cacheDir, strconv.Itoa(number))
		profiles = append(profiles, importedBrowserProfile{
			WindowNumber: number,
			UserDataDir:  normalizeBrowserCacheRoot(userDataDir),
		})
	}

	return profiles, nil
}

func scanBrowserCacheProfile(userDataDir string, windowNumber int, running bool) BrowserCacheProfileResult {
	result := BrowserCacheProfileResult{
		WindowNumber: windowNumber,
		UserDataDir:  normalizeUserDataDir(userDataDir),
		Running:      running,
		Exists:       false,
		CanClean:     false,
		Status:       "环境目录不存在",
		Details:      make([]BrowserCacheDetail, 0, len(browserCacheTargets)),
	}

	rootDir, profileDirs := resolveBrowserCacheLayout(result.UserDataDir)
	if rootDir == "" {
		return result
	}

	if info, err := os.Stat(rootDir); err == nil && info.IsDir() {
		result.Exists = true
	}
	if !result.Exists {
		return result
	}

	for _, target := range browserCacheTargets {
		switch target.Scope {
		case browserCacheTargetRoot:
			targetPath := filepath.Join(rootDir, target.RelativePath)
			size, exists, err := directorySize(targetPath)
			if err != nil {
				log.Printf("scan browser cache failed for %s: %v", targetPath, err)
				continue
			}
			if !exists || size == 0 {
				continue
			}
			result.Bytes += size
			result.Details = append(result.Details, BrowserCacheDetail{
				Name:         target.Name,
				RelativePath: target.RelativePath,
				Bytes:        size,
			})
		case browserCacheTargetProfile:
			for _, profileDir := range profileDirs {
				relativeProfile, err := filepath.Rel(rootDir, profileDir)
				if err != nil {
					relativeProfile = filepath.Base(profileDir)
				}
				relativePath := filepath.Join(relativeProfile, target.RelativePath)
				targetPath := filepath.Join(profileDir, target.RelativePath)
				size, exists, err := directorySize(targetPath)
				if err != nil {
					log.Printf("scan browser cache failed for %s: %v", targetPath, err)
					continue
				}
				if !exists || size == 0 {
					continue
				}
				result.Bytes += size
				result.Details = append(result.Details, BrowserCacheDetail{
					Name:         target.Name,
					RelativePath: relativePath,
					Bytes:        size,
				})
			}
		}
	}

	switch {
	case running:
		result.Status = "浏览器仍在运行，已跳过"
	case result.Bytes == 0:
		result.Status = "未发现可清理缓存"
	default:
		result.Status = "可清理"
		result.CanClean = true
	}

	return result
}

func (c *ChromeService) ScanBrowserCaches(groupName, windowNumbers string) (*BrowserCacheScanResult, error) {
	profiles, err := c.resolveBrowserCacheProfiles(groupName, windowNumbers)
	if err != nil {
		return nil, err
	}

	result := &BrowserCacheScanResult{
		SafeTargets: browserCacheSafeTargetList(),
		Profiles:    make([]BrowserCacheProfileResult, 0, len(profiles)),
	}

	if len(profiles) == 0 {
		return result, nil
	}

	results := make([]BrowserCacheProfileResult, len(profiles))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, browserCacheWorkerLimit())

	for index, profile := range profiles {
		wg.Add(1)
		go func(i int, current importedBrowserProfile) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			running := c.isProfileStillInUse(current.UserDataDir)
			if !running {
				running = c.isProfileStillInUse(normalizeBrowserCacheRoot(current.UserDataDir))
			}
			results[i] = scanBrowserCacheProfile(current.UserDataDir, current.WindowNumber, running)
		}(index, profile)
	}

	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].WindowNumber == results[j].WindowNumber {
			return results[i].UserDataDir < results[j].UserDataDir
		}
		return results[i].WindowNumber < results[j].WindowNumber
	})

	result.Profiles = results
	result.ScannedProfiles = len(results)
	for _, profile := range results {
		if profile.CanClean {
			result.CleanableProfiles++
			result.TotalBytes += profile.Bytes
		}
		if profile.Running {
			result.SkippedRunning++
		}
		if !profile.Exists {
			result.MissingProfiles++
		}
	}

	return result, nil
}

func browserCacheCleanupTargets(userDataDir string) ([]BrowserCacheDetail, error) {
	rootDir, profileDirs := resolveBrowserCacheLayout(userDataDir)
	if rootDir == "" {
		return nil, nil
	}

	details := make([]BrowserCacheDetail, 0, len(browserCacheTargets))
	for _, target := range browserCacheTargets {
		switch target.Scope {
		case browserCacheTargetRoot:
			targetPath := filepath.Join(rootDir, target.RelativePath)
			size, exists, err := directorySize(targetPath)
			if err != nil {
				return nil, err
			}
			if !exists || size == 0 {
				continue
			}
			details = append(details, BrowserCacheDetail{
				Name:         target.Name,
				RelativePath: target.RelativePath,
				Bytes:        size,
			})
		case browserCacheTargetProfile:
			for _, profileDir := range profileDirs {
				targetPath := filepath.Join(profileDir, target.RelativePath)
				size, exists, err := directorySize(targetPath)
				if err != nil {
					return nil, err
				}
				if !exists || size == 0 {
					continue
				}
				relativeProfile, err := filepath.Rel(rootDir, profileDir)
				if err != nil {
					relativeProfile = filepath.Base(profileDir)
				}
				details = append(details, BrowserCacheDetail{
					Name:         target.Name,
					RelativePath: filepath.Join(relativeProfile, target.RelativePath),
					Bytes:        size,
				})
			}
		}
	}

	return details, nil
}

func cleanBrowserCacheTarget(rootDir, relativePath string) error {
	cleanRoot := normalizeUserDataDir(rootDir)
	if cleanRoot == "" {
		return fmt.Errorf("empty browser root")
	}

	targetPath := filepath.Join(cleanRoot, relativePath)
	cleanTarget := filepath.Clean(targetPath)
	if cleanTarget == cleanRoot {
		return fmt.Errorf("refusing to remove browser root directly")
	}

	relative, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return err
	}
	if relative == "." || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("target escapes browser root: %s", cleanTarget)
	}

	if _, err := os.Stat(cleanTarget); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return os.RemoveAll(cleanTarget)
}

func (c *ChromeService) CleanBrowserCaches(groupName, windowNumbers string) (*BrowserCacheCleanResult, error) {
	profiles, err := c.resolveBrowserCacheProfiles(groupName, windowNumbers)
	if err != nil {
		return nil, err
	}

	result := &BrowserCacheCleanResult{
		RequestedProfiles: len(profiles),
		Profiles:          make([]BrowserCacheProfileResult, 0, len(profiles)),
	}

	if len(profiles) == 0 {
		return result, nil
	}

	results := make([]BrowserCacheProfileResult, len(profiles))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, browserCacheWorkerLimit())

	for index, profile := range profiles {
		wg.Add(1)
		go func(i int, current importedBrowserProfile) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			profileResult := BrowserCacheProfileResult{
				WindowNumber: current.WindowNumber,
				UserDataDir:  current.UserDataDir,
				Exists:       false,
				CanClean:     false,
				Status:       "环境目录不存在",
				Details:      []BrowserCacheDetail{},
			}

			rootDir, _ := resolveBrowserCacheLayout(current.UserDataDir)
			if rootDir == "" {
				results[i] = profileResult
				return
			}

			if info, err := os.Stat(rootDir); err == nil && info.IsDir() {
				profileResult.Exists = true
			}
			if !profileResult.Exists {
				results[i] = profileResult
				return
			}

			running := c.isProfileStillInUse(current.UserDataDir)
			if !running {
				running = c.isProfileStillInUse(normalizeBrowserCacheRoot(current.UserDataDir))
			}
			profileResult.Running = running
			if running {
				profileResult.Status = "浏览器仍在运行，已跳过"
				results[i] = profileResult
				return
			}

			details, err := browserCacheCleanupTargets(current.UserDataDir)
			if err != nil {
				profileResult.Status = "扫描缓存失败"
				results[i] = profileResult
				return
			}

			profileResult.Details = details
			for _, detail := range details {
				profileResult.Bytes += detail.Bytes
			}
			if profileResult.Bytes == 0 {
				profileResult.Status = "未发现可清理缓存"
				results[i] = profileResult
				return
			}

			for _, detail := range details {
				if err := cleanBrowserCacheTarget(rootDir, detail.RelativePath); err != nil {
					profileResult.Status = "清理失败: " + err.Error()
					results[i] = profileResult
					return
				}
			}

			profileResult.CanClean = false
			profileResult.Status = "已清理"
			results[i] = profileResult
		}(index, profile)
	}

	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		if results[i].WindowNumber == results[j].WindowNumber {
			return results[i].UserDataDir < results[j].UserDataDir
		}
		return results[i].WindowNumber < results[j].WindowNumber
	})

	result.Profiles = results
	for _, profile := range results {
		switch {
		case !profile.Exists:
			result.MissingProfiles++
		case profile.Running:
			result.SkippedRunning++
		case strings.HasPrefix(profile.Status, "清理失败"):
			result.FailedProfiles++
		case profile.Status == "已清理":
			result.CleanedProfiles++
			result.FreedBytes += profile.Bytes
		}
	}

	return result, nil
}

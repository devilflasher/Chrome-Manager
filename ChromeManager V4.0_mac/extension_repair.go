package main

import (
	"chromemanager/config"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ExtensionRepairTargetResult struct {
	WindowNumber int      `json:"windowNumber"`
	UserDataDir  string   `json:"userDataDir"`
	Exists       bool     `json:"exists"`
	Running      bool     `json:"running"`
	Repaired     bool     `json:"repaired"`
	Status       string   `json:"status"`
	ExtensionIDs []string `json:"extensionIds"`
}

type ExtensionRepairResult struct {
	GroupName         string                        `json:"groupName"`
	DonorWindowNumber int                           `json:"donorWindowNumber"`
	DonorUserDataDir  string                        `json:"donorUserDataDir"`
	ExtensionIDs      []string                      `json:"extensionIds"`
	RequestedTargets  int                           `json:"requestedTargets"`
	RepairedTargets   int                           `json:"repairedTargets"`
	SkippedRunning    int                           `json:"skippedRunning"`
	MissingTargets    int                           `json:"missingTargets"`
	FailedTargets     int                           `json:"failedTargets"`
	Targets           []ExtensionRepairTargetResult `json:"targets"`
}

type extensionPackageSnapshot struct {
	RelativeProfile string
	ExtensionID     string
	SourcePath      string
}

func extensionRepairWorkerLimit() int {
	limit := browserCacheWorkerLimit()
	if limit < 2 {
		return 2
	}
	return limit
}

func copyFileContents(srcPath, dstPath string, fileMode os.FileMode) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Sync()
}

func copyDirectoryRecursive(srcDir, dstDir string) error {
	srcInfo, err := os.Stat(srcDir)
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source is not a directory: %s", srcDir)
	}

	if err := os.MkdirAll(dstDir, srcInfo.Mode().Perm()); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		info, err := entry.Info()
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if err := copyDirectoryRecursive(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		if err := copyFileContents(srcPath, dstPath, info.Mode().Perm()); err != nil {
			return err
		}
	}

	return nil
}

func ensurePathWithinRoot(rootDir, targetPath string) error {
	cleanRoot := normalizeUserDataDir(rootDir)
	cleanTarget := normalizeUserDataDir(targetPath)
	if cleanRoot == "" || cleanTarget == "" {
		return fmt.Errorf("invalid path")
	}

	relative, err := filepath.Rel(cleanRoot, cleanTarget)
	if err != nil {
		return err
	}
	if relative == "." || strings.HasPrefix(relative, "..") {
		return fmt.Errorf("target escapes profile root: %s", cleanTarget)
	}
	return nil
}

func collectExtensionPackageSnapshots(userDataDir string) ([]extensionPackageSnapshot, []string, error) {
	rootDir, profileDirs := resolveBrowserCacheLayout(userDataDir)
	if rootDir == "" {
		return nil, nil, fmt.Errorf("environment directory not found")
	}

	snapshots := make([]extensionPackageSnapshot, 0, 16)
	extensionIDs := make(map[string]struct{})

	for _, profileDir := range profileDirs {
		extensionsDir := filepath.Join(profileDir, "Extensions")
		entries, err := os.ReadDir(extensionsDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, err
		}

		relativeProfile, err := filepath.Rel(rootDir, profileDir)
		if err != nil {
			relativeProfile = filepath.Base(profileDir)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			extensionID := strings.TrimSpace(entry.Name())
			if extensionID == "" {
				continue
			}

			sourcePath := filepath.Join(extensionsDir, extensionID)
			if err := ensurePathWithinRoot(rootDir, sourcePath); err != nil {
				return nil, nil, err
			}

			snapshots = append(snapshots, extensionPackageSnapshot{
				RelativeProfile: relativeProfile,
				ExtensionID:     extensionID,
				SourcePath:      sourcePath,
			})
			extensionIDs[extensionID] = struct{}{}
		}
	}

	if len(snapshots) == 0 {
		return nil, nil, fmt.Errorf("donor environment does not contain any extension packages")
	}

	idList := make([]string, 0, len(extensionIDs))
	for extensionID := range extensionIDs {
		idList = append(idList, extensionID)
	}
	sort.Strings(idList)

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].RelativeProfile == snapshots[j].RelativeProfile {
			return snapshots[i].ExtensionID < snapshots[j].ExtensionID
		}
		return snapshots[i].RelativeProfile < snapshots[j].RelativeProfile
	})

	return snapshots, idList, nil
}

func repairExtensionPackagesForTarget(userDataDir string, windowNumber int, snapshots []extensionPackageSnapshot, extensionIDs []string, running bool) ExtensionRepairTargetResult {
	result := ExtensionRepairTargetResult{
		WindowNumber: windowNumber,
		UserDataDir:  normalizeUserDataDir(userDataDir),
		Exists:       false,
		Running:      running,
		Repaired:     false,
		Status:       "环境目录不存在",
		ExtensionIDs: []string{},
	}

	rootDir, _ := resolveBrowserCacheLayout(result.UserDataDir)
	if rootDir == "" {
		return result
	}

	if info, err := os.Stat(rootDir); err == nil && info.IsDir() {
		result.Exists = true
	}
	if !result.Exists {
		return result
	}

	if running {
		result.Status = "浏览器仍在运行，已跳过"
		return result
	}

	repairedIDs := make([]string, 0, len(extensionIDs))
	for _, snapshot := range snapshots {
		targetExtensionsDir := filepath.Join(rootDir, snapshot.RelativeProfile, "Extensions")
		targetExtensionDir := filepath.Join(targetExtensionsDir, snapshot.ExtensionID)

		if err := ensurePathWithinRoot(rootDir, targetExtensionsDir); err != nil {
			result.Status = "创建扩展目录失败: " + err.Error()
			return result
		}
		if err := ensurePathWithinRoot(rootDir, targetExtensionDir); err != nil {
			result.Status = "复制扩展文件失败: " + err.Error()
			return result
		}

		if err := os.MkdirAll(targetExtensionsDir, 0o755); err != nil {
			result.Status = "创建扩展目录失败: " + err.Error()
			return result
		}

		if err := os.RemoveAll(targetExtensionDir); err != nil {
			result.Status = "清理旧扩展目录失败: " + err.Error()
			return result
		}

		if err := copyDirectoryRecursive(snapshot.SourcePath, targetExtensionDir); err != nil {
			result.Status = "复制扩展文件失败: " + err.Error()
			return result
		}

		repairedIDs = append(repairedIDs, snapshot.ExtensionID)
	}

	sort.Strings(repairedIDs)
	result.ExtensionIDs = repairedIDs
	result.Repaired = len(repairedIDs) > 0
	if result.Repaired {
		result.Status = "扩展安装文件已恢复，请重新安装官方插件"
	}

	return result
}

func (c *ChromeService) RepairExtensionsFromDonor(targetWindowNumbers string, donorWindowNumber int) (*ExtensionRepairResult, error) {
	if c.provider == nil {
		return nil, fmt.Errorf("provider not initialized")
	}

	settings, err := c.loadSettings()
	if err != nil {
		return nil, err
	}

	groupName := currentGroupName(settings)

	cacheDir, err := c.resolveGroupCacheDir(groupName)
	if err != nil {
		return nil, err
	}

	if donorWindowNumber <= 0 {
		return nil, fmt.Errorf("请输入有效的克隆环境编号")
	}

	targetNumbers, err := c.provider.ParseWindowNumbers(targetWindowNumbers)
	if err != nil {
		return nil, fmt.Errorf("受损窗口编号格式无效: %v", err)
	}
	if len(targetNumbers) == 0 {
		return nil, fmt.Errorf("请输入要修复的受损窗口编号")
	}

	filteredTargets := make([]int, 0, len(targetNumbers))
	seenTargets := make(map[int]struct{}, len(targetNumbers))
	for _, number := range targetNumbers {
		if number <= 0 || number == donorWindowNumber {
			continue
		}
		if _, exists := seenTargets[number]; exists {
			continue
		}
		seenTargets[number] = struct{}{}
		filteredTargets = append(filteredTargets, number)
	}
	if len(filteredTargets) == 0 {
		return nil, fmt.Errorf("请至少输入一个与克隆环境不同的受损窗口编号")
	}

	donorUserDataDir := normalizeBrowserCacheRoot(filepath.Join(cacheDir, strconv.Itoa(donorWindowNumber)))
	if donorUserDataDir == "" {
		return nil, fmt.Errorf("无法解析克隆环境目录")
	}

	donorRoot, _ := resolveBrowserCacheLayout(donorUserDataDir)
	if donorRoot == "" {
		return nil, fmt.Errorf("克隆环境目录不存在")
	}

	if c.isProfileStillInUse(donorUserDataDir) || c.isProfileStillInUse(donorRoot) {
		return nil, fmt.Errorf("克隆环境仍在运行，请先关闭 donor 窗口后再修复")
	}

	snapshots, extensionIDs, err := collectExtensionPackageSnapshots(donorUserDataDir)
	if err != nil {
		return nil, err
	}

	result := &ExtensionRepairResult{
		GroupName:         groupName,
		DonorWindowNumber: donorWindowNumber,
		DonorUserDataDir:  donorUserDataDir,
		ExtensionIDs:      extensionIDs,
		RequestedTargets:  len(filteredTargets),
		Targets:           make([]ExtensionRepairTargetResult, len(filteredTargets)),
	}

	var wg sync.WaitGroup
	semaphore := make(chan struct{}, extensionRepairWorkerLimit())

	for index, targetNumber := range filteredTargets {
		wg.Add(1)
		go func(i int, currentWindowNumber int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			targetUserDataDir := normalizeBrowserCacheRoot(filepath.Join(cacheDir, strconv.Itoa(currentWindowNumber)))
			running := c.isProfileStillInUse(targetUserDataDir)
			if !running {
				targetRoot, _ := resolveBrowserCacheLayout(targetUserDataDir)
				if targetRoot != "" {
					running = c.isProfileStillInUse(targetRoot)
				}
			}

			result.Targets[i] = repairExtensionPackagesForTarget(targetUserDataDir, currentWindowNumber, snapshots, extensionIDs, running)
		}(index, targetNumber)
	}

	wg.Wait()

	sort.Slice(result.Targets, func(i, j int) bool {
		return result.Targets[i].WindowNumber < result.Targets[j].WindowNumber
	})

	for _, target := range result.Targets {
		switch {
		case !target.Exists:
			result.MissingTargets++
		case target.Running:
			result.SkippedRunning++
		case target.Repaired:
			result.RepairedTargets++
		default:
			result.FailedTargets++
		}
	}

	if result.RepairedTargets == 0 && result.FailedTargets == 0 && result.MissingTargets == 0 && result.SkippedRunning > 0 {
		return result, fmt.Errorf("所有目标环境仍在运行，请先关闭后再修复")
	}
	return result, nil
}

func currentGroupName(settings *config.Settings) string {
	if settings == nil {
		return "默认分组"
	}
	name := strings.TrimSpace(settings.CurrentGroup)
	if name == "" {
		return "默认分组"
	}
	return name
}

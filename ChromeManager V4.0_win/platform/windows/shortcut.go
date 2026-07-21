//go:build windows

package windows

import (
	"chromemanager/platform/common"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	lnk "github.com/parsiya/golnk"
)

// ParseShortcutsFromDirectory 从目录中解析快捷方式
// 从 utils.ParseShortcutsFromDirectory 迁移 ✅
func (p *Provider) ParseShortcutsFromDirectory(shortcutDir string) ([]common.ShortcutInfo, error) {
	var shortcuts []common.ShortcutInfo

	if shortcutDir == "" {
		return shortcuts, fmt.Errorf("shortcut directory is empty")
	}

	// 确保快捷方式目录存在
	if _, err := os.Stat(shortcutDir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(shortcutDir, 0755); err != nil {
			return shortcuts, fmt.Errorf("创建快捷方式目录失败: %v", err)
		}
	}

	// 查找所有.lnk文件
	files, err := filepath.Glob(filepath.Join(shortcutDir, "*.lnk"))
	if err != nil {
		return shortcuts, fmt.Errorf("failed to glob shortcut files: %v", err)
	}

	for _, file := range files {
		shortcut, err := p.parseShortcut(file)
		if err != nil {
			log.Printf("Failed to parse shortcut %s: %v", file, err)
			continue
		}

		if shortcut.Number > 0 {
			shortcuts = append(shortcuts, shortcut)
		}
	}

	return shortcuts, nil
}

// parseShortcut 解析单个快捷方式文件
// 从 utils.parseShortcut 迁移 ✅
func (p *Provider) parseShortcut(filePath string) (common.ShortcutInfo, error) {
	var shortcut common.ShortcutInfo

	// 从文件名提取编号
	fileName := filepath.Base(filePath)
	fileName = strings.TrimSuffix(fileName, ".lnk")

	number, err := strconv.Atoi(fileName)
	if err != nil {
		return shortcut, fmt.Errorf("filename is not a valid number: %s", fileName)
	}

	shortcut.Number = number
	shortcut.FilePath = filePath

	link, err := lnk.File(filePath)
	if err != nil {
		return shortcut, fmt.Errorf("failed to parse link file: %v", err)
	}

	shortcut.TargetPath = link.LinkInfo.LocalBasePath + link.LinkInfo.CommonPathSuffix
	shortcut.Arguments = link.StringData.CommandLineArguments
	shortcut.WorkingDir = link.StringData.WorkingDir

	// 如果 TargetPath 为空，尝试使用相对路径
	if shortcut.TargetPath == "" {
		shortcut.TargetPath = link.StringData.RelativePath
	}

	return shortcut, nil
}

// ParseWindowNumbers 解析窗口编号字符串（如 "1,2,3-5"）
// 从 utils.ParseWindowNumbers 迁移 ✅
func (p *Provider) ParseWindowNumbers(numbersStr string) ([]int, error) {
	var numbers []int

	if numbersStr == "" {
		return numbers, nil
	}

	parts := strings.Split(numbersStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			// 处理范围（如 "3-5"）
			rangeParts := strings.Split(part, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range format: %s", part)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid start number: %s", rangeParts[0])
			}

			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid end number: %s", rangeParts[1])
			}

			if start > end {
				return nil, fmt.Errorf("start number %d is greater than end number %d", start, end)
			}

			for i := start; i <= end; i++ {
				numbers = append(numbers, i)
			}
		} else {
			// 处理单个数字
			num, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", part)
			}
			numbers = append(numbers, num)
		}
	}

	return numbers, nil
}

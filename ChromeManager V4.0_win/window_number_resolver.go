package main

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"chromemanager/config"
	"chromemanager/platform/common"
)

const temporaryWindowNumberStart = 1001

var (
	userDataDirArgumentPattern = regexp.MustCompile(`(?i)(?:^|\s)--user-data-dir(?:=|\s+)(?:"([^"]+)"|'([^']+)'|([^\s]+))`)
	chromeEnvNumberPattern     = regexp.MustCompile(`(?i)^chrome_env_(\d+)$`)
)

func (c *ChromeService) resolveImportedWindowNumbers(windows []common.WindowInfo, settings *Settings) []common.WindowInfo {
	var shortcuts []common.ShortcutInfo
	if settings != nil {
		shortcutDir := config.GetGroupShortcutPath(settings)
		if shortcutDir != "" {
			parsed, err := c.provider.ParseShortcutsFromDirectory(shortcutDir)
			if err != nil {
				log.Printf("读取快捷方式以恢复窗口编号失败: %v", err)
			} else {
				shortcuts = parsed
			}
		}
	}

	return resolveChromeWindowNumbers(windows, shortcuts)
}

func resolveChromeWindowNumbers(windows []common.WindowInfo, shortcuts []common.ShortcutInfo) []common.WindowInfo {
	resolved := append([]common.WindowInfo(nil), windows...)
	usedNumbers := make(map[int]bool, len(resolved))

	// Reserve every valid number first so fallback recovery cannot steal it from
	// a window that was identified directly from its profile path.
	for i := range resolved {
		number := resolved[i].Number
		if number > 0 && !usedNumbers[number] {
			usedNumbers[number] = true
			continue
		}
		resolved[i].Number = 0
	}

	shortcutPaths, shortcutBasenames := buildShortcutProfileIndexes(shortcuts)
	temporaryNumber := temporaryWindowNumberStart

	for i := range resolved {
		if resolved[i].Number > 0 {
			continue
		}

		profilePath := windowProfilePath(resolved[i])
		candidates := []int{
			numberFromProfilePath(profilePath),
			firstAvailableShortcutNumber(shortcutPaths[normalizeProfilePath(profilePath)], usedNumbers),
			uniqueAvailableShortcutNumber(shortcutBasenames[profileBasename(profilePath)], usedNumbers),
			numberFromDebugPort(resolved[i].DebugPort),
		}

		for _, candidate := range candidates {
			if candidate > 0 && !usedNumbers[candidate] {
				resolved[i].Number = candidate
				usedNumbers[candidate] = true
				break
			}
		}

		if resolved[i].Number == 0 {
			for usedNumbers[temporaryNumber] {
				temporaryNumber++
			}
			resolved[i].Number = temporaryNumber
			usedNumbers[temporaryNumber] = true
			temporaryNumber++
		}
	}

	return resolved
}

func buildShortcutProfileIndexes(shortcuts []common.ShortcutInfo) (map[string][]int, map[string][]int) {
	paths := make(map[string][]int)
	basenames := make(map[string][]int)

	for _, shortcut := range shortcuts {
		if shortcut.Number <= 0 {
			continue
		}

		profilePath := extractUserDataDirArgument(shortcut.Arguments)
		normalizedPath := normalizeProfilePath(profilePath)
		if normalizedPath == "" {
			continue
		}

		paths[normalizedPath] = appendUniqueNumber(paths[normalizedPath], shortcut.Number)
		base := profileBasename(profilePath)
		if isDistinctProfileBasename(base) {
			basenames[base] = appendUniqueNumber(basenames[base], shortcut.Number)
		}
	}

	for key := range paths {
		sort.Ints(paths[key])
	}
	for key := range basenames {
		sort.Ints(basenames[key])
	}

	return paths, basenames
}

func appendUniqueNumber(numbers []int, number int) []int {
	for _, existing := range numbers {
		if existing == number {
			return numbers
		}
	}
	return append(numbers, number)
}

func firstAvailableShortcutNumber(numbers []int, usedNumbers map[int]bool) int {
	for _, number := range numbers {
		if number > 0 && !usedNumbers[number] {
			return number
		}
	}
	return 0
}

func uniqueAvailableShortcutNumber(numbers []int, usedNumbers map[int]bool) int {
	candidate := 0
	for _, number := range numbers {
		if number <= 0 || usedNumbers[number] {
			continue
		}
		if candidate != 0 {
			return 0
		}
		candidate = number
	}
	return candidate
}

func windowProfilePath(window common.WindowInfo) string {
	if profilePath := extractUserDataDirArgument(window.CommandLine); profilePath != "" {
		return profilePath
	}
	return window.UserDataDir
}

func extractUserDataDirArgument(arguments string) string {
	matches := userDataDirArgumentPattern.FindStringSubmatch(arguments)
	if len(matches) == 0 {
		return ""
	}
	for i := 1; i < len(matches); i++ {
		if matches[i] != "" {
			return strings.TrimSpace(matches[i])
		}
	}
	return ""
}

func numberFromProfilePath(profilePath string) int {
	normalized := normalizeProfilePath(profilePath)
	if normalized == "" {
		return 0
	}

	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return r == '\\' || r == '/'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if number, err := strconv.Atoi(part); err == nil && number > 0 {
			return number
		}
		if matches := chromeEnvNumberPattern.FindStringSubmatch(part); len(matches) > 1 {
			if number, err := strconv.Atoi(matches[1]); err == nil && number > 0 {
				return number
			}
		}
	}

	return 0
}

func numberFromDebugPort(debugPort int) int {
	if debugPort <= config.BaseDebugPort || debugPort > 65535 {
		return 0
	}
	return debugPort - config.BaseDebugPort
}

func normalizeProfilePath(profilePath string) string {
	profilePath = strings.Trim(strings.TrimSpace(profilePath), `"'`)
	if profilePath == "" {
		return ""
	}
	profilePath = os.ExpandEnv(profilePath)
	profilePath = strings.ReplaceAll(profilePath, "/", `\`)
	profilePath = strings.TrimPrefix(profilePath, `\\?\`)
	return strings.ToLower(filepath.Clean(profilePath))
}

func profileBasename(profilePath string) string {
	normalized := normalizeProfilePath(profilePath)
	if normalized == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(normalized))
}

func isDistinctProfileBasename(base string) bool {
	if base == "" {
		return false
	}
	commonNames := map[string]bool{
		"default": true,
		"profile": true,
		"chrome":  true,
		"google":  true,
		"browser": true,
		"data":    true,
	}
	return !commonNames[strings.ToLower(base)]
}

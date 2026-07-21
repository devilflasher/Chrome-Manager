package utils

import (
	"regexp"
	"strconv"
)

// ExtractUserDataDir 从命令行参数中提取用户数据目录
func ExtractUserDataDir(cmdline string) string {
	// 匹配 --user-data-dir="path" 或 --user-data-dir=path
	// 优先匹配带引号的
	userDataRegex := regexp.MustCompile(`--user-data-dir=["']([^"']+)["']`)
	if matches := userDataRegex.FindStringSubmatch(cmdline); len(matches) > 1 {
		return matches[1]
	}

	// 匹配不带引号的 (假设路径中无空格，或者直到下一个参数开始)
	// 简单起见，匹配到空格为止，但如果路径含空格且未引号，命令行解析本身就很复杂。
	// 这里假设简单的 --user-data-dir=C:\Path
	userDataRegexSimple := regexp.MustCompile(`--user-data-dir=([^\s]+)`)
	if matches := userDataRegexSimple.FindStringSubmatch(cmdline); len(matches) > 1 {
		return matches[1]
	}

	return ""
}

// ExtractDebugPort 从命令行参数中提取调试端口
func ExtractDebugPort(cmdline string) int {
	portRegex := regexp.MustCompile(`--remote-debugging-port[=\s]+(\d+)`)
	if matches := portRegex.FindStringSubmatch(cmdline); len(matches) > 1 {
		port, _ := strconv.Atoi(matches[1])
		return port
	}
	return 0
}

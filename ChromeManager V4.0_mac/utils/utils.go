package utils

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"chromemanager/platform/common"
	"github.com/shirou/gopsutil/v4/process"
)

type ChromeProcessInfo struct {
	PID         int32
	HWND        uintptr
	Title       string
	CommandLine string
	UserDataDir string
	Number      int
	DebugPort   int
}

type WindowInfo struct {
	Handle    uintptr
	Title     string
	Number    int
	IsRunning bool
	DebugPort int
	PID       int32
	HWND      uintptr
	IsMaster  bool
}


func (bo *BatchOperations) normalizeURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return parsedURL.String()
}

func ParseWindowNumbers(input string) ([]int, error) {
	if input == "" {
		return nil, nil
	}
	parts := strings.Split(input, ",")
	var numbers []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.Contains(p, "-") {
			rangeParts := strings.Split(p, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid range: %s", p)
			}
			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, err
			}
			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, err
			}
			if start > end {
				start, end = end, start
			}
			for i := start; i <= end; i++ {
				numbers = append(numbers, i)
			}
		} else {
			num, err := strconv.Atoi(p)
			if err != nil {
				return nil, err
			}
			numbers = append(numbers, num)
		}
	}
	return numbers, nil
}

// FindChromeProcesses 查找所有正在运行的 Chrome 进程（macOS 实现）
func FindChromeProcesses() ([]ChromeProcessInfo, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, fmt.Errorf("failed to get processes: %w", err)
	}

	var infos []ChromeProcessInfo
	for _, p := range processes {
		args, err := p.CmdlineSlice()
		if err != nil || len(args) == 0 {
			continue
		}

		info := ChromeProcessInfo{
			PID:  p.Pid,
			HWND: uintptr(p.Pid),
		}

		hasUserDataDir := false
		isChildProcess := false

		for _, arg := range args {
			if strings.HasPrefix(arg, "--type=") {
				isChildProcess = true
				break
			}
			if strings.HasPrefix(arg, "--user-data-dir=") {
				info.UserDataDir = strings.TrimPrefix(arg, "--user-data-dir=")
				hasUserDataDir = true

				// 提取窗口编号
				dirName := filepath.Base(info.UserDataDir)
				if common.ChromeUserDataDirPrefix != "" && strings.HasPrefix(dirName, common.ChromeUserDataDirPrefix) {
					numStr := strings.TrimPrefix(dirName, common.ChromeUserDataDirPrefix)
					info.Number, _ = strconv.Atoi(numStr)
				} else {
					// 兼容旧版或直接以数字命名的目录
					info.Number, _ = strconv.Atoi(dirName)
				}
			} else if strings.HasPrefix(arg, "--remote-debugging-port=") {
				portStr := strings.TrimPrefix(arg, "--remote-debugging-port=")
				info.DebugPort, _ = strconv.Atoi(portStr)
			}
		}

		// 只要有用户数据目录、能识别出编号，且不是子进程，就认为是一个有效的实例
		if !isChildProcess && hasUserDataDir && info.Number > 0 {
			name, _ := p.Name()
			info.Title = name
			info.CommandLine = strings.Join(args, " ")
			infos = append(infos, info)
		}
	}

	return infos, nil
}

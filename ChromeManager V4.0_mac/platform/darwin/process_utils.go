package darwin

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GetProcessCommandLine 获取进程的命令行参数
func GetProcessCommandLine(pid int32) (string, error) {
	// 使用 ps 命令获取完整命令行
	// -p: 指定 PID
	// -o command=: 只输出 command 列，且没有 header (由于 command= )
	// 注意: macOS 的 ps 输出可能会截断超长命令行，但在大多数情况下对于 Chrome 参数够用了。
	// 如果需要更精确，可能需要使用 sysctl (涉及 CGO)。
	cmd := exec.Command("ps", "-p", strconv.Itoa(int(pid)), "-o", "command=")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ps execution failed: %v", err)
	}

	cmdLine := strings.TrimSpace(string(output))
	if cmdLine == "" {
		return "", fmt.Errorf("empty command line")
	}

	return cmdLine, nil
}

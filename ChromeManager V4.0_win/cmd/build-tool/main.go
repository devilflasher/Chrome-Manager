package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	Reset   = "\033[0m"
	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Cyan    = "\033[36m"
	White   = "\033[37m"
	Magenta = "\033[35m"
	Bold    = "\033[1m"
	GreenBg = "\033[42m"
	Black   = "\033[30m"

	KeyUp    = 38
	KeyDown  = 40
	KeyEnter = 13
	KeyQ     = 113

	requiredNodeText = "Node.js 版本需满足 Vite 要求：20.19+ 或 22.12+"
	taskInstallPath  = "github.com/go-task/task/v3/cmd/task@v3.44.1"
	wailsInstallPath = "github.com/wailsapp/wails/v3/cmd/wails3@latest"
)

type MenuItem struct {
	Text   string
	Action func()
	Active bool
}

func main() {
	clearScreen()
	printHeader()

	// 1. Check Mandatory Environment
	if _, installed := checkAndInstallTools(); installed {
		fmt.Println(Green + "✅ 环境安装完成，按任意键进入主菜单..." + Reset)
		readKey()
	}

	// Initialize Menu
	menuItems := []MenuItem{
		{Text: "🛠️ 检查开发环境 (Node, Git, Task, Wails)", Action: checkEnvironment, Active: true},
		{Text: "🚀 编译生成可执行文件", Action: buildWindows, Active: true},
		{Text: "🧹 清理构建文件", Action: cleanBuild, Active: true},
		{Text: "🚪 退出", Action: func() {
			fmt.Println("再见！")
			os.Exit(0)
		}, Active: true},
	}

	selectedIndex := 0

	// Main Loop
	for {
		clearScreen()
		printHeader()
		printMenu(menuItems, selectedIndex)

		key := readKey()
		switch key {
		case KeyUp:
			selectedIndex--
			if selectedIndex < 0 {
				selectedIndex = len(menuItems) - 1
			}
		case KeyDown:
			selectedIndex++
			if selectedIndex >= len(menuItems) {
				selectedIndex = 0
			}
		case KeyEnter:
			clearScreen()
			printHeader()
			item := menuItems[selectedIndex]
			if item.Active {
				item.Action()
				fmt.Println("\n按任意键返回菜单...")
				readKey()
			} else {
				// Flash error?
			}
		case KeyQ:
			fmt.Println("再见！")
			os.Exit(0)
		}
	}
}

func printMenu(items []MenuItem, selected int) {
	fmt.Println(White + "请使用 ↑ ↓ 选择，Enter 确认：" + Reset)
	fmt.Println()

	for i, item := range items {
		prefix := "  "
		color := White
		switch i {
		case 0:
			color = Green
		case 1:
			color = Yellow
		case 2:
			color = Magenta
		case 3:
			color = Red
		}

		if i == selected {
			prefix = "👉"
		}

		if !item.Active {
			color = Red
			fmt.Printf("%s %s %s (不可用)%s\n", prefix, color, item.Text, Reset)
		} else {
			if i == selected {
				// Highlight
				fmt.Printf("%s %s %s %s\n", prefix, Bold+color, item.Text, Reset)
			} else {
				fmt.Printf("%s %s %s%s\n", prefix, color, item.Text, Reset)
			}
		}
	}
	fmt.Println()
}

func printHeader() {
	fmt.Println(Cyan + "==============================================" + Reset)
	fmt.Println(Cyan + "       ChromeManager V4.0 统一构建工具" + Reset)
	fmt.Println(Cyan + "==============================================" + Reset)
	fmt.Printf("当前系统: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Println()
}

func clearScreen() {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", "cls")
	} else {
		cmd = exec.Command("clear")
	}
	cmd.Stdout = os.Stdout
	cmd.Run()
}

func checkAndInstallTools() (bool, bool) {
	// Simple output, logic unchanged
	ok := true
	installed := false
	fmt.Println(Yellow + "正在检查必要工具..." + Reset)
	// Check Node
	if _, err := exec.LookPath("node"); err != nil {
		fmt.Println(Red + "❌ 未检测到 Node.js！" + Reset)
		ok = false
	} else if version, err := getNodeVersion(); err != nil {
		fmt.Println(Red+"❌ 无法读取 Node.js 版本："+Reset, err)
		ok = false
	} else if !nodeVersionOK(version) {
		fmt.Println(Red+"❌ Node.js 版本过低："+Reset, version)
		fmt.Println(Yellow + "   " + requiredNodeText + Reset)
		ok = false
	} else {
		fmt.Println(Green + "✅ Node.js 已安装：" + version + Reset)
	}
	// Check npm
	if _, err := exec.LookPath(npmCommand()); err != nil {
		fmt.Println(Red + "❌ 未检测到 npm！请重新安装 Node.js LTS 或检查 PATH。" + Reset)
		ok = false
	} else {
		fmt.Println(Green + "✅ npm 已安装" + Reset)
	}
	// Check Git
	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println(Yellow + "⚠️  未检测到 Git，安装 Wails CLI 需要 Git，正在自动安装..." + Reset)
		if runtime.GOOS == "windows" && installWingetPackage("Git.Git", "Git") {
			installed = true
			ensureGitInPath()
		}
		if _, err := exec.LookPath("git"); err != nil {
			fmt.Println(Red + "❌ Git 安装后仍不可用，请重启此脚本或手动安装 Git：https://git-scm.com/download/win" + Reset)
			ok = false
		} else {
			fmt.Println(Green + "✅ Git 已安装" + Reset)
		}
	} else {
		fmt.Println(Green + "✅ Git 已安装" + Reset)
	}
	// Check Task
	if _, err := exec.LookPath("task"); err != nil {
		fmt.Println(Yellow + "⚠️  未检测到 Task，正在自动安装..." + Reset)
		if installGoTool(taskInstallPath, "Task") {
			installed = true
			ensureGoBinInPath()
		}
		if _, err := exec.LookPath("task"); err != nil {
			fmt.Println(Red + "❌ Task 安装后仍不可用，请确认 GOPATH\\bin 已加入 PATH。" + Reset)
			ok = false
		} else {
			fmt.Println(Green + "✅ Task 已安装" + Reset)
		}
	} else {
		fmt.Println(Green + "✅ Task 已安装" + Reset)
	}
	// Check Wails
	if _, err := exec.LookPath("wails3"); err != nil {
		fmt.Println(Yellow + "⚠️  未检测到 Wails CLI，正在自动安装..." + Reset)
		if installGoTool(wailsInstallPath, "Wails3") {
			installed = true
			ensureGoBinInPath()
		}
		if _, err := exec.LookPath("wails3"); err != nil {
			fmt.Println(Red + "❌ Wails3 安装后仍不可用，请确认 GOPATH\\bin 已加入 PATH。" + Reset)
			ok = false
		} else {
			fmt.Println(Green + "✅ Wails3 已安装" + Reset)
		}
	} else {
		fmt.Println(Green + "✅ Wails3 已安装" + Reset)
	}
	fmt.Println()
	return ok, installed
}

func npmCommand() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd"
	}
	return "npm"
}

func getNodeVersion() (string, error) {
	out, err := exec.Command("node", "-v").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func nodeVersionOK(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return (major == 20 && minor >= 19) || (major == 22 && minor >= 12) || major > 22
}

func installGoTool(path string, name string) bool {
	fmt.Printf("正在安装 %s...\n", name)
	cmd := exec.Command("go", "install", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf(Red+"❌ %s 安装失败: %v\n"+Reset, name, err)
		fmt.Println(Red + "   请查看上方 Go 输出。常见原因：网络不可用、Go 代理不可用、Git 未安装或 GOPATH\\bin 无法写入。" + Reset)
		return false
	}
	return true
}

func installWingetPackage(id string, name string) bool {
	if _, err := exec.LookPath("winget"); err != nil {
		fmt.Println(Red + "❌ 未检测到 Winget，无法自动安装 " + name + "。" + Reset)
		return false
	}

	fmt.Printf("正在安装 %s...\n", name)
	cmd := exec.Command("winget", "install", id, "--accept-source-agreements", "--accept-package-agreements")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf(Red+"❌ %s 安装失败: %v\n"+Reset, name, err)
		fmt.Println(Red + "   请查看上方 Winget 输出。常见原因：网络不可用、Winget 源不可用或权限不足。" + Reset)
		return false
	}
	return true
}

func ensureGoBinInPath() {
	cmd := exec.Command("go", "env", "GOPATH")
	out, err := cmd.Output()
	if err != nil {
		return
	}

	goPath := strings.TrimSpace(string(out))
	if goPath == "" {
		return
	}

	goBin := filepath.Join(goPath, "bin")
	currentPath := os.Getenv("PATH")
	if strings.Contains(strings.ToLower(currentPath), strings.ToLower(goBin)) {
		return
	}
	os.Setenv("PATH", goBin+string(os.PathListSeparator)+currentPath)
}

func ensureGitInPath() {
	candidates := make([]string, 0, 3)
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
		if base == "" {
			continue
		}
		if base == os.Getenv("LocalAppData") {
			candidates = append(candidates, filepath.Join(base, "Programs", "Git", "cmd"))
		} else {
			candidates = append(candidates, filepath.Join(base, "Git", "cmd"))
		}
	}

	currentPath := os.Getenv("PATH")
	lowerPath := strings.ToLower(currentPath)
	for _, candidate := range candidates {
		if _, err := os.Stat(filepath.Join(candidate, "git.exe")); err != nil {
			continue
		}
		if strings.Contains(lowerPath, strings.ToLower(candidate)) {
			continue
		}
		currentPath = candidate + string(os.PathListSeparator) + currentPath
		lowerPath = strings.ToLower(currentPath)
	}
	os.Setenv("PATH", currentPath)
}

func checkEnvironment() {
	checkAndInstallTools()
}

func buildWindows() {
	fmt.Println(Yellow + "\n开始构建免安装版程序..." + Reset)
	ok, _ := checkAndInstallTools()
	if !ok {
		fmt.Println(Red + "❌ 构建环境未就绪，请先按提示修复后再构建。" + Reset)
		return
	}
	runTask("build")
}

func cleanBuild() {
	fmt.Println(Yellow + "\n清理构建文件..." + Reset)
	os.RemoveAll("bin")
	os.RemoveAll("build/bin")
	os.RemoveAll(".build-cache")
	fmt.Println(Green + "✅ 清理完成" + Reset)
}

func runTask(taskName string) {
	cmd := exec.Command("task", taskName, "PRODUCTION=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println(Cyan + "执行命令: task " + taskName + " PRODUCTION=true" + Reset)
	if err := cmd.Run(); err != nil {
		fmt.Printf(Red+"\n❌ 构建失败: %v\n"+Reset, err)
		fmt.Println(Red + "   请查看上方 task 输出定位具体失败步骤。" + Reset)
	} else {
		showSuccessBanner()
	}
}

func showSuccessBanner() {
	binDir := "bin"
	exeName := "ChromeManager.exe"
	if runtime.GOOS != "windows" {
		exeName = "ChromeManager"
	}
	absPath, _ := filepath.Abs(filepath.Join(binDir, exeName))

	fmt.Println()
	fmt.Println(Green + "================================================================" + Reset)
	fmt.Println(Green + "   ✨  🎉  恭喜！构建成功！ (BUILD SUCCESS)  🎉  ✨" + Reset)
	fmt.Println(Green + "================================================================" + Reset)
	fmt.Println("  构建生成的程序文件在：")
	fmt.Printf("  📂 %s\n", absPath)
	fmt.Println("  该文件可以移动到任意目录使用。")
	fmt.Println("  目标电脑需要已安装 Microsoft Edge WebView2 Runtime。")
	fmt.Println()
}

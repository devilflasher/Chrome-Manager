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

var goPathInjectedMessageShown bool

func main() {
	clearScreen()
	printHeader()
	if _, installed := checkAndInstallTools(); installed {
		fmt.Println(Green + "✅ 环境安装完成，按任意键进入主菜单..." + Reset)
		readKey()
	}

	for {
		clearScreen()
		printHeader()

		// Initialize Menu based on OS
		menuItems := getMenuByOS()

		selectedIndex := 0
		runMenu(menuItems, selectedIndex)
	}
}

func getMenuByOS() []MenuItem {
	commonItems := []MenuItem{
		{Text: "🛠️ 检查开发环境 (Go/Node/npm/Git/Task/Wails/Xcode CLT)", Action: checkEnvironment, Active: true},
	}

	var osItems []MenuItem
	switch runtime.GOOS {
	case "darwin":
		osItems = []MenuItem{
			{Text: "📦 打包 macOS 当前架构版 (.app，速度最快)", Action: func() { buildMac("package") }, Active: true},
			{Text: "🌎 打包 macOS 通用版 (.app，Intel + Apple Silicon)", Action: func() { buildMac("package:universal") }, Active: true},
		}
	case "windows":
		osItems = []MenuItem{
			{Text: "📦 打包 Windows 版程序", Action: func() { runTaskWithProduction("package") }, Active: true},
		}
	}

	footerItems := []MenuItem{
		{Text: "🧹 清理构建缓存和产物", Action: cleanBuild, Active: true},
		{Text: "🚪 退出", Action: func() {
			fmt.Println("再见！")
			os.Exit(0)
		}, Active: true},
	}

	items := append(commonItems, osItems...)
	return append(items, footerItems...)
}

func runMenu(items []MenuItem, selectedIndex int) {
	for {
		clearScreen()
		printHeader()
		printMenu(items, selectedIndex)

		key := readKey()
		switch key {
		case KeyUp:
			selectedIndex--
			if selectedIndex < 0 {
				selectedIndex = len(items) - 1
			}
		case KeyDown:
			selectedIndex++
			if selectedIndex >= len(items) {
				selectedIndex = 0
			}
		case KeyEnter:
			clearScreen()
			printHeader()
			item := items[selectedIndex]
			if item.Active {
				item.Action()
				fmt.Println("\n" + Yellow + "任务执行完毕，按任意键返回主菜单..." + Reset)
				readKey()
				return
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

		if i == selected {
			prefix = "👉"
			color = Cyan
		}

		if !item.Active {
			color = Red
			fmt.Printf("%s %s %s (不可用)%s\n", prefix, color, item.Text, Reset)
		} else {
			if i == selected {
				fmt.Printf("%s %s %s %s\n", prefix, Bold+Cyan, item.Text, Reset)
			} else {
				fmt.Printf("%s %s %s%s\n", prefix, color, item.Text, Reset)
			}
		}
	}
	fmt.Println()
}

func printHeader() {
	fmt.Println(Cyan + "==============================================" + Reset)
	fmt.Println(Cyan + "       ChromeManager V4.0 macOS 统一构建工具" + Reset)
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

func getGoEnv(key string) string {
	cmd := exec.Command("go", "env", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func resolveGoBinDir() string {
	gobin := getGoEnv("GOBIN")
	if gobin != "" {
		return gobin
	}

	gopath := getGoEnv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "go", "bin")
	}

	parts := filepath.SplitList(gopath)
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}
	return filepath.Join(parts[0], "bin")
}

func ensureGoBinInPath() (string, bool) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", false
	}

	goBinDir := resolveGoBinDir()
	if goBinDir == "" {
		return "", false
	}

	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == goBinDir {
			return goBinDir, false
		}
	}

	pathValue := os.Getenv("PATH")
	if pathValue == "" {
		_ = os.Setenv("PATH", goBinDir)
	} else {
		_ = os.Setenv("PATH", goBinDir+string(os.PathListSeparator)+pathValue)
	}
	return goBinDir, true
}

func lookPathWithGoBin(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	_, _ = ensureGoBinInPath()
	return exec.LookPath(name)
}

func checkAndInstallTools() (bool, bool) {
	ok := true
	installed := false
	fmt.Println(Yellow + "正在检查必要工具..." + Reset)

	if path, err := exec.LookPath("go"); err != nil {
		fmt.Println(Red + "❌ 未检测到 Go。请通过 build.command/Homebrew 安装，或手动安装：https://go.dev/dl/" + Reset)
		ok = false
	} else {
		fmt.Println(Green+"✅ Go 已安装: "+Reset, path)
		notifyIfGoBinInjected()
	}

	if version, err := getNodeVersion(); err != nil {
		fmt.Println(Red + "❌ 未检测到 Node.js。请通过 build.command/Homebrew 安装，或手动安装 LTS：https://nodejs.org/" + Reset)
		ok = false
	} else if !nodeVersionOK(version) {
		fmt.Println(Red+"❌ Node.js 版本过低："+Reset, version)
		fmt.Println(Yellow + "   " + requiredNodeText + Reset)
		ok = false
	} else {
		fmt.Println(Green + "✅ Node.js 已安装：" + version + Reset)
	}

	if _, err := exec.LookPath(npmCommand()); err != nil {
		fmt.Println(Red + "❌ 未检测到 npm。npm 通常随 Node.js 一起安装，请检查 Node.js 和 PATH。" + Reset)
		ok = false
	} else {
		fmt.Println(Green + "✅ npm 已安装" + Reset)
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println(Red + "❌ 未检测到 Git。安装 Wails CLI 或拉取依赖时需要 Git。" + Reset)
		fmt.Println(Yellow + "   可执行：brew install git" + Reset)
		ok = false
	} else {
		fmt.Println(Green + "✅ Git 已安装" + Reset)
	}

	if runtime.GOOS == "darwin" {
		if err := exec.Command("xcode-select", "-p").Run(); err != nil {
			fmt.Println(Red + "❌ 未检测到 Xcode Command Line Tools。CGO/macOS 打包需要它。" + Reset)
			fmt.Println(Yellow + "   可执行：xcode-select --install" + Reset)
			ok = false
		} else {
			fmt.Println(Green + "✅ Xcode Command Line Tools 已安装" + Reset)
		}
	}

	if _, err := lookPathWithGoBin("task"); err != nil {
		fmt.Println(Yellow + "⚠️  未检测到 Task，正在自动安装..." + Reset)
		if installGoTool(taskInstallPath, "Task", "task") {
			installed = true
			notifyIfGoBinInjected()
		}
		if _, err := lookPathWithGoBin("task"); err != nil {
			fmt.Println(Red + "❌ Task 安装后仍不可用，请确认 GOPATH/bin 已加入 PATH。" + Reset)
			ok = false
		} else {
			fmt.Println(Green + "✅ Task 已安装" + Reset)
		}
	} else {
		fmt.Println(Green + "✅ Task 已安装" + Reset)
	}

	bindingsReady := bindingsExist()
	if _, err := lookPathWithGoBin("wails3"); err != nil {
		if bindingsReady {
			fmt.Println(Yellow + "ℹ️  未检测到 Wails3；发布构建将复用现有 frontend/bindings。" + Reset)
		} else {
			fmt.Println(Yellow + "⚠️  frontend/bindings 缺失且未检测到 Wails3，正在自动安装..." + Reset)
			if installGoTool(wailsInstallPath, "Wails3", "wails3") {
				installed = true
				notifyIfGoBinInjected()
			}
			if _, err := lookPathWithGoBin("wails3"); err != nil {
				fmt.Println(Red + "❌ Wails3 安装后仍不可用，且 bindings 缺失，无法继续构建。" + Reset)
				ok = false
			}
		}
	} else if bindingsReady {
		fmt.Println(Green + "✅ Wails3 已安装；发布构建会优先复用现有 bindings" + Reset)
	} else {
		fmt.Println(Green + "✅ Wails3 已安装，可生成 frontend/bindings" + Reset)
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

func bindingsExist() bool {
	info, err := os.Stat(filepath.Join("frontend", "bindings"))
	return err == nil && info.IsDir()
}

func installGoTool(path string, name string, binaryName string) bool {
	fmt.Printf("正在安装 %s...\n", name)
	cmd := exec.Command("go", "install", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf(Red+"❌ %s 安装失败: %v\n"+Reset, name, err)
		fmt.Println(Red + "   请查看上方 Go 输出。常见原因：网络不可用、Git 未安装、Go 代理不可用或 GOPATH/bin 无法写入。" + Reset)
		return false
	}

	if binaryPath, err := lookPathWithGoBin(binaryName); err == nil {
		fmt.Printf(Green+"✅ %s 安装成功 (%s)\n"+Reset, name, binaryPath)
		return true
	}

	goBinDir := resolveGoBinDir()
	fmt.Printf(Yellow+"⚠️  %s 已安装，但当前会话未找到可执行文件 `%s`\n"+Reset, name, binaryName)
	if goBinDir != "" {
		fmt.Printf(Yellow+"   请确认 PATH 包含: %s\n"+Reset, goBinDir)
	}
	return false
}

func checkEnvironment() {
	checkAndInstallTools()
}

func buildWindows() {
	fmt.Println(Yellow + "\n开始构建 Windows 版本 (正式版)..." + Reset)
	ok, _ := checkAndInstallTools()
	if !ok {
		fmt.Println(Red + "❌ 构建环境未就绪，请先按提示修复后再构建。" + Reset)
		return
	}
	runTaskWithProduction("build")
}

func buildMac(taskName string) {
	if taskName == "package:universal" {
		fmt.Println(Yellow + "\n开始打包 macOS 通用版（arm64 + amd64）..." + Reset)
	} else {
		fmt.Println(Yellow + "\n开始打包 macOS 当前架构版..." + Reset)
	}
	ok, _ := checkAndInstallTools()
	if !ok {
		fmt.Println(Red + "❌ 构建环境未就绪，请先按提示修复后再构建。" + Reset)
		return
	}
	runTaskWithProduction(taskName)
}

func cleanBuild() {
	clearScreen()
	printHeader()
	fmt.Println(Red + Bold + "⚠️  警告：清理构建缓存将执行以下操作：" + Reset)
	fmt.Println(Yellow + "   1. 删除整个 bin/ 目录（包含已生成的 .app, .exe, 安装包等）" + Reset)
	fmt.Println(Yellow + "   2. 清除所有已生成的环境图标 (icons/)" + Reset)
	fmt.Println(Red + "   3. 注意：如果你曾手动修改过 bin/settings.json，请务必先备份！" + Reset)
	fmt.Println()
	fmt.Println(White + "如果您只是想解决打包不生效的问题，请选择“是”。" + Reset)
	fmt.Println()

	confirmItems := []MenuItem{
		{Text: "❌ 否，返回主菜单", Action: func() {}, Active: true},
		{Text: "✅ 是，立即清理 (我已备份设置)", Action: func() {
			fmt.Println(Yellow + "\n正在清理构建文件..." + Reset)
			os.RemoveAll("bin")
			os.RemoveAll("build/bin")
			os.RemoveAll(".build-cache")
			fmt.Println(Green + "✅ 清理完成！" + Reset)
		}, Active: true},
	}

	selected := 0
	for {
		clearScreen()
		printHeader()
		fmt.Println(Red + Bold + "⚠️  警告：清理构建缓存将执行以下操作：" + Reset)
		fmt.Println(Yellow + "   1. 删除整个 bin 目录" + Reset)
		fmt.Println(Yellow + "   2. 清除所有环境图标缓存" + Reset)
		fmt.Println(Red + "   3. 如果需要备份配置文件，请在软件设置页面先备份！" + Reset)
		fmt.Println()

		printMenu(confirmItems, selected)

		key := readKey()
		switch key {
		case KeyUp:
			selected--
			if selected < 0 {
				selected = len(confirmItems) - 1
			}
		case KeyDown:
			selected++
			if selected >= len(confirmItems) {
				selected = 0
			}
		case KeyEnter:
			if selected == 1 {
				confirmItems[1].Action()
				return
			}
			return
		}
	}
}

func runTaskWithProduction(taskName string) {
	notifyIfGoBinInjected()

	taskPath, err := lookPathWithGoBin("task")
	if err != nil {
		fmt.Println(Red + "❌ 未找到 Task，可先运行“检查开发环境”。" + Reset)
		return
	}

	cmd := exec.Command(taskPath, taskName, "PRODUCTION=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println(Cyan + "执行命令: task " + taskName + " PRODUCTION=true" + Reset)
	if err := cmd.Run(); err != nil {
		fmt.Printf(Red+"\n❌ 构建失败: %v\n"+Reset, err)
		fmt.Println(Red + "   请查看上方 task 输出定位具体失败步骤。" + Reset)
	} else {
		// 针对 macOS 的特殊处理：清除隔离标记，防止报“文件损坏”
		if runtime.GOOS == "darwin" && (taskName == "package" || taskName == "package:universal") {
			_ = exec.Command("xattr", "-cr", "bin/ChromeManager.app").Run()
		}
		showSuccessBanner(taskName)
	}
}

func notifyIfGoBinInjected() {
	if goPathInjectedMessageShown {
		return
	}

	if goBinDir, added := ensureGoBinInPath(); added {
		fmt.Println(Cyan + "ℹ️  已将 Go 工具目录加入当前会话 PATH: " + goBinDir + Reset)
		goPathInjectedMessageShown = true
	}
}

func showSuccessBanner(taskName string) {
	binDir := "bin"

	fmt.Println()
	fmt.Println(Green + "================================================================" + Reset)
	fmt.Println(Green + "   ✨  🎉  恭喜！构建/打包成功！ (SUCCESS)  🎉  ✨" + Reset)
	fmt.Println(Green + "================================================================" + Reset)

	if runtime.GOOS == "darwin" && (taskName == "package" || taskName == "package:universal") {
		absPath, _ := filepath.Abs(filepath.Join(binDir, "ChromeManager.app"))
		fmt.Printf(Green+"  📂 产物位置: %s\n"+Reset, absPath)
		if taskName == "package:universal" {
			fmt.Println("  类型: macOS Universal App (Intel + Apple Silicon)")
		} else {
			fmt.Printf("  类型: macOS 当前架构 App (%s)\n", runtime.GOARCH)
		}
		fmt.Println("  可以移动到 /Applications 或任意目录使用。")
		fmt.Println("  首次运行同步功能时，请在系统设置中授予“辅助功能”和“输入监控”权限。")
		fmt.Println("  如果移动或重新打包了 .app，macOS 可能需要重新授权。")
	} else {
		exeName := "ChromeManager.exe"
		if runtime.GOOS != "windows" {
			exeName = "ChromeManager"
		}
		absPath, _ := filepath.Abs(filepath.Join(binDir, exeName))
		fmt.Printf(Green+"  📂 产物位置: %s\n"+Reset, absPath)
	}
	fmt.Println()
}

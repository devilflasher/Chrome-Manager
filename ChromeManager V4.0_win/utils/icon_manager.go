package utils

import (
	"chromemanager/config"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"

	"github.com/sergeymakinen/go-ico"
)

const (
	WM_SETICON          = 0x0080
	ICON_SMALL          = 0
	ICON_BIG            = 1
	IMAGE_ICON          = 1
	LR_LOADFROMFILE     = 0x00000010
	LR_CREATEDIBSECTION = 0x00002000
	SHCNE_ASSOCCHANGED  = 0x08000000
)

var (
	iconUser32         = syscall.NewLazyDLL("user32.dll")
	iconShell32        = syscall.NewLazyDLL("shell32.dll")
	iconLoadImageW     = iconUser32.NewProc("LoadImageW")
	iconSendMessageW   = iconUser32.NewProc("SendMessageW")
	iconIsWindow       = iconUser32.NewProc("IsWindow")
	iconSHChangeNotify = iconShell32.NewProc("SHChangeNotify")
)

type IconManager struct {
	font           *truetype.Font
	iconDir        string
	chromeTemplate string
}

func NewIconManager() (*IconManager, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("get executable path failed: %v", err)
	}
	execDir := filepath.Dir(execPath)

	iconDir := filepath.Join(execDir, "icons")
	chromeTemplate := filepath.Join(execDir, "icons", "chrome.png")
	if _, err := os.Stat(chromeTemplate); errors.Is(err, os.ErrNotExist) {
		chromeTemplate = filepath.Join(execDir, "build", "chrome.png")
		if _, err := os.Stat(chromeTemplate); errors.Is(err, os.ErrNotExist) {
			chromeTemplate = filepath.Join("build", "chrome.png")
			if _, err := os.Stat(chromeTemplate); errors.Is(err, os.ErrNotExist) {
				chromeTemplate = filepath.Join("Old", "icons", "chrome.png")
				if _, err := os.Stat(chromeTemplate); errors.Is(err, os.ErrNotExist) {
					chromeTemplate = ""
				}
			}
		}
	}

	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return nil, fmt.Errorf("create icon dir failed: %v", err)
	}

	font, err := loadArialFont()
	if err != nil {
		log.Printf("load arial font failed: %v", err)
	}

	return &IconManager{
		font:           font,
		iconDir:        iconDir,
		chromeTemplate: chromeTemplate,
	}, nil
}

func loadArialFont() (*truetype.Font, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("仅支持Windows系统")
	}

	fontPath := filepath.Join(os.Getenv("WINDIR"), "Fonts", "arialbd.ttf")
	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {

		fontPath = filepath.Join(os.Getenv("WINDIR"), "Fonts", "arial.ttf")
		fontBytes, err = os.ReadFile(fontPath)
		if err != nil {
			return nil, err
		}
	}

	return freetype.ParseFont(fontBytes)
}

func (im *IconManager) GenerateColorIcon(windowNumber int, size int) (string, error) {
	iconPath := filepath.Join(im.iconDir, fmt.Sprintf("%d.ico", windowNumber))

	if generatedIconIsCurrent(iconPath, im.chromeTemplate) {
		return iconPath, nil
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))

	if im.chromeTemplate != "" {
		bgImg, err := loadChromeBackground(im.chromeTemplate, size)
		if err == nil {
			draw.Draw(img, img.Bounds(), bgImg, image.Point{0, 0}, draw.Over)
		}
	}

	scaleFactor := float64(size) / 48.0

	ellipseWidth := float64(size) * 0.85
	ellipseHeight := float64(size) * 0.5
	ellipseLeft := (float64(size) - ellipseWidth) / 2
	ellipseTop := (float64(size)-ellipseHeight)/2 + (12 * scaleFactor)

	drawEllipse(img, int(ellipseLeft), int(ellipseTop), int(ellipseWidth), int(ellipseHeight),
		color.RGBA{30, 30, 30, 255})

	// 绘制文字 - 简化版本
	fontSize := 22 * scaleFactor    // 字体大小
	textOffsetY := 13 * scaleFactor // 垂直偏移

	if err := im.drawText(img, fmt.Sprintf("%d", windowNumber), fontSize, textOffsetY); err != nil {
		return "", fmt.Errorf("绘制文字失败: %v", err)
	}

	return iconPath, im.saveAsICO(img, iconPath, []int{48, 256})
}

func loadChromeBackground(templatePath string, size int) (image.Image, error) {
	file, err := os.Open(templatePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, err
	}

	// 调整大小到目标尺寸
	return resizeImage(img, size, size), nil
}

func resizeImage(src image.Image, width, height int) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			srcX := x * bounds.Dx() / width
			srcY := y * bounds.Dy() / height
			dst.Set(x, y, src.At(bounds.Min.X+srcX, bounds.Min.Y+srcY))
		}
	}

	return dst
}

func drawEllipse(img *image.RGBA, x, y, width, height int, col color.RGBA) {
	centerX := float64(x + width/2)
	centerY := float64(y + height/2)
	a := float64(width) / 2.0
	b := float64(height) / 2.0

	for py := y; py < y+height; py++ {
		for px := x; px < x+width; px++ {
			if px >= 0 && py >= 0 && px < img.Bounds().Dx() && py < img.Bounds().Dy() {

				dx := float64(px) - centerX
				dy := float64(py) - centerY

				if (dx*dx)/(a*a)+(dy*dy)/(b*b) <= 1.0 {
					img.Set(px, py, col)
				}
			}
		}
	}
}

func (im *IconManager) drawText(img *image.RGBA, text string, fontSize, offsetY float64) error {
	if im.font == nil {
		return fmt.Errorf("字体未加载")
	}

	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(im.font)
	c.SetFontSize(fontSize)
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(image.NewUniform(color.RGBA{255, 255, 255, 255}))

	// 计算文字位置
	textWidth := int(fontSize * float64(len(text)) * 0.5)
	x := (img.Bounds().Dx()-textWidth)/2 - 2
	y := img.Bounds().Dy()/2 + int(offsetY) + int(fontSize*0.35)

	pt := freetype.Pt(x, y)
	_, err := c.DrawString(text, pt)
	return err
}

func (im *IconManager) saveAsICO(img image.Image, path string, sizes []int) error {
	var images []image.Image

	for _, size := range sizes {
		if img.Bounds().Dx() == size {
			images = append(images, img)
		} else {
			images = append(images, resizeImage(img, size, size))
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if len(images) > 0 {
		return ico.EncodeAll(file, images)
	}
	return fmt.Errorf("没有图像要保存")
}

func (im *IconManager) SetWindowIcon(hwnd uintptr, iconPath string, retries int, delay time.Duration) error {
	for attempt := 0; attempt < retries; attempt++ {
		// 确保窗口仍然有效
		ret, _, _ := iconIsWindow.Call(hwnd)
		if ret == 0 {
			return fmt.Errorf("窗口 %d 已不存在", hwnd)
		}

		iconPathUTF16, err := syscall.UTF16PtrFromString(iconPath)
		if err != nil {
			return fmt.Errorf("转换路径失败: %v", err)
		}

		bigIcon, _, _ := iconLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(iconPathUTF16)),
			IMAGE_ICON,
			32, 32,
			LR_LOADFROMFILE,
		)
		if bigIcon == 0 {
			time.Sleep(delay)
			continue
		}

		smallIcon, _, _ := iconLoadImageW.Call(
			0,
			uintptr(unsafe.Pointer(iconPathUTF16)),
			IMAGE_ICON,
			16, 16,
			LR_LOADFROMFILE,
		)
		if smallIcon == 0 {
			time.Sleep(delay)
			continue
		}

		// 再次检查窗口是否有效
		ret, _, _ = iconIsWindow.Call(hwnd)
		if ret == 0 {
			return fmt.Errorf("设置图标前窗口 %d 已关闭", hwnd)
		}

		// 设置大图标
		iconSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, bigIcon)
		time.Sleep(delay / 2)

		// 设置小图标
		iconSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, smallIcon)

		// 刷新图标缓存
		iconSHChangeNotify.Call(SHCNE_ASSOCCHANGED, 0, 0, 0)

		time.Sleep(delay)
		return nil
	}

	return fmt.Errorf("failed to set icon after %d attempts", retries)
}

func (im *IconManager) GenerateIconsParallel(windowNumbers []int, maxWorkers int) map[int]string {
	if maxWorkers <= 0 {
		maxWorkers = 10
	}

	jobs := make(chan int, len(windowNumbers))
	results := make(chan struct {
		number int
		path   string
		err    error
	}, len(windowNumbers))

	// 启动工作协程
	var wg sync.WaitGroup
	for i := 0; i < maxWorkers && i < len(windowNumbers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for number := range jobs {
				path, err := im.GenerateColorIcon(number, 256)
				results <- struct {
					number int
					path   string
					err    error
				}{number, path, err}
			}
		}()
	}

	// 发送任务
	for _, number := range windowNumbers {
		jobs <- number
	}
	close(jobs)

	// 等待完成
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	iconPaths := make(map[int]string)

	for result := range results {
		if result.err == nil {
			iconPaths[result.number] = result.path
		}
	}

	return iconPaths
}

func (im *IconManager) SetWindowIconsParallel(windowData map[int]uintptr, iconPaths map[int]string, maxWorkers int) int {
	if maxWorkers <= 0 {
		maxWorkers = 5
	}

	type job struct {
		number   int
		hwnd     uintptr
		iconPath string
	}

	jobs := make(chan job, len(windowData))
	results := make(chan bool, len(windowData))

	// 启动工作协程
	var wg sync.WaitGroup
	for i := 0; i < maxWorkers && i < len(windowData); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				err := im.SetWindowIcon(j.hwnd, j.iconPath, 2, 300*time.Millisecond)
				results <- (err == nil)
			}
		}()
	}

	// 发送任务
	for number, hwnd := range windowData {
		if iconPath, exists := iconPaths[number]; exists {
			jobs <- job{number, hwnd, iconPath}
		}
	}
	close(jobs)

	// 等待完成
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	successCount := 0
	for success := range results {
		if success {
			successCount++
		}
	}

	return successCount
}

func (im *IconManager) UpdateShortcutIcon(shortcutPath, iconPath string) error {
	if _, err := os.Stat(shortcutPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("快捷方式文件不存在: %s", shortcutPath)
	}

	if _, err := os.Stat(iconPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("图标文件不存在: %s", iconPath)
	}

	script := fmt.Sprintf(`
		$shell = New-Object -ComObject WScript.Shell
		$shortcut = $shell.CreateShortcut('%s')
		$shortcut.IconLocation = '%s'
		$shortcut.Save()
	`, shortcutPath, iconPath)

	cmd := exec.Command("powershell", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("更新快捷方式图标失败: %v", err)
	}

	return nil
}

func (im *IconManager) UpdateShortcutIconsParallel(shortcutDir string, iconPaths map[int]string, maxWorkers int) int {
	if maxWorkers <= 0 {
		maxWorkers = 10
	}

	if _, err := os.Stat(shortcutDir); errors.Is(err, os.ErrNotExist) {
		log.Printf("快捷方式目录不存在: %s", shortcutDir)
		return 0
	}

	type job struct {
		number       int
		shortcutPath string
		iconPath     string
	}

	jobs := make(chan job, len(iconPaths))
	results := make(chan bool, len(iconPaths))

	// 启动工作协程
	var wg sync.WaitGroup
	for i := 0; i < maxWorkers && i < len(iconPaths); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				err := im.UpdateShortcutIcon(j.shortcutPath, j.iconPath)
				results <- (err == nil)
			}
		}()
	}

	// 发送任务
	for number, iconPath := range iconPaths {
		shortcutPath := filepath.Join(shortcutDir, fmt.Sprintf("%d.lnk", number))
		jobs <- job{number, shortcutPath, iconPath}
	}
	close(jobs)

	// 等待完成
	go func() {
		wg.Wait()
		close(results)
	}()

	// 收集结果
	successCount := 0
	for success := range results {
		if success {
			successCount++
		}
	}

	return successCount
}

func (im *IconManager) ApplyIconsToWindows(windows []ChromeProcessInfo, autoModifyShortcut bool, shortcutDir string) {
	// 提取窗口编号
	windowNumbers := make([]int, 0, len(windows))
	windowData := make(map[int]uintptr)

	for _, window := range windows {
		windowNumbers = append(windowNumbers, window.Number)
		windowData[window.Number] = window.HWND
	}

	iconPaths := im.GenerateIconsParallel(windowNumbers, 10)

	if autoModifyShortcut && shortcutDir != "" && len(iconPaths) > 0 {
		im.UpdateShortcutIconsParallel(shortcutDir, iconPaths, 10)
	}

	im.SetWindowIconsParallel(windowData, iconPaths, 5)
}

func (im *IconManager) CleanIconCache() error {
	if err := im.closeExplorer(); err != nil {
		log.Printf("关闭资源管理器失败: %v", err)
	}

	// 等待一小段时间确保进程完全关闭
	time.Sleep(1 * time.Second)

	if err := im.deleteIconCacheFiles(); err != nil {
		log.Printf("删除图标缓存文件失败: %v", err)
	}

	if err := im.restartExplorer(); err != nil {
		log.Printf("重启资源管理器失败: %v", err)
	}

	im.refreshIconCache()

	return nil
}

func (im *IconManager) closeExplorer() error {
	cmd := exec.Command("taskkill", "/f", "/im", "explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func (im *IconManager) deleteIconCacheFiles() error {
	userProfile := os.Getenv("USERPROFILE")
	if userProfile == "" {
		return fmt.Errorf("无法获取用户配置文件路径")
	}

	iconCacheDB := filepath.Join(userProfile, "AppData", "Local", "IconCache.db")
	if err := os.Remove(iconCacheDB); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("删除IconCache.db失败: %v", err)
	}

	explorerDir := filepath.Join(userProfile, "AppData", "Local", "Microsoft", "Windows", "Explorer")
	if files, err := filepath.Glob(filepath.Join(explorerDir, "IconCache_*.db")); err == nil {
		for _, file := range files {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("删除图标缓存文件失败 %s: %v", file, err)
			}
		}
	}

	// 删除缩略图缓存
	if files, err := filepath.Glob(filepath.Join(explorerDir, "thumbcache_*.db")); err == nil {
		for _, file := range files {
			if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("删除缩略图缓存失败 %s: %v", file, err)
			}
		}
	}

	return nil
}

func (im *IconManager) restartExplorer() error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

type ShortcutRestorer struct{}

func NewShortcutRestorer() *ShortcutRestorer {
	return &ShortcutRestorer{}
}

func (sr *ShortcutRestorer) RestoreToDefaultIcons() error {
	settings, err := sr.loadSettings()
	if err != nil {
		log.Printf("加载设置失败: %v", err)
		return fmt.Errorf("无法加载设置: %v", err)
	}

	if settings.ShortcutPath == "" {
		return fmt.Errorf("快捷方式目录不存在或未设置")
	}
	if _, err := os.Stat(settings.ShortcutPath); os.IsNotExist(err) {
		return fmt.Errorf("快捷方式目录不存在: %s", settings.ShortcutPath)
	}

	chromePath, err := FindBrowserPath(settings.BrowserType)
	if err != nil {
		return fmt.Errorf("未找到Chrome安装路径: %v", err)
	}

	shortcuts, err := sr.getShortcutFiles(settings.ShortcutPath)
	if err != nil {
		return fmt.Errorf("获取快捷方式文件失败: %v", err)
	}

	if len(shortcuts) == 0 {
		return fmt.Errorf("快捷方式目录中没有找到.lnk文件")
	}

	_, err = sr.restoreShortcutsOptimized(shortcuts, chromePath)
	if err != nil {
		return fmt.Errorf("还原快捷方式图标失败: %v", err)
	}

	// 刷新图标缓存
	sr.refreshIconCache()

	return nil
}

// RestoreToDefaultIconsAllGroups 还原所有分组的快捷方式图标
func (sr *ShortcutRestorer) RestoreToDefaultIconsAllGroups(settings *config.Settings) error {
	totalSuccessCount := 0
	totalFiles := 0

	// 获取Chrome路径
	chromePath, err := FindBrowserPath(settings.BrowserType)
	if err != nil {
		return fmt.Errorf("未找到Chrome安装路径: %v", err)
	}

	// 还原默认分组
	if settings.ShortcutPath != "" {
		if _, err := os.Stat(settings.ShortcutPath); err == nil {
			shortcuts, err := sr.getShortcutFiles(settings.ShortcutPath)
			if err == nil && len(shortcuts) > 0 {
				successCount, _ := sr.restoreShortcutsOptimized(shortcuts, chromePath)
				totalSuccessCount += successCount
				totalFiles += len(shortcuts)
			}
		}
	}

	// 还原所有自定义分组
	for _, group := range settings.Groups {
		if group.ShortcutPath != "" {
			if _, err := os.Stat(group.ShortcutPath); err == nil {
				shortcuts, err := sr.getShortcutFiles(group.ShortcutPath)
				if err == nil && len(shortcuts) > 0 {
					successCount, _ := sr.restoreShortcutsOptimized(shortcuts, chromePath)
					totalSuccessCount += successCount
					totalFiles += len(shortcuts)
				}
			}
		}
	}

	if totalFiles == 0 {
		return fmt.Errorf("没有找到任何快捷方式文件")
	}

	// 刷新图标缓存
	sr.refreshIconCache()

	return nil
}

func (sr *ShortcutRestorer) loadSettings() (*config.Settings, error) {
	configPath, err := config.GetSettingsFilePath()
	if err != nil {
		return nil, fmt.Errorf("获取配置文件路径失败: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	var settings config.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	return &settings, nil
}

func (sr *ShortcutRestorer) getShortcutFiles(dir string) ([]string, error) {
	var shortcuts []string

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".lnk") {
			shortcuts = append(shortcuts, filepath.Join(dir, file.Name()))
		}
	}

	return shortcuts, nil
}

func (sr *ShortcutRestorer) restoreShortcutsOptimized(shortcuts []string, chromePath string) (int, error) {

	var scriptParts []string
	scriptParts = append(scriptParts, "$shell = New-Object -ComObject WScript.Shell")
	scriptParts = append(scriptParts, fmt.Sprintf("$chromePath = '%s'", strings.ReplaceAll(chromePath, "'", "''")))
	scriptParts = append(scriptParts, "$successCount = 0")

	// 逐个添加快捷方式处理，避免复杂的数组语法
	for _, shortcut := range shortcuts {
		cleanPath := strings.ReplaceAll(shortcut, "'", "''")
		scriptParts = append(scriptParts, fmt.Sprintf(`
try {
    $shortcut = $shell.CreateShortcut('%s')
    $shortcut.IconLocation = "$chromePath,0"
    $shortcut.Save()
    $successCount++
} catch {
    Write-Host "Failed: %s"
}`, cleanPath, filepath.Base(shortcut)))
	}

	scriptParts = append(scriptParts, "Write-Host \"Completed: $successCount shortcuts\"")

	// 合并所有脚本部分
	scriptContent := strings.Join(scriptParts, "\n")

	cmd := exec.Command("powershell", "-ExecutionPolicy", "Bypass", "-Command", scriptContent)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.CombinedOutput()
	if err != nil {
		// 如果失败，尝试更简单的方法
		log.Printf("批量PowerShell失败，尝试逐个处理: %v", err)
		return sr.restoreShortcutsOneByOne(shortcuts, chromePath)
	}

	// 解析成功数量
	outputStr := string(output)
	lines := strings.Split(outputStr, "\n")

	for _, line := range lines {
		if strings.Contains(line, "Completed:") && strings.Contains(line, "shortcuts") {
			re := regexp.MustCompile(`Completed: (\d+) shortcuts`)
			matches := re.FindStringSubmatch(line)
			if len(matches) > 1 {
				if count, err := strconv.Atoi(matches[1]); err == nil {
					return count, nil
				}
			}
		}
	}

	return 0, fmt.Errorf("无法解析处理结果")
}

func (sr *ShortcutRestorer) restoreShortcutsOneByOne(shortcuts []string, chromePath string) (int, error) {
	successCount := 0

	for _, shortcut := range shortcuts {
		cmd := exec.Command("powershell", "-ExecutionPolicy", "Bypass", "-Command", fmt.Sprintf(`
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut('%s')
$shortcut.IconLocation = '%s,0'
$shortcut.Save()
`, strings.ReplaceAll(shortcut, "'", "''"), strings.ReplaceAll(chromePath, "'", "''")))
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

		if err := cmd.Run(); err != nil {
			log.Printf("还原快捷方式失败 %s: %v", filepath.Base(shortcut), err)
		} else {
			successCount++
		}
	}

	return successCount, nil
}

func (sr *ShortcutRestorer) refreshIconCache() {
	cmd := exec.Command("ie4uinit.exe", "-show")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}

func (im *IconManager) refreshIconCache() {
	cmd := exec.Command("ie4uinit.exe", "-show")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}

package utils

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"github.com/sergeymakinen/go-ico"
)

type OptimizedIconManager struct {
	font           *truetype.Font
	iconDir        string
	chromeTemplate string

	// 性能优化组件
	iconCache         *IconCache
	precomputedShapes *PrecomputedShapes
	workerPool        chan struct{}
}

type IconCache struct {
	cache       map[string]*CachedIcon
	sizeLRU     *LRUCache
	maxSize     int64
	currentSize int64
	ttl         time.Duration
	mu          sync.RWMutex
}

type CachedIcon struct {
	Path       string
	Size       int64
	CreatedAt  time.Time
	LastAccess time.Time
}

type LRUCache struct {
	capacity int
	items    map[string]*LRUItem
	head     *LRUItem
	tail     *LRUItem
	mu       sync.Mutex
}

type LRUItem struct {
	key   string
	value interface{}
	prev  *LRUItem
	next  *LRUItem
}

type PrecomputedShapes struct {
	ellipseMasks map[string][][]bool
	textMetrics  map[string]TextMetric
	mu           sync.RWMutex
}

type TextMetric struct {
	Width   int
	Height  int
	OffsetX int
	OffsetY int
}

func NewOptimizedIconManager() (*OptimizedIconManager, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取可执行文件路径失败: %v", err)
	}
	execDir := filepath.Dir(execPath)

	// 设置图标目录（确保在bin目录下）
	iconDir := filepath.Join(execDir, "icons")

	possiblePaths := []string{
		filepath.Join(execDir, "icons", "chrome.png"),
		filepath.Join(execDir, "build", "chrome.png"),
		filepath.Join("build", "chrome.png"),
		filepath.Join("Old", "icons", "chrome.png"),
		filepath.Join(execDir, "..", "build", "chrome.png"), // 向上一级目录查找
	}

	chromeTemplate := ""
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			chromeTemplate = path
			break
		}
	}

	if chromeTemplate == "" {
		log.Printf("⚠️ 警告：Chrome模板图标不存在，图标可能不会有背景")
	}

	// 确保图标目录存在
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return nil, fmt.Errorf("创建图标目录失败: %v", err)
	}

	// 加载字体
	font, err := loadArialFont()
	if err != nil {
		log.Printf("加载Arial字体失败，使用默认字体: %v", err)
	}

	iconCache := &IconCache{
		cache:       make(map[string]*CachedIcon),
		sizeLRU:     NewLRUCache(100),
		maxSize:     50 * 1024 * 1024,
		currentSize: 0,
		ttl:         24 * time.Hour,
	}

	precomputedShapes := &PrecomputedShapes{
		ellipseMasks: make(map[string][][]bool),
		textMetrics:  make(map[string]TextMetric),
	}

	manager := &OptimizedIconManager{
		font:              font,
		iconDir:           iconDir,
		chromeTemplate:    chromeTemplate,
		iconCache:         iconCache,
		precomputedShapes: precomputedShapes,
		workerPool:        make(chan struct{}, runtime.NumCPU()), // 限制并发数
	}

	// 预计算常用形状
	go manager.precomputeCommonShapes()

	return manager, nil
}

func NewLRUCache(capacity int) *LRUCache {
	lru := &LRUCache{
		capacity: capacity,
		items:    make(map[string]*LRUItem),
	}

	lru.head = &LRUItem{}
	lru.tail = &LRUItem{}
	lru.head.next = lru.tail
	lru.tail.prev = lru.head

	return lru
}

func (im *OptimizedIconManager) GenerateColorIcon(windowNumber int, size int) (string, error) {
	iconPath := filepath.Join(im.iconDir, fmt.Sprintf("%d.ico", windowNumber))
	cacheKey := fmt.Sprintf("%d_%d", windowNumber, size)

	if cachedIcon := im.iconCache.Get(cacheKey); cachedIcon != nil {
		// 验证文件是否存在
		if _, err := os.Stat(cachedIcon.Path); err == nil {
			return cachedIcon.Path, nil
		}
		// 文件不存在，从缓存中移除
		im.iconCache.Remove(cacheKey)
	}

	if generatedIconIsCurrent(iconPath, im.chromeTemplate) {
		// 文件存在，添加到缓存
		if stat, err := os.Stat(iconPath); err == nil {
			im.iconCache.Put(cacheKey, &CachedIcon{
				Path:       iconPath,
				Size:       stat.Size(),
				CreatedAt:  stat.ModTime(),
				LastAccess: time.Now(),
			})
		}
		return iconPath, nil
	}

	im.workerPool <- struct{}{}
	defer func() { <-im.workerPool }()

	img := image.NewRGBA(image.Rect(0, 0, size, size))

	if im.chromeTemplate != "" {
		if bgImg, err := loadChromeBackground(im.chromeTemplate, size); err == nil {
			draw.Draw(img, img.Bounds(), bgImg, image.Point{0, 0}, draw.Over)
		}
	}

	// 计算椭圆参数（严格按照修改前的正确代码）
	scaleFactor := float64(size) / 48.0
	ellipseWidth := float64(size) * 0.85
	ellipseHeight := float64(size) * 0.5 // 恢复原版参数
	ellipseLeft := (float64(size) - ellipseWidth) / 2
	ellipseTop := (float64(size)-ellipseHeight)/2 + (12.0 * scaleFactor) // 使用原版参数

	ellipseColor := color.RGBA{30, 30, 30, 255}
	im.drawOptimizedEllipse(img, int(ellipseLeft), int(ellipseTop), int(ellipseWidth), int(ellipseHeight), ellipseColor)

	// 绘制文字（根据数字位数自动调整字体大小）
	text := strconv.Itoa(windowNumber)

	// 根据数字位数调整字体大小
	var baseFontSize float64
	switch len(text) {
	case 1, 2, 3:
		baseFontSize = 22.0
	case 4:
		baseFontSize = 16.0
	case 5:
		baseFontSize = 12.0
	default:
		baseFontSize = 10.0
	}

	fontSize := baseFontSize * scaleFactor
	textOffsetY := 13.0 * scaleFactor // 保持垂直偏移不变

	if err := im.drawOptimizedText(img, text, fontSize, textOffsetY); err != nil {
		// 尝试使用原来的方法绘制文字
		im.drawSimpleText(img, text, fontSize, textOffsetY)
	}

	sizes := []int{48, 256}
	if size != 48 && size != 256 {
		sizes = append(sizes, size)
	}

	if err := im.saveAsICO(img, iconPath, sizes); err != nil {
		return "", fmt.Errorf("保存ICO文件失败: %v", err)
	}

	// 添加到缓存
	if stat, err := os.Stat(iconPath); err == nil {
		im.iconCache.Put(cacheKey, &CachedIcon{
			Path:       iconPath,
			Size:       stat.Size(),
			CreatedAt:  time.Now(),
			LastAccess: time.Now(),
		})
	}

	return iconPath, nil
}

func (im *OptimizedIconManager) drawOptimizedEllipse(img *image.RGBA, x, y, width, height int, col color.RGBA) {
	maskKey := fmt.Sprintf("ellipse_%d_%d", width, height)

	im.precomputedShapes.mu.RLock()
	mask, exists := im.precomputedShapes.ellipseMasks[maskKey]
	im.precomputedShapes.mu.RUnlock()

	if !exists {
		// 生成并缓存掩码
		mask = im.generateEllipseMask(width, height)
		im.precomputedShapes.mu.Lock()
		im.precomputedShapes.ellipseMasks[maskKey] = mask
		im.precomputedShapes.mu.Unlock()
	}

	for my := 0; my < height && my < len(mask); my++ {
		for mx := 0; mx < width && mx < len(mask[my]); mx++ {
			if mask[my][mx] {
				px := x + mx
				py := y + my
				if px >= 0 && py >= 0 && px < img.Bounds().Dx() && py < img.Bounds().Dy() {
					img.Set(px, py, col)
				}
			}
		}
	}
}

func (im *OptimizedIconManager) generateEllipseMask(width, height int) [][]bool {
	mask := make([][]bool, height)
	for i := range mask {
		mask[i] = make([]bool, width)
	}

	centerX := float64(width) / 2.0
	centerY := float64(height) / 2.0
	a := centerX
	b := centerY

	// 预计算椭圆掩码
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dx := float64(x) - centerX
			dy := float64(y) - centerY

			if (dx*dx)/(a*a)+(dy*dy)/(b*b) <= 1.0 {
				mask[y][x] = true
			}
		}
	}

	return mask
}

func (im *OptimizedIconManager) drawOptimizedText(img *image.RGBA, text string, fontSize, offsetY float64) error {
	if im.font == nil {
		return fmt.Errorf("字体未加载")
	}

	metricKey := fmt.Sprintf("text_%s_%.1f", text, fontSize)

	im.precomputedShapes.mu.RLock()
	metric, exists := im.precomputedShapes.textMetrics[metricKey]
	im.precomputedShapes.mu.RUnlock()

	if !exists {
		// 计算并缓存文本度量（根据字体大小动态调整）
		textWidth := int(fontSize * float64(len(text)) * 0.5)

		// 根据字体大小调整水平偏移
		var xOffset int
		if len(text) >= 4 {
			xOffset = 0
		} else {
			xOffset = -2
		}

		metric = TextMetric{
			Width:   textWidth,
			Height:  int(fontSize),
			OffsetX: (img.Bounds().Dx()-textWidth)/2 + xOffset,
			OffsetY: img.Bounds().Dy()/2 + int(offsetY) + int(fontSize*0.35),
		}

		im.precomputedShapes.mu.Lock()
		im.precomputedShapes.textMetrics[metricKey] = metric
		im.precomputedShapes.mu.Unlock()
	}

	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(im.font)
	c.SetFontSize(fontSize)
	c.SetClip(img.Bounds())
	c.SetDst(img)
	c.SetSrc(image.NewUniform(color.RGBA{255, 255, 255, 255}))

	pt := freetype.Pt(metric.OffsetX, metric.OffsetY)
	_, err := c.DrawString(text, pt)
	return err
}

func (im *OptimizedIconManager) precomputeCommonShapes() {
	// 预计算常用尺寸的椭圆
	commonSizes := []struct{ width, height int }{
		{40, 20},
		{217, 108},
		{85, 42},
		{170, 85},
	}

	for _, size := range commonSizes {
		maskKey := fmt.Sprintf("ellipse_%d_%d", size.width, size.height)

		im.precomputedShapes.mu.RLock()
		_, exists := im.precomputedShapes.ellipseMasks[maskKey]
		im.precomputedShapes.mu.RUnlock()

		if !exists {
			mask := im.generateEllipseMask(size.width, size.height)
			im.precomputedShapes.mu.Lock()
			im.precomputedShapes.ellipseMasks[maskKey] = mask
			im.precomputedShapes.mu.Unlock()
		}
	}
}

func (ic *IconCache) Get(key string) *CachedIcon {
	ic.mu.RLock()
	defer ic.mu.RUnlock()

	icon, exists := ic.cache[key]
	if !exists {
		return nil
	}

	if time.Since(icon.CreatedAt) > ic.ttl {
		return nil
	}

	// 更新访问时间
	icon.LastAccess = time.Now()

	ic.sizeLRU.Get(key)

	return icon
}

func (ic *IconCache) Put(key string, icon *CachedIcon) {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	if ic.currentSize+icon.Size > ic.maxSize {
		ic.evictLRU()
	}

	ic.cache[key] = icon
	ic.currentSize += icon.Size
	ic.sizeLRU.Put(key, icon)
}

func (ic *IconCache) Remove(key string) {
	ic.mu.Lock()
	defer ic.mu.Unlock()

	if icon, exists := ic.cache[key]; exists {
		ic.currentSize -= icon.Size
		delete(ic.cache, key)
		ic.sizeLRU.Remove(key)
	}
}

func (ic *IconCache) evictLRU() {

	var toRemove []string
	count := len(ic.cache) / 2

	for key, icon := range ic.cache {
		if count <= 0 {
			break
		}
		if time.Since(icon.LastAccess) > time.Hour {
			toRemove = append(toRemove, key)
			count--
		}
	}

	for _, key := range toRemove {
		if icon, exists := ic.cache[key]; exists {
			ic.currentSize -= icon.Size
			delete(ic.cache, key)
		}
	}
}

func (lru *LRUCache) Get(key string) interface{} {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if item, exists := lru.items[key]; exists {
		lru.moveToHead(item)
		return item.value
	}
	return nil
}

func (lru *LRUCache) Put(key string, value interface{}) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if item, exists := lru.items[key]; exists {
		item.value = value
		lru.moveToHead(item)
		return
	}

	newItem := &LRUItem{
		key:   key,
		value: value,
	}

	lru.items[key] = newItem
	lru.addToHead(newItem)

	if len(lru.items) > lru.capacity {
		tail := lru.removeTail()
		delete(lru.items, tail.key)
	}
}

func (lru *LRUCache) Remove(key string) {
	lru.mu.Lock()
	defer lru.mu.Unlock()

	if item, exists := lru.items[key]; exists {
		lru.removeItem(item)
		delete(lru.items, key)
	}
}

func (lru *LRUCache) moveToHead(item *LRUItem) {
	lru.removeItem(item)
	lru.addToHead(item)
}

func (lru *LRUCache) addToHead(item *LRUItem) {
	item.prev = lru.head
	item.next = lru.head.next

	lru.head.next.prev = item
	lru.head.next = item
}

func (lru *LRUCache) removeItem(item *LRUItem) {
	item.prev.next = item.next
	item.next.prev = item.prev
}

func (lru *LRUCache) removeTail() *LRUItem {
	lastItem := lru.tail.prev
	lru.removeItem(lastItem)
	return lastItem
}

func (im *OptimizedIconManager) saveAsICO(img image.Image, path string, _ []int) error {
	// sizes 参数保留以保持接口兼容性，但 ico.Encode 会自动处理多尺寸
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// 直接保存原图像，ico库会处理多尺寸
	return ico.Encode(file, img)
}

func (im *OptimizedIconManager) GetCacheStats() map[string]interface{} {
	im.iconCache.mu.RLock()
	defer im.iconCache.mu.RUnlock()

	im.precomputedShapes.mu.RLock()
	defer im.precomputedShapes.mu.RUnlock()

	return map[string]interface{}{
		"icon_cache_size":     len(im.iconCache.cache),
		"icon_cache_bytes":    im.iconCache.currentSize,
		"precomputed_shapes":  len(im.precomputedShapes.ellipseMasks),
		"precomputed_metrics": len(im.precomputedShapes.textMetrics),
	}
}

func (im *OptimizedIconManager) ClearCache() error {
	im.iconCache.mu.Lock()
	im.iconCache.cache = make(map[string]*CachedIcon)
	im.iconCache.currentSize = 0
	im.iconCache.sizeLRU = NewLRUCache(100)
	im.iconCache.mu.Unlock()

	return nil
}

func (im *OptimizedIconManager) ApplyIconsToWindows(windows []ChromeProcessInfo, autoModifyShortcut bool, shortcutDir string) {
	// 提取窗口编号
	windowNumbers := make([]int, 0, len(windows))
	windowData := make(map[int]uintptr)

	for _, window := range windows {
		if window.Number <= 0 {
			log.Printf("跳过无效窗口编号的图标设置: PID=%d, 编号=%d", window.PID, window.Number)
			continue
		}
		windowNumbers = append(windowNumbers, window.Number)
		windowData[window.Number] = window.HWND
	}

	// 并行生成优化图标（使用工作池限制并发）
	iconPaths := im.generateIconsOptimized(windowNumbers)

	// 更新快捷方式图标（如果启用）
	if autoModifyShortcut && shortcutDir != "" && len(iconPaths) > 0 {
		go func() {
			// 后台异步处理快捷方式图标
			im.updateShortcutIconsOptimized(shortcutDir, iconPaths)
		}()
	}

	// 并行设置窗口图标
	im.setWindowIconsOptimized(windowData, iconPaths)
}

func (im *OptimizedIconManager) generateIconsOptimized(windowNumbers []int) map[int]string {
	iconPaths := make(map[int]string)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, number := range windowNumbers {
		wg.Add(1)

		im.workerPool <- struct{}{}

		go func(num int) {
			defer wg.Done()
			defer func() { <-im.workerPool }()

			if path, err := im.GenerateColorIcon(num, 256); err == nil {
				mu.Lock()
				iconPaths[num] = path
				mu.Unlock()
			}
		}(number)
	}

	wg.Wait()
	return iconPaths
}

func (im *OptimizedIconManager) setWindowIconsOptimized(windowData map[int]uintptr, iconPaths map[int]string) int {
	var successCount int32
	var wg sync.WaitGroup

	for number, hwnd := range windowData {
		if iconPath, exists := iconPaths[number]; exists {
			wg.Add(1)

			go func(windowNumber int, windowHWND uintptr, path string) {
				defer wg.Done()

				if im.setWindowIconOptimized(windowHWND, path) {
					atomic.AddInt32(&successCount, 1)
				}
			}(number, hwnd, iconPath)
		}
	}

	wg.Wait()
	return int(successCount)
}

func (im *OptimizedIconManager) setWindowIconOptimized(hwnd uintptr, iconPath string) bool {
	// 验证窗口有效性
	ret, _, _ := iconIsWindow.Call(hwnd)
	if ret == 0 {
		return false
	}

	// 加载图标
	iconPtr, _ := syscall.UTF16PtrFromString(iconPath)
	hIcon, _, _ := iconLoadImageW.Call(
		0,
		uintptr(unsafe.Pointer(iconPtr)),
		IMAGE_ICON,
		0, 0,
		LR_LOADFROMFILE,
	)

	if hIcon == 0 {
		return false
	}

	// 设置大图标和小图标
	iconSendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, hIcon)
	iconSendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, hIcon)

	return true
}

func (im *OptimizedIconManager) updateShortcutIconsOptimized(shortcutDir string, iconPaths map[int]string) {
	if _, err := os.Stat(shortcutDir); os.IsNotExist(err) {
		return
	}

	for number, iconPath := range iconPaths {
		shortcutPath := filepath.Join(shortcutDir, fmt.Sprintf("%d.lnk", number))

		if _, err := os.Stat(shortcutPath); err == nil {
			if _, err := os.Stat(iconPath); err == nil {
				// 转换为绝对路径
				absIconPath, err := filepath.Abs(iconPath)
				if err != nil {
					continue
				}

				im.updateShortcutIcon(shortcutPath, absIconPath)
			}
		}
	}
}

func (im *OptimizedIconManager) drawSimpleText(img *image.RGBA, text string, fontSize, offsetY float64) error {
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

	// 计算文字位置（根据字体大小动态调整）
	textWidth := int(fontSize * float64(len(text)) * 0.5)

	// 根据字体大小调整水平偏移
	var xOffset int
	if len(text) >= 4 {
		xOffset = 0
	} else {
		xOffset = -2
	}

	x := (img.Bounds().Dx()-textWidth)/2 + xOffset
	y := img.Bounds().Dy()/2 + int(offsetY) + int(fontSize*0.35)

	pt := freetype.Pt(x, y)
	_, err := c.DrawString(text, pt)
	return err
}

func (im *OptimizedIconManager) updateShortcutIcon(shortcutPath, iconPath string) bool {
	if _, err := os.Stat(shortcutPath); os.IsNotExist(err) {
		return false
	}

	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		return false
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
		return false
	}

	return true
}

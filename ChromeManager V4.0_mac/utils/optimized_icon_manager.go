package utils

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
)

type CachedIcon struct {
	Path       string
	Size       int64
	CreatedAt  time.Time
	LastAccess time.Time
}

type IconCache struct {
	cache       map[string]*CachedIcon
	sizeLRU     *LRUCache
	maxSize     int64
	currentSize int64
	ttl         time.Duration
	mu          sync.RWMutex
}

func (c *IconCache) Get(key string) *CachedIcon {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache[key]
}

func (c *IconCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cache, key)
}

func (c *IconCache) Put(key string, icon *CachedIcon) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = icon
}

type TextMetric struct {
	Width   int
	Height  int
	OffsetX int
	OffsetY int
}

type PrecomputedShapes struct {
	ellipseMasks map[string][][]bool
	textMetrics  map[string]TextMetric
	mu           sync.RWMutex
}

type OptimizedIconManager struct {
	font           *truetype.Font
	iconDir        string
	chromeTemplate string

	// 性能优化组件
	iconCache         *IconCache
	precomputedShapes *PrecomputedShapes
	workerPool        chan struct{}
}

func NewOptimizedIconManager() (*OptimizedIconManager, error) {
	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("获取可执行文件路径失败: %v", err)
	}
	execDir := filepath.Dir(execPath)

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("获取用户缓存目录失败: %v", err)
	}
	iconDir := filepath.Join(cacheDir, "ChromeManager", "icons")
	bundleIconsDir := filepath.Clean(filepath.Join(execDir, "..", "Resources", "icons"))
	possiblePaths := []string{
		filepath.Join(bundleIconsDir, "chrome.png"),
		filepath.Join(iconDir, "chrome.png"),
		filepath.Join(execDir, "build", "chrome.png"),
		filepath.Join("build", "chrome.png"),
		filepath.Join("Old", "icons", "chrome.png"),
		filepath.Join(execDir, "..", "build", "chrome.png"),
		filepath.Join(bundleIconsDir, "appicon.png"),
		filepath.Join(iconDir, "appicon.png"),
	}

	chromeTemplate := ""
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			chromeTemplate = path
			break
		}
	}

	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return nil, fmt.Errorf("创建图标目录失败: %v", err)
	}

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
		workerPool:        make(chan struct{}, runtime.NumCPU()),
	}
	go manager.precomputeCommonShapes()
	return manager, nil
}

func (im *OptimizedIconManager) GenerateColorIcon(windowNumber int, size int) (string, error) {
	// macOS ICNS format needed. We generate PNG and use sips to convert to ICNS.
	if size == 0 {
		size = 256
	}
	pngPath := filepath.Join(im.iconDir, fmt.Sprintf("%d.png", windowNumber))
	icnsPath := filepath.Join(im.iconDir, fmt.Sprintf("%d.icns", windowNumber))
	cacheKey := fmt.Sprintf("%d_%d_mac", windowNumber, size)

	if cachedIcon := im.iconCache.Get(cacheKey); cachedIcon != nil {
		if _, err := os.Stat(cachedIcon.Path); err == nil {
			return cachedIcon.Path, nil
		}
		im.iconCache.Remove(cacheKey)
	}

	if _, err := os.Stat(icnsPath); err == nil {
		if stat, err := os.Stat(icnsPath); err == nil {
			im.iconCache.Put(cacheKey, &CachedIcon{
				Path:       icnsPath,
				Size:       stat.Size(),
				CreatedAt:  stat.ModTime(),
				LastAccess: time.Now(),
			})
		}
		return icnsPath, nil
	}

	im.workerPool <- struct{}{}
	defer func() { <-im.workerPool }()

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	if im.chromeTemplate != "" {
		if bgImg, err := loadChromeBackground(im.chromeTemplate, size); err == nil {
			draw.Draw(img, img.Bounds(), bgImg, image.Point{0, 0}, draw.Over)
		}
	}

	scaleFactor := float64(size) / 48.0
	ellipseWidth := float64(size) * 0.85
	ellipseHeight := float64(size) * 0.5
	ellipseLeft := (float64(size) - ellipseWidth) / 2
	ellipseTop := (float64(size)-ellipseHeight)/2 + (12.0 * scaleFactor)

	ellipseColor := color.RGBA{30, 30, 30, 255}
	im.drawOptimizedEllipse(img, int(ellipseLeft), int(ellipseTop), int(ellipseWidth), int(ellipseHeight), ellipseColor)

	text := strconv.Itoa(windowNumber)
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
	textOffsetY := 13.0 * scaleFactor

	if err := im.drawOptimizedText(img, text, fontSize, textOffsetY); err != nil {
		im.drawSimpleText(img, text, fontSize, textOffsetY)
	}

	// Save PNG
	f, err := os.Create(pngPath)
	if err != nil {
		return "", fmt.Errorf("创建 PNG 文件失败: %v", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return "", fmt.Errorf("编码 PNG 文件失败: %v", err)
	}
	f.Close()

	// Convert PNG to ICNS using sips
	cmd := exec.Command("sips", "-s", "format", "icns", pngPath, "--out", icnsPath)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("PNG 转 ICNS 失败: %v", err)
	}

	// Keep the ICNS, delete the temp PNG if we want to save space, but let's just keep both or delete PNG.
	os.Remove(pngPath)

	if stat, err := os.Stat(icnsPath); err == nil {
		im.iconCache.Put(cacheKey, &CachedIcon{
			Path:       icnsPath,
			Size:       stat.Size(),
			CreatedAt:  time.Now(),
			LastAccess: time.Now(),
		})
	}
	return icnsPath, nil
}

func (im *OptimizedIconManager) GenerateAndApplyMacEnvironmentIcon(windowNumber int, appPath string) error {
	icnsPath, err := im.GenerateColorIcon(windowNumber, 256)
	if err != nil {
		return err
	}

	// Copy the ICNS to the app's Resources directory as "appicon.icns"
	destPath := filepath.Join(appPath, "Contents", "Resources", "appicon.icns")
	if err := CopyFile(icnsPath, destPath); err != nil {
		return err
	}

	// Touch the app bundle to force Finder to refresh its icon
	exec.Command("touch", appPath).Run()
	return nil
}

// -------------------------------------------------------------
// HELPER METHODS (Drawn from Windows implementation)
// -------------------------------------------------------------

func (im *OptimizedIconManager) drawOptimizedEllipse(img *image.RGBA, x, y, width, height int, col color.RGBA) {
	maskKey := fmt.Sprintf("ellipse_%d_%d", width, height)
	im.precomputedShapes.mu.RLock()
	mask, exists := im.precomputedShapes.ellipseMasks[maskKey]
	im.precomputedShapes.mu.RUnlock()

	if !exists {
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
		textWidth := int(fontSize * float64(len(text)) * 0.5)
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

	textWidth := int(fontSize * float64(len(text)) * 0.5)
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

func (im *OptimizedIconManager) precomputeCommonShapes() {
	commonSizes := []struct{ width, height int }{
		{40, 20}, {217, 108}, {85, 42}, {170, 85},
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

// macOS API Stubs
func (im *OptimizedIconManager) ApplyIconsToWindows(windows []ChromeProcessInfo, autoModifyShortcut bool, shortcutDir string) {
	// Not needed on macOS at runtime, we apply to .app via GenerateAndApplyMacEnvironmentIcon
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

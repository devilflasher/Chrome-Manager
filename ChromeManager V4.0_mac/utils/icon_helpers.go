package utils

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
)

func loadArialFont() (*truetype.Font, error) {
	var fontPath string

	switch runtime.GOOS {
	case "windows":
		fontPath = filepath.Join(os.Getenv("WINDIR"), "Fonts", "arialbd.ttf")
		if _, err := os.Stat(fontPath); err != nil {
			fontPath = filepath.Join(os.Getenv("WINDIR"), "Fonts", "arial.ttf")
		}
	case "darwin":
		fontPath = "/System/Library/Fonts/Supplemental/Arial Bold.ttf"
		if _, err := os.Stat(fontPath); err != nil {
			fontPath = "/System/Library/Fonts/Supplemental/Arial.ttf"
			if _, err := os.Stat(fontPath); err != nil {
				// Fallback to older macOS location
				fontPath = "/Library/Fonts/Arial Bold.ttf"
				if _, err := os.Stat(fontPath); err != nil {
					fontPath = "/Library/Fonts/Arial.ttf"
				}
			}
		}
	default:
		return nil, fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		return nil, fmt.Errorf("读取字体失败: %v", err)
	}

	return freetype.ParseFont(fontBytes)
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

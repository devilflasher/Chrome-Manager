package common

import (
	"runtime"
)

// GetPlatformName 返回当前平台名称。
func GetPlatformName() string {
	return runtime.GOOS
}

// IsPlatformSupported 检查指定平台是否支持。
func IsPlatformSupported(platform string) bool {
	switch platform {
	case "windows": // 目前支持的平台
		return true
	case "linux": // 计划支持
		return false
	default:
		return false
	}
}

// IsCurrentPlatformSupported 检查当前平台是否支持。
func IsCurrentPlatformSupported() bool {
	return IsPlatformSupported(runtime.GOOS)
}

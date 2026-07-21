//go:build windows

package platform

import (
	"chromemanager/platform/common"
	"chromemanager/platform/windows"
)

// NewProvider 根据当前操作系统创建对应的PlatformProvider。
// 这是创建平台Provider的主要入口点。
//
// Windows平台实现:
//   provider, err := platform.NewProvider()
//   if err != nil {
//       log.Fatal(err)
//   }
//   defer provider.Shutdown()
//
//   handle, err := provider.FindWindow("Chrome_WidgetWin_1", "")
//   if err != nil {
//       log.Fatal(err)
//   }
func NewProvider() (common.PlatformProvider, error) {
	return windows.NewProvider()
}

// MustNewProvider 创建PlatformProvider,如果失败则panic。
// 适用于初始化代码,不期望失败的场景。
func MustNewProvider() common.PlatformProvider {
	provider, err := NewProvider()
	if err != nil {
		panic("failed to create Windows platform provider: " + err.Error())
	}
	return provider
}

//go:build darwin

package platform

import (
	"chromemanager/platform/common"
	"chromemanager/platform/darwin"
)

// NewProvider 根据当前操作系统创建对应的PlatformProvider。
// 这是创建平台Provider的主要入口点。
//
// macOS平台实现:
//   provider, err := platform.NewProvider()
//   if err != nil {
//       log.Fatal(err)
//   }
//   defer provider.Shutdown()
//
//   handle, err := provider.FindWindow("", "Google Chrome")
//   if err != nil {
//       log.Fatal(err)
//   }
func NewProvider() (common.PlatformProvider, error) {
	return darwin.NewProvider()
}

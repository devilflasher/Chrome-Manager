//go:build windows

package windows

import (
	"fmt"
)

// SHChangeNotify 通知 Shell 图标或文件关联已更改
// 从 utils.SHChangeNotify 迁移 ✅
func (p *Provider) SHChangeNotify() error {
	// SHCNE_ASSOCCHANGED = 0x08000000
	// SHCNF_IDLIST = 0x0000
	ret, _, _ := procSHChangeNotify.Call(
		0x08000000, // SHCNE_ASSOCCHANGED
		0x0000,     // SHCNF_IDLIST
		0,          // pszPath1 (NULL)
		0,          // pszPath2 (NULL)
	)
	if ret == 0 {
		return fmt.Errorf("failed to notify shell of icon change")
	}
	return nil
}

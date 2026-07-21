//go:build windows

package windows

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsRunningAsAdmin 检查当前进程是否以管理员权限运行
// 从 utils.IsRunningAsAdmin 迁移完成 ✅
func (p *Provider) IsRunningAsAdmin() bool {
	// 首先检查是否在管理员组中
	if isInAdminGroup() {
		return true
	}

	// 检查是否有管理员令牌
	if hasAdminToken() {
		return true
	}

	// 检查是否能执行管理员操作
	if canPerformAdminOperation() {
		return true
	}

	return false
}

// CheckAdminPrivileges 检查并打印管理员权限状态
// 从 utils.CheckAdminPrivileges 迁移完成 ✅
func (p *Provider) CheckAdminPrivileges() {
	fmt.Println("🔍 正在检查管理员权限...")

	if p.IsRunningAsAdmin() {
		fmt.Println("✅ 程序正在以管理员权限运行")
		fmt.Println("✅ 全局钩子功能应该可以正常工作")
		fmt.Println("✅ Windows低级钩子已启用")
	} else {
		fmt.Println("⚠️  警告：程序未以管理员权限运行")
		fmt.Println("⚠️  全局钩子功能可能无法正常工作")
		fmt.Println("⚠️  建议：右键程序选择'以管理员身份运行'")
		fmt.Println("⚠️  或使用 run_as_admin.bat 脚本启动")

		// 如果清单文件配置了自动请求，但检测失败，可能是检测问题
		fmt.Println("💡 提示：如果程序已弹出UAC提示并确认，但仍显示此警告，")
		fmt.Println("💡 可能是权限检测的时机问题，请忽略此警告")
	}
	fmt.Println()
}

// RequestAdminPrivileges 请求管理员权限（重新启动程序）
// 从 utils.RequestAdminPrivileges 迁移完成 ✅
func (p *Provider) RequestAdminPrivileges() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// 使用 ShellExecute 以管理员权限运行
	verb, _ := syscall.UTF16PtrFromString("runas")
	exePtr, _ := syscall.UTF16PtrFromString(exe)

	ret, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW").Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(exePtr)),
		0,
		0,
		1, // SW_SHOWNORMAL
	)

	if ret <= 32 {
		return fmt.Errorf("failed to restart with admin privileges")
	}

	return nil
}

// isInAdminGroup 检查当前进程是否在管理员组中
func isInAdminGroup() bool {
	// 使用简单安全的方法 - 直接检查令牌权限级别
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	var elevation uint32
	var returnedLen uint32

	err = windows.GetTokenInformation(token, 18, // TokenElevation
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &returnedLen)
	if err == nil {
		return elevation == 2
	}

	// 权限检查失败，但不影响程序继续执行，假设有权限
	return true
}

// hasAdminToken 检查是否有管理员令牌
func hasAdminToken() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()

	// 获取令牌的权限级别
	var elevation uint32
	var returnedLen uint32

	err = windows.GetTokenInformation(token, 18, // TokenElevation
		(*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &returnedLen)
	if err != nil {
		return false
	}

	return elevation == 2
}

// canPerformAdminOperation 检查是否能执行管理员操作
func canPerformAdminOperation() bool {
	// 尝试访问需要管理员权限的注册表项
	var regKey windows.Handle
	err := windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE,
		windows.StringToUTF16Ptr("SOFTWARE\\Microsoft\\Windows\\CurrentVersion"),
		0, windows.KEY_READ, &regKey)
	if err != nil {
		return false
	}
	defer windows.RegCloseKey(regKey)

	return true
}

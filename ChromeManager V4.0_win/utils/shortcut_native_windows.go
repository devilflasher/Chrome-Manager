//go:build windows

package utils

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
)

var (
	clsidShellLink = ole.NewGUID("{00021401-0000-0000-C000-000000000046}")
	iidShellLinkW  = ole.NewGUID("{000214F9-0000-0000-C000-000000000046}")
	iidPersistFile = ole.NewGUID("{0000010B-0000-0000-C000-000000000046}")
)

type shellLinkW struct {
	vtbl *shellLinkWVtbl
}

type shellLinkWVtbl struct {
	queryInterface      uintptr
	addRef              uintptr
	release             uintptr
	getPath             uintptr
	getIDList           uintptr
	setIDList           uintptr
	getDescription      uintptr
	setDescription      uintptr
	getWorkingDirectory uintptr
	setWorkingDirectory uintptr
	getArguments        uintptr
	setArguments        uintptr
	getHotkey           uintptr
	setHotkey           uintptr
	getShowCmd          uintptr
	setShowCmd          uintptr
	getIconLocation     uintptr
	setIconLocation     uintptr
	setRelativePath     uintptr
	resolve             uintptr
	setPath             uintptr
}

type persistFile struct {
	vtbl *persistFileVtbl
}

type persistFileVtbl struct {
	queryInterface uintptr
	addRef         uintptr
	release        uintptr
	getClassID     uintptr
	isDirty        uintptr
	load           uintptr
	save           uintptr
	saveCompleted  uintptr
	getCurFile     uintptr
}

func createNativeShortcut(shortcutPath, targetPath, arguments, workingDir string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	shouldUninitialize, err := initializeShortcutCOM()
	if err != nil {
		return err
	}
	if shouldUninitialize {
		defer ole.CoUninitialize()
	}

	unknown, err := ole.CreateInstance(clsidShellLink, iidShellLinkW)
	if err != nil {
		return fmt.Errorf("创建 IShellLinkW 失败: %w", err)
	}
	defer unknown.Release()

	link := (*shellLinkW)(unsafe.Pointer(unknown))
	if err := link.setString(link.vtbl.setPath, targetPath); err != nil {
		return fmt.Errorf("设置快捷方式目标失败: %w", err)
	}
	if err := link.setString(link.vtbl.setArguments, arguments); err != nil {
		return fmt.Errorf("设置快捷方式参数失败: %w", err)
	}
	if err := link.setString(link.vtbl.setWorkingDirectory, workingDir); err != nil {
		return fmt.Errorf("设置快捷方式工作目录失败: %w", err)
	}
	if err := link.setShowCommand(windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("设置快捷方式窗口状态失败: %w", err)
	}
	if err := link.setIconLocation(targetPath, 0); err != nil {
		return fmt.Errorf("设置快捷方式图标失败: %w", err)
	}

	persistUnknown, err := unknown.QueryInterface(iidPersistFile)
	if err != nil {
		return fmt.Errorf("获取 IPersistFile 失败: %w", err)
	}
	defer persistUnknown.Release()

	persist := (*persistFile)(unsafe.Pointer(persistUnknown))
	pathPtr, err := windows.UTF16PtrFromString(shortcutPath)
	if err != nil {
		return fmt.Errorf("转换快捷方式路径失败: %w", err)
	}
	if err := checkHRESULT(callCOM(persist.vtbl.save, uintptr(unsafe.Pointer(persist)), uintptr(unsafe.Pointer(pathPtr)), 1)); err != nil {
		return fmt.Errorf("保存快捷方式失败: %w", err)
	}
	return nil
}

func initializeShortcutCOM() (bool, error) {
	const (
		sFalse          = uintptr(1)
		rpcEChangedMode = uintptr(0x80010106)
	)
	err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED)
	if err == nil {
		return true, nil
	}
	oleErr, ok := err.(*ole.OleError)
	if !ok {
		return false, fmt.Errorf("初始化快捷方式 COM 组件失败: %w", err)
	}
	switch oleErr.Code() {
	case sFalse:
		return true, nil
	case rpcEChangedMode:
		return false, nil
	default:
		return false, fmt.Errorf("初始化快捷方式 COM 组件失败: %w", err)
	}
}

func (link *shellLinkW) setString(method uintptr, value string) error {
	valuePtr, err := windows.UTF16PtrFromString(value)
	if err != nil {
		return err
	}
	return checkHRESULT(callCOM(method, uintptr(unsafe.Pointer(link)), uintptr(unsafe.Pointer(valuePtr))))
}

func (link *shellLinkW) setShowCommand(showCommand int) error {
	return checkHRESULT(callCOM(link.vtbl.setShowCmd, uintptr(unsafe.Pointer(link)), uintptr(showCommand)))
}

func (link *shellLinkW) setIconLocation(iconPath string, iconIndex int32) error {
	iconPtr, err := windows.UTF16PtrFromString(iconPath)
	if err != nil {
		return err
	}
	return checkHRESULT(callCOM(
		link.vtbl.setIconLocation,
		uintptr(unsafe.Pointer(link)),
		uintptr(unsafe.Pointer(iconPtr)),
		uintptr(iconIndex),
	))
}

func callCOM(method uintptr, args ...uintptr) uintptr {
	result, _, _ := syscall.SyscallN(method, args...)
	return result
}

func checkHRESULT(result uintptr) error {
	if int32(uint32(result)) < 0 {
		return ole.NewError(result)
	}
	return nil
}

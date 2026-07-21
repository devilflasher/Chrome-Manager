//go:build windows

package windows

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SelectFolderDialog 打开文件夹选择对话框
// 从 utils.SelectFolderDialog 迁移完成 ✅
func (p *Provider) SelectFolderDialog(title string) (string, error) {
	ole32 := syscall.NewLazyDLL("ole32.dll")
	shell32 := syscall.NewLazyDLL("shell32.dll")
	user32 := syscall.NewLazyDLL("user32.dll")

	procCoInitialize := ole32.NewProc("CoInitialize")
	procCoUninitialize := ole32.NewProc("CoUninitialize")
	procSHBrowseForFolder := shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList := shell32.NewProc("SHGetPathFromIDListW")
	procGetActiveWindow := user32.NewProc("GetActiveWindow")
	procSetForegroundWindow := user32.NewProc("SetForegroundWindow")

	// 初始化COM
	procCoInitialize.Call(0)
	defer procCoUninitialize.Call()

	// 定义BROWSEINFO结构
	type BROWSEINFO struct {
		HwndOwner      uintptr
		PidlRoot       uintptr
		PszDisplayName uintptr
		LpszTitle      uintptr
		UlFlags        uint32
		Lpfn           uintptr
		LParam         uintptr
		IImage         int32
	}

	displayName := make([]uint16, 260)
	titlePtr, _ := windows.UTF16PtrFromString(title)

	parentHwnd, _, _ := procGetActiveWindow.Call()

	// 如果没有活动窗口，尝试通过进程名查找主窗口
	if parentHwnd == 0 {
		procFindWindow := user32.NewProc("FindWindowW")
		parentHwnd, _, _ = procFindWindow.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("ChromeManager V4.0"))))
	}

	bi := BROWSEINFO{
		HwndOwner:      parentHwnd, // 设置父窗口句柄
		PidlRoot:       0,
		PszDisplayName: uintptr(unsafe.Pointer(&displayName[0])),
		LpszTitle:      uintptr(unsafe.Pointer(titlePtr)),
		// BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE | BIF_EDITBOX
		UlFlags: 0x0001 | 0x0040 | 0x0100,
		Lpfn:    0,
		LParam:  0,
		IImage:  0,
	}

	// 强制设置父窗口为前台窗口
	if parentHwnd != 0 {
		procSetForegroundWindow.Call(parentHwnd)
		// 额外设置窗口为topmost
		procSetWindowPos := user32.NewProc("SetWindowPos")
		procSetWindowPos.Call(parentHwnd, ^uintptr(0), 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
		// 短暂延迟确保窗口状态更新
		time.Sleep(50 * time.Millisecond)
	}

	// 显示对话框
	pidl, _, _ := procSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))

	// 对话框关闭后，恢复父窗口的正常状态
	if parentHwnd != 0 {
		procSetWindowPos := user32.NewProc("SetWindowPos")
		procSetWindowPos.Call(parentHwnd, ^uintptr(1), 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE)
	}

	if pidl == 0 {
		return "", fmt.Errorf("user cancelled folder selection")
	}

	pathBuffer := make([]uint16, 260)
	ret, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&pathBuffer[0])))
	if ret == 0 {
		return "", fmt.Errorf("failed to get path from folder selection")
	}

	return windows.UTF16ToString(pathBuffer), nil
}

// SaveFileDialog 打开文件保存对话框
// 从 utils.SaveFileDialog 迁移完成 ✅
func (p *Provider) SaveFileDialog(title, defaultFileName, fileFilter string) (string, error) {
	// 使用 PowerShell 调用 SaveFileDialog
	psScript := fmt.Sprintf(`
		Add-Type -AssemblyName System.Windows.Forms
		$dialog = New-Object System.Windows.Forms.SaveFileDialog
		$dialog.Title = '%s'
		$dialog.FileName = '%s'
		$dialog.Filter = '%s'
		$dialog.DefaultExt = 'json'
		$result = $dialog.ShowDialog()
		if ($result -eq 'OK') {
			Write-Output $dialog.FileName
		} else {
			exit 1
		}
	`, title, defaultFileName, fileFilter)

	cmd := exec.Command("powershell", "-WindowStyle", "Hidden", "-Command", psScript)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("user cancelled file save")
	}

	path := strings.TrimSpace(string(output))
	if path == "" {
		return "", fmt.Errorf("user cancelled file save")
	}

	return path, nil
}

// SelectFileDialog 打开文件选择对话框
// SelectFileDialog 打开文件选择对话框
func (p *Provider) SelectFileDialog(title, fileFilter string) (string, error) {
	comdlg32 := syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileName := comdlg32.NewProc("GetOpenFileNameW")

	user32 := syscall.NewLazyDLL("user32.dll")
	procGetActiveWindow := user32.NewProc("GetActiveWindow")

	// 转换过滤器格式: "Description|*.ext" -> "Description\0*.ext\0\0"
	// 假设输入格式为 "Executable Files|*.exe"
	filter := strings.ReplaceAll(fileFilter, "|", "\x00")
	if len(filter) > 0 {
		filter += "\x00\x00"
	}

	// 准备缓冲区
	fileBuf := make([]uint16, 260)
	titlePtr, _ := windows.UTF16PtrFromString(title)

	// 手动转换 filter unicode
	var filterUTF16 []uint16
	if len(fileFilter) > 0 {
		// Replace | with null
		parts := strings.Split(fileFilter, "|")
		for _, part := range parts {
			u, _ := windows.UTF16FromString(part)
			filterUTF16 = append(filterUTF16, u[:len(u)-1]...) // Remove null terminator
			filterUTF16 = append(filterUTF16, 0)
		}
		filterUTF16 = append(filterUTF16, 0) // Double null terminate
	}

	type OPENFILENAME struct {
		lStructSize       uint32
		hwndOwner         uintptr
		hInstance         uintptr
		lpstrFilter       *uint16
		lpstrCustomFilter *uint16
		nMaxCustFilter    uint32
		nFilterIndex      uint32
		lpstrFile         *uint16
		nMaxFile          uint32
		lpstrFileTitle    *uint16
		nMaxFileTitle     uint32
		lpstrInitialDir   *uint16
		lpstrTitle        *uint16
		flags             uint32
		nFileOffset       uint16
		nFileExtension    uint16
		lpstrDefExt       *uint16
		lCustData         uintptr
		lpfnHook          uintptr
		lpTemplateName    *uint16
		pvReserved        uintptr
		dwReserved        uint32
		FlagsEx           uint32
	}

	hwnd, _, _ := procGetActiveWindow.Call()

	var filterC *uint16
	if len(filterUTF16) > 0 {
		filterC = &filterUTF16[0]
	}

	ofn := OPENFILENAME{
		lStructSize: 0,
		hwndOwner:   hwnd,
		lpstrFilter: filterC,
		lpstrFile:   &fileBuf[0],
		nMaxFile:    uint32(len(fileBuf)),
		lpstrTitle:  titlePtr,
		flags:       0x00000800 | 0x00001000 | 0x00080000, // OFN_PATHMUSTEXIST | OFN_FILEMUSTEXIST | OFN_EXPLORER
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))

	ret, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))

	if ret == 0 {
		return "", fmt.Errorf("user cancelled file selection")
	}

	return windows.UTF16ToString(fileBuf), nil
}

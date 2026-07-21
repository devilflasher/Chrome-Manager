//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const webview2RuntimeGUID = `{F3017226-FE2A-4295-8BDF-00C3A9C6A7}`

func ensureStartupDependencies() error {
	if webView2RuntimeInstalled() {
		return nil
	}

	return errors.New("当前系统未检测到 Microsoft Edge WebView2 Runtime。\n\nChromeManager V4.0 需要 WebView2 Runtime 来显示软件界面。\n如果你是从其他电脑复制来的 ChromeManager.exe，也需要在当前电脑安装这个运行依赖。\n\n请先安装 Microsoft Edge WebView2 Runtime 后再启动程序。\n下载地址：https://developer.microsoft.com/microsoft-edge/webview2/")
}

func showStartupError(title string, err error) {
	if err == nil {
		return
	}
	showWindowsMessageBox(title, err.Error())
}

func webView2RuntimeInstalled() bool {
	if webView2RuntimeInRegistry() {
		return true
	}
	return webView2RuntimeOnDisk()
}

func webView2RuntimeInRegistry() bool {
	roots := []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE}
	paths := []string{
		`SOFTWARE\Microsoft\EdgeUpdate\Clients\` + webview2RuntimeGUID,
		`SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + webview2RuntimeGUID,
	}

	for _, root := range roots {
		for _, path := range paths {
			key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			version, _, err := key.GetStringValue("pv")
			_ = key.Close()
			if err == nil && strings.TrimSpace(version) != "" && strings.TrimSpace(version) != "0.0.0.0" {
				return true
			}
		}
	}

	return false
}

func webView2RuntimeOnDisk() bool {
	bases := []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LocalAppData"),
	}

	for _, base := range bases {
		if base == "" {
			continue
		}
		patterns := []string{
			filepath.Join(base, "Microsoft", "EdgeWebView", "Application", "*", "msedgewebview2.exe"),
			filepath.Join(base, "Programs", "Microsoft", "EdgeWebView", "Application", "*", "msedgewebview2.exe"),
		}
		for _, pattern := range patterns {
			matches, err := filepath.Glob(pattern)
			if err == nil && len(matches) > 0 {
				return true
			}
		}
	}

	return false
}

func showWindowsMessageBox(title, message string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")

	titlePtr, _ := syscall.UTF16PtrFromString(title)
	messagePtr, _ := syscall.UTF16PtrFromString(message)

	const (
		mbOK        = 0x00000000
		mbIconStop  = 0x00000010
		mbSetForegr = 0x00010000
	)

	messageBox.Call(
		0,
		uintptr(unsafe.Pointer(messagePtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconStop|mbSetForegr),
	)
}

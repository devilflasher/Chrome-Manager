//go:build windows

package windows

import (
	"chromemanager/config"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func browserExecutableFromCommand(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if strings.HasPrefix(command, `"`) {
		rest := strings.TrimPrefix(command, `"`)
		if idx := strings.Index(rest, `"`); idx >= 0 {
			return rest[:idx]
		}
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func existingBrowserPathFromRegistry(root registry.Key, keyPath string) (string, bool) {
	k, err := registry.OpenKey(root, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()

	value, _, err := k.GetStringValue("")
	if err != nil || value == "" {
		return "", false
	}
	path := browserExecutableFromCommand(value)
	if path == "" {
		path = value
	}
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return "", false
}

// FindBrowserPath locates a Chromium-family browser executable.
// Supported browserType values: chrome, edge, opera, brave.
func (p *Provider) FindBrowserPath(browserType string) (string, error) {
	if browserType == "" {
		browserType = config.BrowserTypeChrome
	}

	var appPathsKey, clientKey, exeName string
	var commonPaths []string

	homeDir, _ := os.UserHomeDir()
	localAppData := os.Getenv("LOCALAPPDATA")

	switch browserType {
	case config.BrowserTypeChrome:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Google Chrome\shell\open\command`
		exeName = "chrome.exe"
		commonPaths = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(localAppData, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(homeDir, `AppData\Local\Google\Chrome\Application\chrome.exe`),
		}
	case config.BrowserTypeEdge:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Microsoft Edge\shell\open\command`
		exeName = "msedge.exe"
		commonPaths = []string{
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			filepath.Join(localAppData, `Microsoft\Edge\Application\msedge.exe`),
		}
	case config.BrowserTypeOpera:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\opera.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Opera\shell\open\command`
		exeName = "opera.exe"
		commonPaths = []string{
			filepath.Join(localAppData, `Programs\Opera\opera.exe`),
			filepath.Join(homeDir, `AppData\Local\Programs\Opera\opera.exe`),
			`C:\Program Files\Opera\opera.exe`,
			`C:\Program Files (x86)\Opera\opera.exe`,
		}
	case config.BrowserTypeBrave:
		appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\brave.exe`
		clientKey = `SOFTWARE\Clients\StartMenuInternet\Brave\shell\open\command`
		exeName = "brave.exe"
		commonPaths = []string{
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
			`C:\Program Files (x86)\BraveSoftware\Brave-Browser\Application\brave.exe`,
			filepath.Join(localAppData, `BraveSoftware\Brave-Browser\Application\brave.exe`),
		}
	default:
		return p.FindBrowserPath(config.BrowserTypeChrome)
	}

	for _, keyPath := range []string{appPathsKey, clientKey} {
		if keyPath == "" {
			continue
		}
		if path, ok := existingBrowserPathFromRegistry(registry.LOCAL_MACHINE, keyPath); ok {
			return path, nil
		}
		if path, ok := existingBrowserPathFromRegistry(registry.CURRENT_USER, keyPath); ok {
			return path, nil
		}
	}

	for _, path := range commonPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s not found in registry or common installation paths", exeName)
}

package main

import (
	"path/filepath"

	"chromemanager/utils"
)

func normalizeUserDataDir(userDataDir string) string {
	if userDataDir == "" {
		return ""
	}

	return filepath.Clean(userDataDir)
}

func (c *ChromeService) isProfileStillInUse(userDataDir string) bool {
	targetDir := normalizeUserDataDir(userDataDir)
	if targetDir == "" {
		return false
	}

	processes, err := utils.FindChromeProcesses()
	if err != nil {
		return false
	}

	for _, process := range processes {
		if normalizeUserDataDir(process.UserDataDir) == targetDir {
			return true
		}

		rootDir, _ := resolveBrowserCacheLayout(process.UserDataDir)
		if normalizeUserDataDir(rootDir) == targetDir {
			return true
		}
	}

	return false
}

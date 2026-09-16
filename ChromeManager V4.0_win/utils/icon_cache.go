package utils

import "os"

func generatedIconIsCurrent(iconPath, templatePath string) bool {
	iconInfo, err := os.Stat(iconPath)
	if err != nil {
		return false
	}
	if templatePath == "" {
		return true
	}

	templateInfo, err := os.Stat(templatePath)
	if err != nil {
		return false
	}
	return !iconInfo.ModTime().Before(templateInfo.ModTime())
}

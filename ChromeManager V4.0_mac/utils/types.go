package utils

import "chromemanager/config"

type BatchOperations struct {
	settings *config.Settings
}

func NewBatchOperations(settings *config.Settings) *BatchOperations {
	return &BatchOperations{settings: settings}
}

type EnvironmentCreator struct {
	CacheDir               string
	ShortcutDir            string
	ChromePath             string
	AutoModifyShortcutIcon bool
}

func NewEnvironmentCreator(cacheDir, shortcutDir, chromePath string, autoModifyIcon bool) *EnvironmentCreator {
	return &EnvironmentCreator{
		CacheDir:               cacheDir,
		ShortcutDir:            shortcutDir,
		ChromePath:             chromePath,
		AutoModifyShortcutIcon: autoModifyIcon,
	}
}


//go:build !windows

package main

func ensureStartupDependencies() error {
	return nil
}

func showStartupError(title string, err error) {
}

//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func shellLaunchExecutable(executablePath, arguments, workingDir string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	executable, err := windows.UTF16PtrFromString(executablePath)
	if err != nil {
		return err
	}

	var argumentPtr *uint16
	if arguments != "" {
		argumentPtr, err = windows.UTF16PtrFromString(arguments)
		if err != nil {
			return err
		}
	}

	if workingDir == "" {
		workingDir = filepath.Dir(executablePath)
	}
	workingDirectory, err := windows.UTF16PtrFromString(workingDir)
	if err != nil {
		return err
	}

	return windows.ShellExecute(0, verb, executable, argumentPtr, workingDirectory, windows.SW_SHOWNORMAL)
}

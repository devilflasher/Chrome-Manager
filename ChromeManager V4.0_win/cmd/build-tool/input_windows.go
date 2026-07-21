//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

var (
	msvcrt    = windows.NewLazySystemDLL("msvcrt.dll")
	procGetch = msvcrt.NewProc("_getch")
)

func readKey() int {
	r, _, _ := procGetch.Call()
	code := int(r)

	if code == 0 || code == 0xE0 {
		r2, _, _ := procGetch.Call()
		code2 := int(r2)
		// Map Windows Scan Codes to consistent internal codes
		switch code2 {
		case 72:
			return KeyUp
		case 80:
			return KeyDown
		}
	} else if code == 13 {
		return KeyEnter
	} else if code == 113 || code == 81 {
		return KeyQ
	}

	return code
}

//go:build darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func readKey() int {
	fd := int(os.Stdin.Fd())
	// macOS uses TIOCGETA/TIOCSETA instead of TCGETS/TCSETS
	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return 0
	}

	newState := *termios
	newState.Lflag &^= unix.ICANON | unix.ECHO
	unix.IoctlSetTermios(fd, unix.TIOCSETA, &newState)

	defer unix.IoctlSetTermios(fd, unix.TIOCSETA, termios)

	b := make([]byte, 3)
	n, err := os.Stdin.Read(b)
	if err != nil {
		return 0
	}

	if n == 1 {
		if b[0] == 10 || b[0] == 13 {
			return KeyEnter
		}
		if b[0] == 113 || b[0] == 81 {
			return KeyQ
		}
		return int(b[0])
	}

	if n == 3 && b[0] == 27 && b[1] == 91 {
		switch b[2] {
		case 65:
			return KeyUp
		case 66:
			return KeyDown
		}
	}

	return 0
}

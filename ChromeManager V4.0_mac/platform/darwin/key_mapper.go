package darwin

// Map macOS Virtual Key Code to (DOM Code, Windows Virtual Key Code, Key Value)
// Key Value defaults to the character or key name (UI Events Spec)
func mapKeyCodeToDOMCode(keyCode int) (string, int, string) {
	switch keyCode {
	case 0:
		return "KeyA", 65, "a"
	case 1:
		return "KeyS", 83, "s"
	case 2:
		return "KeyD", 68, "d"
	case 3:
		return "KeyF", 70, "f"
	case 4:
		return "KeyH", 72, "h"
	case 5:
		return "KeyG", 71, "g"
	case 6:
		return "KeyZ", 90, "z"
	case 7:
		return "KeyX", 88, "x"
	case 8:
		return "KeyC", 67, "c"
	case 9:
		return "KeyV", 86, "v"
	case 11:
		return "KeyB", 66, "b"
	case 12:
		return "KeyQ", 81, "q"
	case 13:
		return "KeyW", 87, "w"
	case 14:
		return "KeyE", 69, "e"
	case 15:
		return "KeyR", 82, "r"
	case 16:
		return "KeyY", 89, "y"
	case 17:
		return "KeyT", 84, "t"
	case 18:
		return "Digit1", 49, "1"
	case 19:
		return "Digit2", 50, "2"
	case 20:
		return "Digit3", 51, "3"
	case 21:
		return "Digit4", 52, "4"
	case 22:
		return "Digit6", 54, "6"
	case 23:
		return "Digit5", 53, "5"
	case 24:
		return "Equal", 187, "="
	case 25:
		return "Digit9", 57, "9"
	case 26:
		return "Digit7", 55, "7"
	case 27:
		return "Minus", 189, "-"
	case 28:
		return "Digit8", 56, "8"
	case 29:
		return "Digit0", 48, "0"
	case 30:
		return "BracketRight", 221, "]"
	case 31:
		return "KeyO", 79, "o"
	case 32:
		return "KeyU", 85, "u"
	case 33:
		return "BracketLeft", 219, "["
	case 34:
		return "KeyI", 73, "i"
	case 35:
		return "KeyP", 80, "p"
	case 36:
		return "Enter", 13, "Enter"
	case 37:
		return "KeyL", 76, "l"
	case 38:
		return "KeyJ", 74, "j"
	case 39:
		return "Quote", 222, "'"
	case 40:
		return "KeyK", 75, "k"
	case 41:
		return "Semicolon", 186, ";"
	case 42:
		return "Backslash", 220, "\\"
	case 43:
		return "Comma", 188, ","
	case 44:
		return "Slash", 191, "/"
	case 45:
		return "KeyN", 78, "n"
	case 46:
		return "KeyM", 77, "m"
	case 47:
		return "Period", 190, "."
	case 48:
		return "Tab", 9, "Tab"
	case 49:
		return "Space", 32, " "
	case 50:
		return "Backquote", 192, "`"
	case 51:
		return "Backspace", 8, "Backspace"
	// Modifiers
	case 53:
		return "Escape", 27, "Escape"
	case 55:
		return "MetaLeft", 91, "Meta" // Command
	case 56:
		return "ShiftLeft", 16, "Shift"
	case 57:
		return "CapsLock", 20, "CapsLock"
	case 58:
		return "AltLeft", 18, "Alt"
	case 59:
		return "ControlLeft", 17, "Control"
	case 60:
		return "ShiftRight", 16, "Shift"
	case 61:
		return "AltRight", 18, "Alt"
	case 62:
		return "ControlRight", 17, "Control"
	case 123:
		return "ArrowLeft", 37, "ArrowLeft"
	case 124:
		return "ArrowRight", 39, "ArrowRight"
	case 125:
		return "ArrowDown", 40, "ArrowDown"
	case 126:
		return "ArrowUp", 38, "ArrowUp"
	// Special Keys
	case 117:
		return "Delete", 46, "Delete"
	case 115:
		return "Home", 36, "Home"
	case 119:
		return "End", 35, "End"
	case 116:
		return "PageUp", 33, "PageUp"
	case 121:
		return "PageDown", 34, "PageDown"
	// Function Keys
	case 122:
		return "F1", 112, "F1"
	case 120:
		return "F2", 113, "F2"
	case 99:
		return "F3", 114, "F3"
	case 118:
		return "F4", 115, "F4"
	case 96:
		return "F5", 116, "F5"
	case 97:
		return "F6", 117, "F6"
	case 98:
		return "F7", 118, "F7"
	case 100:
		return "F8", 119, "F8"
	case 101:
		return "F9", 120, "F9"
	case 109:
		return "F10", 121, "F10"
	case 103:
		return "F11", 122, "F11"
	case 111:
		return "F12", 123, "F12"
	default:
		return "", 0, ""
	}
}

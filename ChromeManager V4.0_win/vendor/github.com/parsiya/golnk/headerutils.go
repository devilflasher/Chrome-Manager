package lnk

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ShellLinkHeader utilities.

/*
	ShowCommand valid values:
	0x00000001 - SW_SHOWNORMAL - The application is open and its window is open in a normal fashion.
	0x00000003 - SW_SHOWMAXIMIZED - The application is open, and keyboard focus is given to the application, but its window is not shown.
	0x00000007 - SW_SHOWMINNOACTIVE - The application is open, but its window is not shown. It is not given the keyboard focus.
	All other values are SW_SHOWNORMAL.
*/
// showCommand returns the string associated with ShowCommand uint32 field.
func showCommand(s uint32) string {
	switch s {
	case 0x03:
		return "SW_SHOWMAXIMIZED"
	case 0x07:
		return "SW_SHOWMINNOACTIVE"
	}
	// Anything other than these two (include 0x01) is "SW_SHOWNORMAL"
	return "SW_SHOWNORMAL"
}

/*
	HotKeyFlags contains the hotkey.
	Although it's 4 bytes, only the first 2 bytes are used.

	First byte is LowByte.
	If between 0x30 and 0x5A inclusive, it's ASCII-hex of the key.
	If between 0x70 and 0x87 it's F(num-0x70+1) (e.g. 0x70 == F1 and 0x87 == F24).
	0x90 == NUM LOCK and 0x91 SCROLL LOCK.

	Second byte is HighByte.
	0x01: SHIFT
	0X02: CTRL
	0X03: ALT
*/
// HotKey returns the string representation of the hotkey uint32.
func HotKey(hotkey uint16) string {
	var sb strings.Builder
	lb := byteMaskuint16(hotkey, 0) // first byte
	hb := byteMaskuint16(hotkey, 1) // second byte

	switch hb {
	case 0x01:
		sb.WriteString("SHIFT")
	case 0x02:
		sb.WriteString("CTRL")
	case 0x04:
		sb.WriteString("ALT")
	default:
		// 0x00 is technically "no key assigned", but any value other than these
		// is the same.
		return "No Key Assigned"
	}

	sb.WriteString("+")

	switch {
	case 0x30 <= lb && lb <= 0x5A:
		sb.WriteString(string(lb))
	case 0x70 <= lb && lb <= 0x87:
		sb.WriteString("F" + strconv.Itoa(int(lb-0x70+1)))
	case lb == 0x90:
		sb.WriteString("NUM LOCK")
	case lb == 0x91:
		sb.WriteString("SCROLL LOCK")
	default:
		// 0x00 is technically "no key assigned", but any value other than these
		// is the same.
		return "No Key Assigned"
	}
	return sb.String()
}

// toTime converts an 8-byte Windows Filetime to time.Time.
func toTime(t [8]byte) time.Time {
	// Taken from https://golang.org/src/syscall/types_windows.go#L352, which is only available on Windows
	nsec := int64(binary.LittleEndian.Uint32(t[4:]))<<32 + int64(binary.LittleEndian.Uint32(t[:4]))
	// change starting time to the Epoch (00:00:00 UTC, January 1, 1970)
	nsec -= 116444736000000000
	// convert into nanoseconds
	nsec *= 100
	return time.Unix(0, nsec)
}

// formatTime converts a 8-byte Windows Filetime to time.Time and then formats
// it to string.
func formatTime(t [8]byte) string {
	return toTime(t).Format("2006-01-02 15:04:05.999999 -07:00")
}

// Flag utilities.

/*
	matchFlag does the following:
	Given a uint32 flag read in littleEndian from disk and a []string,
	match the flag bits and return a map[string]bool (FlagMap) that has the
	// matched flags as keys.
	Flag bits must be reversed because bits are matched to the flags from 0
	onwards but the bit string is the other way around.
*/
func matchFlag(flag uint32, flagText []string) FlagMap {
	// Convert to bits and then reverse.
	flagBits := reverse(fmt.Sprintf("%b", flag))
	mp := make(FlagMap, 0)
	// If we have more bits than flags (something has gone wrong or the file is corrupted),
	// then reduce the flagbits.

	if len(flagBits) > len(flagText) {
		flagBits = flagBits[:len(flagText)]
	}
	for bitIndex := 0; bitIndex < len(flagBits); bitIndex++ {
		if flagBits[bitIndex] == 0x31 {
			mp[flagText[bitIndex]] = true
		}
	}
	return mp
}

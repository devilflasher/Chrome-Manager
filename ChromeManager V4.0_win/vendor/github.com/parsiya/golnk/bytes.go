package lnk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf8"
)

// byteMaskuint16 returns one of the two bytes from a uint16.
func byteMaskuint16(b uint16, n int) uint16 {
	// Maybe we should not panic, hmm.
	if n < 0 || n > 2 {
		panic(fmt.Sprintf("invalid byte mask, got %d", n))
	}
	mask := uint16(0x000000FF) << uint16(n*8)
	return (b & mask) >> uint16(n*8)
}

// bitMaskuint32 returns one of the 32-bits from a uint32.
// Returns true for 1 and false for 0.
func bitMaskuint32(b uint32, n int) bool {
	if n < 0 || n > 31 {
		panic(fmt.Sprintf("invalid bit number, got %d", n))
	}
	return ((b >> uint(n)) & 1) == 1
}

// ReadBytes reads n bytes from the slice starting from offset and
// returns a []byte and the number of bytes read. If offset is out of bounds
// it returns an empty []byte and 0 bytes read.
// TODO: Write tests for this.
func ReadBytes(b []byte, offset, num int) (out []byte, n int) {
	if offset >= len(b) {
		return out, 0
	}
	if offset+num >= len(b) {
		return b[offset:], len(b[offset:])
	}
	return b[offset : offset+num], num
}

/*
	readSection reads a size from the start of the io.Reader. The size length is
	decided by the parameter sSize.
	sSize == 2 - read uint16
	sSize == 4 - read uint32
	sSize == 8 - read uint64 - Not needed for now.
	Then read (size-sSize) bytes, populate the start with the original bytes
	and add the rest. Finally return the []byte and a new io.Reader to it.
	The size bytes are added to the start of the []byte to keep the section
	[]byte intact for later offset use.
*/
func readSection(r io.Reader, sSize int, maxSize uint64) (data []byte, nr io.Reader, size int, err error) {
	// We are not going to lose data by copying a smaller var into a larger one.
	var sectionSize uint64
	switch sSize {
	case 2:
		// Read uint16.
		var size16 uint16
		err = binary.Read(r, binary.LittleEndian, &size16)
		if err != nil {
			return data, nr, size, fmt.Errorf("golnk.readSection: read size %d bytes - %s", sSize, err.Error())
		}
		sectionSize = uint64(size16)
		// Add bytes to the start of data []byte.
		data = uint16Byte(size16)
	case 4:
		// Read uint32.
		var size32 uint32
		err = binary.Read(r, binary.LittleEndian, &size32)
		if err != nil {
			return data, nr, size, fmt.Errorf("golnk.readSection: read size %d bytes - %s", sSize, err.Error())
		}
		sectionSize = uint64(size32)
		// Add bytes to the start of data []byte.
		data = uint32Byte(size32)
	case 8:
		// Read uint64 or sectionSize.
		err = binary.Read(r, binary.LittleEndian, &sectionSize)
		if err != nil {
			return data, nr, size, fmt.Errorf("golnk.readSection: read size %d bytes - %s", sSize, err.Error())
		}
		// Add bytes to the start of data []byte.
		data = uint64Byte(sectionSize)
	default:
		return data, nr, size, fmt.Errorf("golnk.readSection: invalid sSize - got %v", sSize)
	}

	// Create a []byte of sectionSize-4 and read that many bytes from io.Reader.
	computedSize := sectionSize - uint64(sSize)
	if computedSize > maxSize {
		return data, nr, size, fmt.Errorf("golnk.readSection: invalid computed size got %d; expected a size < %d", computedSize, maxSize)
	}

	tempData := make([]byte, computedSize)
	err = binary.Read(r, binary.LittleEndian, &tempData)
	if err != nil {
		return data, nr, size, fmt.Errorf("golnk.readSection: read section %d bytes - %s", sectionSize-uint64(sSize), err.Error())
	}

	// If this is successful, append it to data []byte.
	data = append(data, tempData...)

	// Create a reader from the unread bytes.
	nr = bytes.NewReader(tempData)

	return data, nr, int(sectionSize), nil
}

// readString returns a string of all bytes from the []byte until the first 0x00.
func readString(data []byte) string {
	// Find the index of first 0x00.
	i := bytes.IndexByte(data, byte(0x00))
	if i == -1 {
		// If 0x00 is not found, return all the slice.
		i = len(data)
	}
	return string(data[:i])
}

// readUnicodeString returns a string of all bytes from the []byte until the
// first 0x0000.
func readUnicodeString(data []byte) string {

	// Read two bytes at a time and convert to rune, stop if both are 0x0000 or
	// we have reached the end of the input.
	var runes []rune
	for bitIndex := 0; bitIndex < len(data)/2; bitIndex++ {
		if data[bitIndex*2] == 0x00 && data[(bitIndex*2)+1] == 0x00 {
			return string(runes)
		}
		r, _ := utf8.DecodeRune(data[bitIndex*2:])
		runes = append(runes, r)
	}
	return string(runes)
}

// readStringData reads a uint16 as size and then reads that many bytes
// (*2 for unicode) into a string. The string is not null-terminated.
// TODO: Write tests.
func readStringData(r io.Reader, isUnicode bool) (str string, err error) {
	// Recover in case we attempt to read more bytes than there is in the reader.
	defer func() {
		if r := recover(); r != nil {
			// If panic occurs, return this error message
			err = fmt.Errorf("golnk.readStringData: not enough bytes in reader")
		}
	}()

	var size uint16
	err = binary.Read(r, binary.LittleEndian, &size)
	if err != nil {
		return str, fmt.Errorf("golnk.readStringData: read size - %s", err.Error())
	}
	if isUnicode {
		size = size * 2
	}
	b := make([]byte, size)
	err = binary.Read(r, binary.LittleEndian, &b)
	if err != nil {
		return str, fmt.Errorf("golnk.readStringData: read bytes - %s", err.Error())
	}
	// If unicode, read every 2 byte and get a rune.
	if isUnicode {
		var runes []rune
		for bitIndex := 0; bitIndex < int(size)/2; bitIndex++ {
			r, _ := utf8.DecodeRune(b[bitIndex*2:])
			runes = append(runes, r)
		}
		return string(runes), nil
	}
	return string(b), nil
}

// uint16Little reads a uint16 from []byte and returns the result in Little-Endian.
func uint16Little(b []byte) uint16 {
	if len(b) < 2 {
		panic(fmt.Sprintf("input smaller than two bytes - got %d", len(b)))
	}
	return binary.LittleEndian.Uint16(b)
}

// uint32Little reads a uint32 from []byte and returns the result in Little-Endian.
func uint32Little(b []byte) uint32 {
	if len(b) < 4 {
		panic(fmt.Sprintf("input smaller than four bytes - got %d", len(b)))
	}
	return binary.LittleEndian.Uint32(b)
}

// uint64Little reads a uint64 from []byte and returns the result in Little-Endian.
func uint64Little(b []byte) uint64 {
	if len(b) < 8 {
		panic(fmt.Sprintf("input smaller than eight bytes - got %d", len(b)))
	}
	return binary.LittleEndian.Uint64(b)
}

// uint16Str converts a uint16 to string using fmt.Sprint.
func uint16Str(u uint16) string {
	return fmt.Sprint(u)
}

// int16Str converts an int16 to string using fmt.Sprint.
func int16Str(u int16) string {
	return fmt.Sprint(u)
}

// uint32Str converts a uint32 to string using fmt.Sprint.
func uint32Str(u uint32) string {
	return fmt.Sprint(u)
}

// uint32StrHex converts a uint32 to a hex encoded string using fmt.Sprintf.
func uint32StrHex(u uint32) string {
	str := fmt.Sprintf("%x", u)
	// Add a 0 to the start of odd-length string. This converts "0x1AB" to "0x01AB"
	if (len(str) % 2) != 0 {
		str = "0" + str
	}
	return "0x" + str
}

// uint32TableStr creates a string that has both decimal and hex values
// of uint32.
func uint32TableStr(u uint32) string {
	return fmt.Sprintf("%s - %s", uint32Str(u), uint32StrHex(u))
}

// int32Str converts an int32 to string using fmt.Sprint.
func int32Str(u int32) string {
	return fmt.Sprint(u)
}

// uint16Byte converts a uint16 to a []byte.
func uint16Byte(u uint16) []byte {
	var buf bytes.Buffer
	err := binary.Write(&buf, binary.LittleEndian, u)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// uint32Byte converts a uint32 to a []byte.
func uint32Byte(u uint32) []byte {
	var buf bytes.Buffer
	err := binary.Write(&buf, binary.LittleEndian, u)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// uint64Byte converts a uint64 to a []byte.
func uint64Byte(u uint64) []byte {
	var buf bytes.Buffer
	err := binary.Write(&buf, binary.LittleEndian, u)
	if err != nil {
		panic(err)
	}
	return buf.Bytes()
}

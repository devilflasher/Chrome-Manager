package lnk

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// VolID is VolumeID (section 2.3.1. of [SHLLINK]).
// Information about the volume that the link target was on when the link was created.
type VolID struct {
	// Size of VolumeID including this.
	Size uint32

	// Type of drive that target was stored on.
	DriveType string // Originally a uint32 that is the index to the driveType []string.

	// Serial number of the volume. TODO: Store as hex string?
	DriveSerialNumber string // Originally a uint32, converted to hex string.

	// Offset to the a null-terminated string that contains the volume label of
	// the drive that the link target is stored on.
	// If == 0x14, it must be ignored and VolumeLabelOffsetUnicode must be used.
	VolumeLabelOffset uint32

	// Offset to Unicode version of VolumeLabel.
	// Must not be present if VolumeLabelOffset is not 0x14.
	VolumeLabelOffsetUnicode uint32

	// VolumeLabel in either ASCII-HEX or Unicode.
	VolumeLabel string
}

// Different DriveTypes. The value of field is the index to this slice.
var driveType = []string{
	"DRIVE_UNKNOWN",
	"DRIVE_NO_ROOT_DIR",
	"DRIVE_REMOVABLE",
	"DRIVE_FIXED",
	"DRIVE_REMOTE",
	"DRIVE_CDROM",
	"DRIVE_RAMDISK",
}

// VolumeID reads the VolID struct.
func VolumeID(r io.Reader, maxSize uint64) (v VolID, err error) {
	// Read the section.
	sectionData, sectionReader, sectionSize, err := readSection(r, 4, maxSize)
	if err != nil {
		return v, fmt.Errorf("golnk.VolumeID: read VolumeID section - %s", err.Error())
	}
	_ = sectionSize
	// fmt.Printf("Read section volumeID. %d bytes.\n", sectionSize)
	// fmt.Println(hex.Dump(sectionData))

	// Read DriveType.
	var dt uint32
	err = binary.Read(sectionReader, binary.LittleEndian, &dt)
	if err != nil {
		return v, fmt.Errorf("golnk.VolumeID: read VolumeID.DriveType - %s", err.Error())
	}
	// Check if it's a valid DriveType.
	if dt >= uint32(len(driveType)) {
		// This is not in the specification but it's better than just returning
		// an error and cancelling the parse.
		v.DriveType = "DRIVE_INVALID"
	} else {
		v.DriveType = driveType[dt]
	}
	// fmt.Println("VolumeID.DriveType:", v.DriveType)

	// Read DriveSerialNumber which is a uint32.
	var sr [4]byte
	err = binary.Read(sectionReader, binary.LittleEndian, &sr)
	if err != nil {
		return v, fmt.Errorf("golnk.VolumeID: read VolumeID.DriveSerialNumber - %s", err.Error())
	}
	v.DriveSerialNumber = "0x" + hex.EncodeToString(sr[:])

	// fmt.Println("VolumeID.DriveSerialNumber:", v.DriveSerialNumber)

	// Read VolumeLabelOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &v.VolumeLabelOffset)
	if err != nil {
		return v, fmt.Errorf("golnk.VolumeID: read VolumeID.VolumeLabelOffset - %s", err.Error())
	}
	// fmt.Println("VolumeID.VolumeLabelOffset:", v.VolumeLabelOffset)

	// Check if it's 0x14, if it's not, then use this offset and read a
	// null-terminated string.
	// If it is 0x14, ignore this and read the next uint32 for VolumeLabelOffsetUnicode.
	if v.VolumeLabelOffset != 0x14 {
		// Read a null-terminated string from sectionData[v.VolumeLabelOffset:].
		str := readString(sectionData[v.VolumeLabelOffset:])
		v.VolumeLabel = str
		// fmt.Println("VolumeLabel", str)

		// Because we read VolumeLabel manually, VolumeLabelOffsetUnicode must
		// not exist and we can return.
		return v, nil
	}

	// TODO: Test this.
	// If v.VolumeLabelOffset is 0x14, it means we need to read a uint32
	// to get VolumeLabelOffsetUnicode and read a unicode string there.
	err = binary.Read(sectionReader, binary.LittleEndian, &v.VolumeLabelOffsetUnicode)
	// fmt.Println("v.VolumeLabelOffsetUnicode", v.VolumeLabelOffsetUnicode)

	// Read a unicode string from that offset.
	unicodeStr := readUnicodeString(sectionData[v.VolumeLabelOffsetUnicode:])
	v.VolumeLabel = unicodeStr
	// fmt.Println("VolumeLabelUnicode", v.VolumeLabel)

	return v, err
}

// String prints VolumeID in a table.
func (v VolID) String() string {

	var sb strings.Builder

	table := tablewriter.NewWriter(&sb)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)

	table.SetHeader([]string{"VolumeID", "Value"})

	table.Append([]string{"Size", uint32TableStr(v.Size)})
	table.Append([]string{"DriveType", v.DriveType})
	table.Append([]string{"DriveSerialNumber", v.DriveSerialNumber})

	if v.VolumeLabelOffset != 0 {
		table.Append([]string{"VolumeLabelOffset", uint32TableStr(v.VolumeLabelOffset)})
		table.Append([]string{"VolumeLabel", v.VolumeLabel})
	}

	if v.VolumeLabelOffsetUnicode != 0 {
		table.Append([]string{"VolumeLabelOffsetUnicode", uint32TableStr(v.VolumeLabelOffsetUnicode)})
		table.Append([]string{"VolumeLabel", v.VolumeLabel})
	}

	table.Render()
	return sb.String()
}

package lnk

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
)

const (
	headerSize = 0x4C
	// This is how it appears in the file when we read an array.
	// Endian-ness does not make a difference.
	classID = "0114020000000000c000000000000046"
)

type FlagMap map[string]bool

// From the docs:
// "Multi-byte data values in the Shell Link Binary File Format are stored in little-endian format."

// ShellLinkHeaderSection represents the lnk header.
type ShellLinkHeaderSection struct {
	Magic          uint32    // Header size: should be 0x4c.
	LinkCLSID      [16]byte  // A class identifier, should be 00021401-0000-0000-C000-000000000046.
	LinkFlags      FlagMap   // Information about the file and optional sections in the file.
	FileAttributes FlagMap   // File attributes about link target, originally a uint32.
	CreationTime   time.Time // Creation time of link target in UTC. 16 bytes in file.
	AccessTime     time.Time // Access time of link target. Could be zero. 16 bytes in file.
	WriteTime      time.Time // Write time  of link target. Could be zero. 16 bytes in file.
	TargetFileSize uint32    // Filesize of link target. If larger than capacity, it will have the LSB 32-bits of size.
	IconIndex      int32     // 32-bit signed integer, the index of an icon within a given icon location.
	ShowCommand    string    // Result of the uint32 integer: The expected windows state of the target after execution.
	HotKey         string    // HotKeyFlags structure to launch the target. Original is uint16.
	Reserved1      uint16    // Zero.
	Reserved2      uint32    // Zero.
	Reserved3      uint32    // Zero.
	Raw            []byte    // Section's raw bytes.
}

// linkFlags defines what shell link structures are in the file.
var linkFlags = []string{
	"HasLinkTargetIDList",         // bit00 - ShellLinkHeader is followed by a LinkTargetIDList structure.
	"HasLinkInfo",                 // bit01 - LinkInfo in file.
	"HasName",                     // bit02 - NAME_String in file.
	"HasRelativePath",             // bit03 - RELATIVE_PATH in file.
	"HasWorkingDir",               // bit04 - WORKING_DIR in file.
	"HasArguments",                // bit05 - COMMAND_LINE_ARGUMENTS
	"HasIconLocation",             // bit06 - ICON_LOCATION
	"IsUnicode",                   // bit07 - Strings are in unicode
	"ForceNoLinkInfo",             // bit08 - LinkInfo is ignored
	"HasExpString",                // bit09 - The shell link is saved with an EnvironmentVariableDataBlock
	"RunInSeparateProcess",        // bit10 - Target runs in a 16-bit virtual machine
	"Unused1",                     // bit11 - ignore
	"HasDarwinID",                 // bit12 - The shell link is saved with a DarwinDataBlock
	"RunAsUser",                   // bit13 - The application is run as a different user when the target of the shell link is activated.
	"HasExpIcon",                  // bit14 - The shell link is saved with an IconEnvironmentDataBlock
	"NoPidlAlias",                 // bit15 - The file system location is represented in the shell namespace when the path to an item is parsed into an IDList.
	"Unused2",                     // bit16 - ignore
	"RunWithShimLayer",            // bit17 - The shell link is saved with a ShimDataBlock.
	"ForceNoLinkTrack",            // bit18 - The TrackerDataBlock is ignored.
	"EnableTargetMetadata",        // bit19 - The shell link attempts to collect target properties and store them in the PropertyStoreDataBlock (section 2.5.7) when the link target is set.
	"DisableLinkPathTracking",     // bit20 - The EnvironmentVariableDataBlock is ignored.
	"DisableKnownFolderTracking",  // bit21 - The SpecialFolderDataBlock (section 2.5.9) and the KnownFolderDataBlock (section 2.5.6) are ignored when loading the shell link. If this bit is set, these extra data blocks SHOULD NOT be saved when saving the shell link.
	"DisableKnownFolderAlias",     // bit22 - If the link has a KnownFolderDataBlock (section 2.5.6), the unaliased form of the known folder IDList SHOULD be used when translating the target IDList at the time that the link is loaded.
	"AllowLinkToLink",             // bit23 - Creating a link that references another link is enabled. Otherwise, specifying a link as the target IDList SHOULD NOT be allowed.
	"UnaliasOnSave",               // bit24 - When saving a link for which the target IDList is under a known folder, either the unaliased form of that known folder or the target IDList SHOULD be used.
	"PreferEnvironmentPath",       // bit25 - The target IDList SHOULD NOT be stored; instead, the path specified in the EnvironmentVariableDataBlock (section 2.5.4) SHOULD be used to refer to the target.
	"KeepLocalIDListForUNCTarget", // bit26 - When the target is a UNC name that refers to a location on a local machine, the local path IDList in the PropertyStoreDataBlock (section 2.5.7) SHOULD be stored, so it can be used when the link is loaded on the local machine.
}

// fileAttributesFlags represent target file attributes.
var fileAttributesFlags = []string{
	"FILE_ATTRIBUTE_READONLY",
	"FILE_ATTRIBUTE_HIDDEN",
	"FILE_ATTRIBUTE_SYSTEM",
	"Reserved1", // Must be zero.
	"FILE_ATTRIBUTE_DIRECTORY",
	"FILE_ATTRIBUTE_ARCHIVE",
	"Reserved2", // Must be zero.
	"FILE_ATTRIBUTE_NORMAL",
	"FILE_ATTRIBUTE_TEMPORARY",
	"FILE_ATTRIBUTE_SPARSE_FILE",
	"FILE_ATTRIBUTE_REPARSE_POINT",
	"FILE_ATTRIBUTE_COMPRESSED",
	"FILE_ATTRIBUTE_OFFLINE",
	"FILE_ATTRIBUTE_NOT_CONTENT_INDEXED",
	"FILE_ATTRIBUTE_ENCRYPTED",
}

// Header parses the first 0x4c bytes of the io.Reader and returns a ShellLinkHeader.
func Header(r io.Reader, maxSize uint64) (head ShellLinkHeaderSection, err error) {

	// Read the section.
	sectionData, sectionReader, sectionSize, err := readSection(r, 4, maxSize)
	if err != nil {
		return head, fmt.Errorf("lnk.header: error reading magic string - %s", err.Error())
	}

	// Check sectionSize (the first four bytes) against 0x4c.
	if sectionSize != headerSize {
		return head, fmt.Errorf("lnk.header: invalid magic string - got %x, want %s", sectionSize, "0x4C")
	}
	head.Magic = uint32(sectionSize)

	// Store raw bytes.
	head.Raw = sectionData

	// Next 16 bytes should be 00021401-0000-0000-C000-000000000046.
	// When reading, we will see 0114020000000000c000000000000046.
	var clsID [16]byte
	err = binary.Read(sectionReader, binary.LittleEndian, &clsID)
	if err != nil {
		return head, fmt.Errorf("lnk.header: error reading LinkCLSID - %s", err.Error())
	}
	hexClsID := hex.EncodeToString(clsID[:])
	if hexClsID != classID {
		return head,
			fmt.Errorf("lnk.header: invalid class ID - got %s, want %s", hexClsID, classID)
	}
	head.LinkCLSID = clsID

	// Parse LinkFlags.
	// Convert the next uint32 to bits, go over the bits and add the flags.
	// We will lose preceding zeros by using uint32 but we do not care about them.
	var lf uint32
	err = binary.Read(sectionReader, binary.LittleEndian, &lf)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading LinkFlags - %s", err.Error())
	}
	head.LinkFlags = matchFlag(lf, linkFlags)

	// Parse FileAttributes.
	var attribs uint32
	// Same as before, read BigEndian.
	err = binary.Read(sectionReader, binary.LittleEndian, &attribs)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading FileAttributes - %s", err.Error())
	}
	head.FileAttributes = matchFlag(attribs, fileAttributesFlags)

	// Convert timestamps from Windows Filetime to time.Time.
	var crTime, wrTime, acTime [8]byte
	err = binary.Read(sectionReader, binary.LittleEndian, &crTime)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading CreationTime - %s", err.Error())
	}
	head.CreationTime = toTime(crTime)

	err = binary.Read(sectionReader, binary.LittleEndian, &wrTime)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading WriteTime - %s", err.Error())
	}
	head.WriteTime = toTime(wrTime)

	err = binary.Read(sectionReader, binary.LittleEndian, &acTime)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading AccessTime - %s", err.Error())
	}
	head.AccessTime = toTime(acTime)

	// Target file size.
	err = binary.Read(sectionReader, binary.LittleEndian, &head.TargetFileSize)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading target file size - %s", err.Error())
	}

	// Icon index is a signed 32-bit integer.
	err = binary.Read(sectionReader, binary.LittleEndian, &head.IconIndex)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading icon index - %s", err.Error())
	}

	// ShowCommand
	var sw uint32
	err = binary.Read(sectionReader, binary.LittleEndian, &sw)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading showcommand - %s", err.Error())
	}
	head.ShowCommand = showCommand(sw)

	// Hotkey.
	var hk uint16
	err = binary.Read(sectionReader, binary.LittleEndian, &hk)
	if err != nil {
		return head, fmt.Errorf("lnk.header: reading hotkey - %s", err.Error())
	}
	head.HotKey = HotKey(hk)

	// The rest should be 10 0x00 bytes.
	binary.Read(sectionReader, binary.LittleEndian, &head.Reserved1)
	binary.Read(sectionReader, binary.LittleEndian, &head.Reserved2)
	binary.Read(sectionReader, binary.LittleEndian, &head.Reserved3)

	return head, err
}

// String prints the ShellLinkHeader in a table.
func (h ShellLinkHeaderSection) String() string {
	var sb, flags, attribs strings.Builder

	// Append all flags.
	for fl := range h.LinkFlags {
		flags.WriteString(fl)
		flags.WriteString("\n")
	}

	// Append all file attributes.
	for at := range h.FileAttributes {
		attribs.WriteString(at)
		attribs.WriteString("\n")
	}

	table := tablewriter.NewWriter(&sb)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)

	table.SetHeader([]string{"ShellLinkHeader", "Value"})

	table.Append([]string{"Magic", uint32TableStr(h.Magic)})
	table.Append([]string{"LinkCLSID", hex.EncodeToString(h.LinkCLSID[:])})
	table.Append([]string{"LinkFlags", flags.String()})
	table.Append([]string{"FileAttributes", attribs.String()})
	table.Append([]string{"CreationTime", h.CreationTime.String()})
	table.Append([]string{"AccessTime", h.AccessTime.String()})
	table.Append([]string{"WriteTime", h.WriteTime.String()})
	table.Append([]string{"TargetFileSize", uint32TableStr(h.TargetFileSize)})
	table.Append([]string{"IconIndex", uint32TableStr(uint32(h.IconIndex))})
	table.Append([]string{"HotKey", h.HotKey})
	table.Render()

	return sb.String()
}

// Dump returns the hex.Dump of section data.
func (h ShellLinkHeaderSection) Dump() string {
	return hex.Dump(h.Raw)
}

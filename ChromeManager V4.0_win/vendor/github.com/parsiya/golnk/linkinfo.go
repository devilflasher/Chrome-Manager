package lnk

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// LinkInfoSection represents the LinkInfo structure. Section 2.3 of [MS-SHLLINK].
// It appears right after LinkTargetIDList if it's in the linkFlags.
type LinkInfoSection struct {
	// LinkInfo header section start.

	// Size of the LinkInfo structure. Includes these four bytes.
	Size uint32

	// Size of LinkInfo header section.
	// If == 0x1c => Offsets to optional fields are not specified.
	// If >= 0x24 => Offsets to optional fields are specified.
	// Header section include LinkInfoSize and some of the following fields.
	LinkInfoHeaderSize uint32

	// Offsets are from the start of LinkInfo structure == start of the io.Reader.
	// If an offset is zero, then field does not exist.

	// Flags that specify whether the VolumeID, LocalBasePath, LocalBasePathUnicode,
	// and CommonNetworkRelativeLink fields are present in this structure.
	// See linkInfoFlags
	LinkInfoFlags uint32

	// LinkInfoFlagsStr contains the flags in string format.
	LinkInfoFlagsStr []string

	// Offset of VolumeID if VolumeIDAndLocalBasePath is set.
	VolumeIDOffset uint32

	// Offset of LocalBasePath if VolumeIDAndLocalBasePath is set.
	LocalBasePathOffset uint32

	// Offset of CommonNetworkRelativeLink if CommonNetworkRelativeLinkAndPathSuffix is set.
	CommonNetworkRelativeLinkOffset uint32

	// Offset of CommonPathSuffix.
	CommonPathSuffixOffset uint32

	// Offset of optional LocalBasePathUnicode and present if VolumeIDAndLocalBasePath is set
	// and LinkInfoHeaderSize >= 0x24.
	LocalBasePathOffsetUnicode uint32 // Optional

	// Offset of CommonPathSuffixUnicode and present if LinkInfoHeaderSize >= 0x24.
	CommonPathSuffixOffsetUnicode uint32 // Optional

	// LinkInfo header section end (I think?).

	// VolumeID present if VolumeIDAndLocalBasePath is set.
	VolID VolID

	// Null-terminated string present if VolumeIDAndLocalBasePath is set.
	// Combine with CommonPathSuffix to get the full path to target.
	LocalBasePath string // Optional

	// Optional CommonNetworkRelativeLink, contains information about network
	// location of the target.
	NetworkRelativeLink CommonNetworkRelativeLink

	// Null-terminated string. Combine with LocalBasePath to get full path to target.
	CommonPathSuffix string // Optional

	// Null-terminated Unicode string to base path.
	// Present only VolumeIDAndLocalBasePath is set and LinkInfoHeaderSize >= 0x24.
	LocalBasePathUnicode string // Optional

	// Null-terminated Unicode string to common path.
	// Present only VolumeIDAndLocalBasePath is set and LinkInfoHeaderSize >= 0x24.
	CommonPathSuffixUnicode string // Optional

	// Section's raw bytes.
	Raw []byte
}

// linkInfoFlags defines the LinkInfoFlags. Only the first two bits are used for now.
var linkInfoFlags = []string{
	// If 1, VolumeIDOffset and LocalBasePathOffset point to respective fields.
	// If LinkInfoHeaderSize >= 0x24 and LocalBasePathOffsetUnicode is populated.
	"VolumeIDAndLocalBasePath", // Bit 0

	// If 1, CommonNetworkRelativeLinkOffset field is populated.
	// If 0, offset is zero.
	"CommonNetworkRelativeLinkAndPathSuffix", // Bit 1
}

// LinkInfo reads the io.Reader and returns a populated LinkInfoSection.
func LinkInfo(r io.Reader, maxSize uint64) (info LinkInfoSection, err error) {

	// Parse section.
	sectionData, sectionReader, sectionSize, err := readSection(r, 4, maxSize)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: section - %s", err.Error())
	}
	info.Size = uint32(sectionSize)

	// Save raw bytes.
	info.Raw = sectionData

	// fmt.Println("info.Size", info.Size)

	// Read LinkInfoHeaderSize.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.LinkInfoHeaderSize)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read LinkInfoHeaderSize - %s", err.Error())
	}

	// // If 0x1C no optional fields.
	// // If >= 0x24 offset to optional fields are here.
	// optionalHeaderFields := false
	// if info.LinkInfoHeaderSize == 0x1c {
	// 	optionalHeaderFields = false
	// }
	// if info.LinkInfoHeaderSize >= 0x24 {
	// 	optionalHeaderFields = true
	// }

	// fmt.Printf("LinkInfoHeaderSize is %x, setting optionalHeaderFields to %v.\n", info.LinkInfoHeaderSize, optionalHeaderFields)

	// Read LinkInfoFlags.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.LinkInfoFlags)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read LinkInfoFlags - %s", err.Error())
	}

	// fmt.Println("LinkInfoFlags", info.LinkInfoFlags)
	// Set flags.
	for bitIndex := 0; bitIndex < 2; bitIndex++ {
		if bitMaskuint32(info.LinkInfoFlags, bitIndex) {
			info.LinkInfoFlagsStr = append(info.LinkInfoFlagsStr, linkInfoFlags[bitIndex])
		}
	}

	// fmt.Println("LinkInfoFlagsStr", info.LinkInfoFlagsStr)

	// Read VolumeIDOffset, LocalBasePathOffset, CommonNetworkRelativeLinkOffset
	// and CommonPathSuffixOffset because they are not optional. Then we will
	// act based on LinkInfoFlags.

	// Read VolumeIDOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.VolumeIDOffset)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read VolumeIDOffset - %s", err.Error())
	}
	// fmt.Printf("VolumeIDOffset : %v\n", info.VolumeIDOffset)

	// Read LocalBasePathOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.LocalBasePathOffset)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read LocalBasePathOffset - %s", err.Error())
	}
	// fmt.Println("LocalBasePathOffset:", info.LocalBasePathOffset)

	// Read CommonNetworkRelativeLinkOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.CommonNetworkRelativeLinkOffset)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read CommonNetworkRelativeLinkOffset - %s", err.Error())
	}
	// fmt.Println("CommonNetworkRelativeLinkOffset:", info.CommonNetworkRelativeLinkOffset)

	// Read CommonPathSuffixOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &info.CommonPathSuffixOffset)
	if err != nil {
		return info, fmt.Errorf("lnk.LinkInfo: read CommonPathSuffixOffset - %s", err.Error())
	}
	// fmt.Println("CommonPathSuffixOffset:", info.CommonPathSuffixOffset)

	// Read CommonPathSuffix if offset is not zero.
	if info.CommonPathSuffixOffset != 0x00 {
		info.CommonPathSuffix = readString(sectionData[info.CommonPathSuffixOffset:])
	}

	// If VolumeIDAndLocalBasePath is set then VolumeIDOffset and LocalBasePathOffset
	// are both set.
	if bitMaskuint32(info.LinkInfoFlags, 0) {
		// Populate VolumeID based on offset from linkInfo.
		if info.VolumeIDOffset > info.Size {
			return info,
				fmt.Errorf("lnk.LinkInfo: VolumeIDOffset %d larger than LinkInfo size %d",
					info.VolumeIDOffset, info.Size)
		}

		// Read VolumeID struct from offset.
		// Make an io.Reader for bytes starting from that offset.
		vbuf := bytes.NewReader(sectionData[info.VolumeIDOffset:])
		vol, err := VolumeID(vbuf, maxSize)
		if err != nil {
			return info, fmt.Errorf("lnk.LinkInfo: parse VolumeID - %s", err.Error())
		}
		info.VolID = vol
		// fmt.Println(StructToJSON(info.VolID, true))

		// Read LocalBasePath which is a null-terminated string.
		info.LocalBasePath = readString(sectionData[info.LocalBasePathOffset:])
		// fmt.Println("LocalBasePath", info.LocalBasePath)

		// LocalBasePathOffsetUnicode and CommonPathSuffixOffsetUnicode only
		// exist if LinkInfoHeaderSize >= 0x24 and are not zero if
		// VolumeIDAndLocalBasePath is set.
		// TODO: Find lnk files that test this.
		if info.LinkInfoHeaderSize >= 0x24 {
			// Read LocalBasePathOffsetUnicode.
			err = binary.Read(sectionReader, binary.LittleEndian, &info.LocalBasePathOffsetUnicode)
			if err != nil {
				return info, fmt.Errorf("lnk.LinkInfo: read LocalBasePathOffsetUnicode - %s", err.Error())
			}
			// fmt.Println("LocalBasePathOffsetUnicode:", info.LocalBasePathOffsetUnicode)

			// If we have reached here, it's non-zero, so try and read it, if the
			// offset is not larger than section.
			if uint32(sectionSize) > info.LocalBasePathOffsetUnicode && info.LocalBasePathOffsetUnicode != 0x00 {
				// Read unicode string.
				info.LocalBasePathUnicode = readUnicodeString(sectionData[info.LocalBasePathOffsetUnicode:])
			}
			// fmt.Println("LocalBasePathUnicode:", info.LocalBasePathUnicode)

			// Read CommonPathSuffixOffsetUnicode.
			err = binary.Read(sectionReader, binary.LittleEndian, &info.CommonPathSuffixOffsetUnicode)
			if err != nil {
				return info, fmt.Errorf("lnk.LinkInfo: read CommonPathSuffixOffsetUnicode - %s", err.Error())
			}
			// fmt.Println("CommonPathSuffixOffsetUnicode:", info.CommonPathSuffixOffsetUnicode)

			// Read it.
			if uint32(sectionSize) > info.CommonPathSuffixOffsetUnicode && info.CommonPathSuffixOffsetUnicode != 0x00 {
				// Read unicode string.
				info.CommonPathSuffixUnicode = readUnicodeString(sectionData[info.CommonPathSuffixOffsetUnicode:])
			}
			// fmt.Println("CommonPathSuffixUnicode:", info.CommonPathSuffixUnicode)
		}
	}

	// Check if CommonNetworkRelativeLinkAndPathSuffix flag is set.
	if bitMaskuint32(info.LinkInfoFlags, 1) {

		// Read and parse CommonNetworkRelativeLink, if it exists. It exists if the
		// CommonNetworkRelativeLinkAndPathSuffix is set and the offset is not zero.
		// TODO: Find lnks that have this to test.
		if info.CommonNetworkRelativeLinkOffset != 0x00 {
			// Create a reader from CommonNetworkRelativeLink data.
			nbuf := bytes.NewReader(sectionData[info.CommonNetworkRelativeLinkOffset:])
			// And parse it.
			info.NetworkRelativeLink, _ = CommonNetwork(nbuf, maxSize)
		}
	}
	return info, err
}

// String prints LinkInfoSection in a table.
func (li LinkInfoSection) String() string {

	var sb, flags strings.Builder
	// Append all flags.
	for _, fl := range li.LinkInfoFlagsStr {
		flags.WriteString(fl)
		flags.WriteString("\n")
	}

	table := tablewriter.NewWriter(&sb)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)

	table.SetHeader([]string{"LinkInfo", "Value"})

	table.Append([]string{"Size", uint32TableStr(li.Size)})
	table.Append([]string{"HeaderSize", uint32TableStr(li.LinkInfoHeaderSize)})
	table.Append([]string{"Flags", flags.String()})

	// Only add rows that exist (their offset is not zero).
	if li.LocalBasePathOffset != 0 {
		table.Append([]string{"LocalBasePathOffset", uint32TableStr(li.LocalBasePathOffset)})
		table.Append([]string{"LocalBasePath", li.LocalBasePath})
	}

	if li.CommonPathSuffixOffset != 0 {
		table.Append([]string{"CommonPathSuffixOffset", uint32TableStr(li.CommonPathSuffixOffset)})
		table.Append([]string{"CommonPathSuffix", li.CommonPathSuffix})
	}

	if li.LocalBasePathOffsetUnicode != 0 {
		table.Append([]string{"LocalBasePathOffsetUnicode", uint32TableStr(li.LocalBasePathOffsetUnicode)})
		table.Append([]string{"LocalBasePathUnicode", li.LocalBasePathUnicode})
	}

	if li.CommonPathSuffixOffsetUnicode != 0 {
		table.Append([]string{"CommonPathSuffixOffsetUnicode", uint32TableStr(li.CommonPathSuffixOffsetUnicode)})
		table.Append([]string{"CommonPathSuffixUnicode", li.CommonPathSuffixUnicode})
	}

	// Add VolumeID and CommonNetwork offsets if they are not zero.
	if li.VolumeIDOffset != 0 {
		table.Append([]string{"VolumeIDOffset", uint32TableStr(li.VolumeIDOffset)})
	}

	if li.CommonNetworkRelativeLinkOffset != 0 {
		table.Append([]string{"CommonNetworkRelativeLinkOffset", uint32TableStr(li.CommonNetworkRelativeLinkOffset)})
	}

	table.Render()

	// Print VolumeID in a separate table if it exists.
	if li.VolumeIDOffset != 0 {
		sb.WriteString("\n\n")
		sb.WriteString(li.VolID.String())
	}

	// Print CommonNetworkRelativeLink in a separate table if it exists.
	if li.CommonNetworkRelativeLinkOffset != 0 {
		sb.WriteString("\n\n")
		sb.WriteString(li.NetworkRelativeLink.String())
	}
	return sb.String()
}

// Dump returns the hex.Dump of section data.
func (li LinkInfoSection) Dump() string {
	return hex.Dump(li.Raw)
}

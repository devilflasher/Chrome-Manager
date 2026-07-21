package lnk

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// CommonNetworkRelativeLink (section 2.3.2)
// Information about the network location where a link target is stored,
type CommonNetworkRelativeLink struct {
	Size uint32

	// Only the first two bits are used. commonNetworkRelativeLinkFlags
	CommonNetworkRelativeLinkFlags uint32

	// String version of the flag.
	CommonNetworkRelativeLinkFlagsStr []string

	// Offset of NetName field from start of structure.
	// If value > 0x14, then NetNameOffsetUnicode must not exist.
	NetNameOffset uint32

	// Offset of DeviceName field from start of structure.
	DeviceNameOffset uint32

	// Type of NetworkProvider. See networkProviderType for table.
	// If ValidNetType is not set, ignore this.
	// NetworkProviderType uint32
	NetworkProviderType string // A uint32 in file, maps to networkProviderType.

	// Optional offset of NetNameUnicode. Must not exist if NetNameOffset > 0x14.
	NetNameOffsetUnicode uint32

	// Optional value of DeviceNameUnicode. Must not exist if NetNameOffset > 0x14.
	DeviceNameOffsetUnicode uint32

	// Server share path (e.g. \\server\share). Null-terminated string.
	NetName string

	// Device name like drive letter. Null-terminated string.
	DeviceName string

	// Unicode string. Must not exist if NetNameOffset > 0x14.
	NetNameUnicode string

	// Unicode string. Must not exist if NetNameOffset > 0x14.
	DeviceNameUnicode string
}

// commonNetworkRelativeLinkFlags is the index for CommonNetworkRelativeLinkFlags.
var commonNetworkRelativeLinkFlags = []string{
	// If 1, DeviceNameOffset is offset to device name.
	// If 0, DeviceNameOffset must be zero.
	"ValidDevice", // Bit 0

	// If 1, NetProviderType has network provider type.
	// If 0, NetProviderType must be zero.
	"ValidNetType", // Bit 1
}

// networkProviderType returns a string representing the network provider based
// on the value of the NetworkProviderType uint32 and "" for invalid values.
func networkProviderType(index uint32) string {
	networkMap := map[uint32]string{
		0x001A0000: "WNNC_NET_AVID",
		0x001B0000: "WNNC_NET_DOCUSPACE",
		0x001C0000: "WNNC_NET_MANGOSOFT",
		0x001D0000: "WNNC_NET_SERNET",
		0X001E0000: "WNNC_NET_RIVERFRONT1",
		0x001F0000: "WNNC_NET_RIVERFRONT2",
		0x00200000: "WNNC_NET_DECORB",
		0x00210000: "WNNC_NET_PROTSTOR",
		0x00220000: "WNNC_NET_FJ_REDIR",
		0x00230000: "WNNC_NET_DISTINCT",
		0x00240000: "WNNC_NET_TWINS",
		0x00250000: "WNNC_NET_RDR2SAMPLE",
		0x00260000: "WNNC_NET_CSC",
		0x00270000: "WNNC_NET_3IN1",
		0x00290000: "WNNC_NET_EXTENDNET",
		0x002A0000: "WNNC_NET_STAC",
		0x002B0000: "WNNC_NET_FOXBAT",
		0x002C0000: "WNNC_NET_YAHOO",
		0x002D0000: "WNNC_NET_EXIFS",
		0x002E0000: "WNNC_NET_DAV",
		0x002F0000: "WNNC_NET_KNOWARE",
		0x00300000: "WNNC_NET_OBJECT_DIRE",
		0x00310000: "WNNC_NET_MASFAX",
		0x00320000: "WNNC_NET_HOB_NFS",
		0x00330000: "WNNC_NET_SHIVA",
		0x00340000: "WNNC_NET_IBMAL",
		0x00350000: "WNNC_NET_LOCK",
		0x00360000: "WNNC_NET_TERMSRV",
		0x00370000: "WNNC_NET_SRT",
		0x00380000: "WNNC_NET_QUINCY",
		0x00390000: "WNNC_NET_OPENAFS",
		0X003A0000: "WNNC_NET_AVID1",
		0x003B0000: "WNNC_NET_DFS",
		0x003C0000: "WNNC_NET_KWNP",
		0x003D0000: "WNNC_NET_ZENWORKS",
		0x003E0000: "WNNC_NET_DRIVEONWEB",
		0x003F0000: "WNNC_NET_VMWARE",
		0x00400000: "WNNC_NET_RSFX",
		0x00410000: "WNNC_NET_MFILES",
		0x00420000: "WNNC_NET_MS_NFS",
		0x00430000: "WNNC_NET_GOOGLE",
	}
	val, exists := networkMap[index]
	if exists {
		return val
	}
	// If not found, return the hex encoded string.
	return uint32StrHex(index)
}

// CommonNetwork reads the section data and populates a CommonNetworkRelativeLink.
// Section 2.3.2 in docs.
func CommonNetwork(r io.Reader, maxSize uint64) (c CommonNetworkRelativeLink, err error) {
	// Read the section.
	sectionData, sectionReader, sectionSize, err := readSection(r, 4, maxSize)
	if err != nil {
		return c, fmt.Errorf("golnk.CommonNetwork: read CommonNetwork section - %s", err.Error())
	}
	c.Size = uint32(sectionSize)

	// fmt.Println("------")

	// fmt.Printf("Read section CommonNetwork. %d bytes.\n", sectionSize)

	// fmt.Println(hex.Dump(sectionData))

	// Read CommonNetworkRelativeLinkFlags.
	err = binary.Read(sectionReader, binary.LittleEndian, &c.CommonNetworkRelativeLinkFlags)
	if err != nil {
		return c, fmt.Errorf("golnk.CommonNetwork: read CommonNetworkRelativeLinkFlags - %s", err.Error())
	}
	// fmt.Println("CommonNetworkRelativeLinkFlags", c.CommonNetworkRelativeLinkFlags)

	// Parse the flag.
	for bitIndex := 0; bitIndex < len(commonNetworkRelativeLinkFlags); bitIndex++ {
		if bitMaskuint32(c.CommonNetworkRelativeLinkFlags, bitIndex) {
			c.CommonNetworkRelativeLinkFlagsStr =
				append(c.CommonNetworkRelativeLinkFlagsStr, commonNetworkRelativeLinkFlags[bitIndex])
		}
	}
	// fmt.Println("c.CommonNetworkRelativeLinkFlagsStr", c.CommonNetworkRelativeLinkFlagsStr)

	// Read NetNameOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &c.NetNameOffset)
	if err != nil {
		return c, fmt.Errorf("golnk.CommonNetwork: read NetNameOffset - %s", err.Error())
	}
	// fmt.Println("NetNameOffset", c.NetNameOffset)

	// Read DeviceNameOffset.
	err = binary.Read(sectionReader, binary.LittleEndian, &c.DeviceNameOffset)
	if err != nil {
		return c, fmt.Errorf("golnk.CommonNetwork: read DeviceNameOffset - %s", err.Error())
	}
	// fmt.Println("DeviceNameOffset", c.DeviceNameOffset)

	// Read NetworkProviderType.
	var nType uint32
	err = binary.Read(sectionReader, binary.LittleEndian, &nType)
	if err != nil {
		return c, fmt.Errorf("golnk.CommonNetwork: read NetworkProviderType - %s", err.Error())
	}
	// fmt.Println("nType", nType)
	// fmt.Printf("%x\n", nType)

	// Map nType to networkProviderType.
	c.NetworkProviderType = networkProviderType(nType)
	// fmt.Println("networkProviderType", c.NetworkProviderType)

	// If value of NetNameOffset field > 0x14, two optional fields are present:
	// NetNameOffsetUnicode - uint32
	// DeviceNameOffsetUnicode - uint32
	if c.NetNameOffset > 0x14 {
		// Read NetNameOffsetUnicode.
		err = binary.Read(sectionReader, binary.LittleEndian, &c.NetNameOffsetUnicode)
		if err != nil {
			return c, fmt.Errorf("golnk.CommonNetwork: read NetNameOffsetUnicode - %s", err.Error())
		}
		// fmt.Println("NetNameOffsetUnicode", c.NetNameOffsetUnicode)

		// Read DeviceNameOffsetUnicode.
		err = binary.Read(sectionReader, binary.LittleEndian, &c.DeviceNameOffsetUnicode)
		if err != nil {
			return c, fmt.Errorf("golnk.CommonNetwork: read DeviceNameOffsetUnicode - %s", err.Error())
		}
		// fmt.Println("DeviceNameOffsetUnicode", c.DeviceNameOffsetUnicode)
	} else {
		// fmt.Printf("NetNameOffset 0x%x smaller than or equal to 0x14, skipping Unicode fields.\n", c.NetNameOffset)
		// Read NetName from NetNameOffset as a null-terminated string.
		c.NetName = readString(sectionData[c.NetNameOffset:])
		// fmt.Println("NetName", c.NetName)
	}
	return c, err
}

// String prints CommonNetworkRelativeLink in a table.
func (c CommonNetworkRelativeLink) String() string {
	var sb, flags strings.Builder

	// Append all flags.
	for _, fl := range c.CommonNetworkRelativeLinkFlagsStr {
		flags.WriteString(fl)
		flags.WriteString("\n")
	}

	table := tablewriter.NewWriter(&sb)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)

	table.SetHeader([]string{"CommonNetworkRelativeLink", "Value"})

	table.Append([]string{"Size", uint32TableStr(c.Size)})
	table.Append([]string{"Flags", flags.String()})
	table.Append([]string{"NetworkProviderType", c.NetworkProviderType})

	// Only add rows that exist (their offset is not zero).
	if c.NetNameOffset != 0 {
		table.Append([]string{"NetNameOffset", uint32TableStr(c.NetNameOffset)})
		table.Append([]string{"NetName", c.NetName})
	}

	if c.DeviceNameOffset != 0 {
		table.Append([]string{"DeviceNameOffset", uint32TableStr(c.DeviceNameOffset)})
		table.Append([]string{"DeviceName", c.DeviceName})
	}

	if c.NetNameOffsetUnicode != 0 {
		table.Append([]string{"NetNameOffsetUnicode", uint32TableStr(c.NetNameOffsetUnicode)})
		table.Append([]string{"NetNameUnicode", c.NetNameUnicode})
	}

	if c.DeviceNameOffsetUnicode != 0 {
		table.Append([]string{"DeviceNameOffsetUnicode", uint32TableStr(c.DeviceNameOffsetUnicode)})
		table.Append([]string{"DeviceNameUnicode", c.DeviceNameUnicode})
	}

	table.Render()
	return sb.String()
}

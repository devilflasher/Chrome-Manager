package lnk

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

// ExtraDataSection represents section 2.5 of the specification.
type ExtraDataSection struct {
	Blocks []ExtraDataBlock
	// Terminal block at the end of the ExtraData section.
	// Value must be smaller than 0x04.
	TerminalBlock uint32
}

/*
	ExtraDataBlock represents one of the optional data blocks at the end of the
	lnk file.
	Each data block starts with a uint32 size and a uint32 signature.
	Detection is as follows:
	1. Read the uint32 size. If size < 0x04, it's the terminal block.
	2. Read the datablock (size-4) more bytes from the io.Reader.
	3. Read the uint32 signature. It will designate the datablock.
	4. Parse the data based on the signature.
*/
type ExtraDataBlock struct {
	Size      uint32
	Signature uint32
	Type      string
	Data      []byte
}

// DataBlock reads and populates an ExtraData.
func DataBlock(r io.Reader) (extra ExtraDataSection, err error) {

	var db ExtraDataBlock
	for {
		// Read size.
		var size uint32
		err = binary.Read(r, binary.LittleEndian, &size)
		if err != nil {
			return extra, fmt.Errorf("golnk.readDataBlock: read size - %s", err.Error())
		}
		// fmt.Println("Size", size)
		// Have we reached the TerminalBlock?
		if size < 0x04 {
			extra.TerminalBlock = size
			break
		}
		db.Size = size

		// Read block's signature.
		err = binary.Read(r, binary.LittleEndian, &db.Signature)
		if err != nil {
			return extra, fmt.Errorf("golnk.readDataBlock: read signature - %s", err.Error())
		}
		// fmt.Println("Signature", hex.EncodeToString(uint32Byte(db.Signature)))
		db.Type = blockSignature(db.Signature)
		// fmt.Println("Type:", db.Type)

		// Read the rest of the data. Size-8.
		data := make([]byte, db.Size-8)
		err = binary.Read(r, binary.LittleEndian, &data)
		if err != nil {
			return extra, fmt.Errorf("golnk.readDataBlock: read data - %s", err.Error())
		}
		db.Data = data
		// fmt.Println(hex.Dump(data))
		extra.Blocks = append(extra.Blocks, db)
	}
	return extra, nil
}

// blockSignature returns the block type based on signature.
func blockSignature(sig uint32) string {
	signatureMap := map[uint32]string{
		0xA0000002: "ConsoleDataBlock",
		0xA0000004: "ConsoleFEDataBlock",
		0xA0000006: "DarwinDataBlock",
		0xA0000001: "EnvironmentVariableDataBlock",
		0xA0000007: "IconEnvironmentDataBlock",
		0xA0000009: "PropertyStoreDataBlock",
		0xA0000008: "ShimDataBlock",
		0xA0000005: "SpecialFolderDataBlock",
		0xA0000003: "TrackerDataBlock",
		0xA000000C: "VistaAndAboveIDListDataBlock",
		0xA000000B: "KnownFolderDataBlock",
	}
	if val, exists := signatureMap[sig]; exists {
		return val
	}
	return "Signature Not Found - " + hex.EncodeToString(uint32Byte(sig))
}

// String prints the ExtraData blocks' Type, Size, and a hexdump of their content.
func (e ExtraDataSection) String() string {

	var sb strings.Builder
	for _, b := range e.Blocks {
		sb.WriteString(fmt.Sprintf("Size: %s\n", uint32TableStr(b.Size)))
		sb.WriteString(fmt.Sprintf("Signature: %s\n", uint32StrHex(b.Signature)))
		sb.WriteString(fmt.Sprintf("Type: %s\n", b.Type))
		sb.WriteString("Dump\n")
		sb.WriteString(b.Dump())
		sb.WriteString("-------------------------\n")
	}
	return sb.String()
}

// Dump returns the hex.Dump of ExtraDataBlock.
func (db ExtraDataBlock) Dump() string {
	return hex.Dump(db.Data)
}

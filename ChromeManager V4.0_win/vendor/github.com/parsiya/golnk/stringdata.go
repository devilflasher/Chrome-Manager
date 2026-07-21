package lnk

import (
	"io"
	"strings"

	"github.com/olekukonko/tablewriter"
)

// Optional StringData - Section 2.4.

// StringDataSection represents section 2.4 of the lnk.
// Fields are mostly optional and present if certain flags are set.
type StringDataSection struct {

	// On disk, all these fields have a uint16 size, followed by a string.
	// The string is not null-terminated.
	// The strings are unicode if IsUnicode is set in header.

	// NameString specifies a description of the shortcut that is displayed to
	// end users to identify the purpose of the shell link.
	// Present with HasName flag.
	NameString string

	// RelativePath specifies the location of the link target relative to the
	// file that contains the shell link.
	// Present with HasRelativePath flag.
	RelativePath string

	// WorkingDir specifies the file system path of the working directory to
	// be used when activating the link target.
	// Present with HasWorkingDir flag.
	WorkingDir string

	// CommandLineArguments stores the command-line arguments of the link target.
	// Present with HasArguments flag.
	CommandLineArguments string

	// IconLocation specifies the location of the icon to be used.
	// Present with HasIconLocation flag.
	IconLocation string
}

// StringData parses the StringData portion of the lnk.
// flags is the ShellLinkHeader.LinkFlags.
func StringData(r io.Reader, linkFlags FlagMap) (st StringDataSection, err error) {

	// Read unicode strings if is unicode flag is set.
	isUnicode := linkFlags["IsUnicode"]

	// Read NameString if HasName flag is set.
	if linkFlags["HasName"] {
		name, _ := readStringData(r, isUnicode)
		// fmt.Println("Name", name)
		st.NameString = name
	}

	// Read NameString if HasName flag is set.
	if linkFlags["HasRelativePath"] {
		st.RelativePath, err = readStringData(r, isUnicode)
		if err != nil {
			return st, err
		}
	}

	// Read WorkingDir if HasWorkingDir flag is set.
	if linkFlags["HasWorkingDir"] {
		st.WorkingDir, err = readStringData(r, isUnicode)
		if err != nil {
			return st, err
		}
	}

	// Read CommandLineArguments if HasArguments flag is set.
	if linkFlags["HasArguments"] {
		st.CommandLineArguments, err = readStringData(r, isUnicode)
		if err != nil {
			return st, err
		}
	}

	// Read IconLocation if HasIconLocation flag is set.
	if linkFlags["HasIconLocation"] {
		st.IconLocation, err = readStringData(r, isUnicode)
		if err != nil {
			return st, err
		}
	}
	return st, err
}

// String prints StringDataSection in a table.
func (st StringDataSection) String() string {
	var sb strings.Builder

	table := tablewriter.NewWriter(&sb)
	table.SetAlignment(tablewriter.ALIGN_LEFT)
	table.SetRowLine(true)

	table.SetHeader([]string{"StringData", "Value"})

	if st.NameString != "" {
		table.Append([]string{"NameString", st.NameString})
	}

	if st.RelativePath != "" {
		table.Append([]string{"RelativePath", st.RelativePath})
	}

	if st.WorkingDir != "" {
		table.Append([]string{"WorkingDir", st.WorkingDir})
	}

	if st.CommandLineArguments != "" {
		table.Append([]string{"CommandLineArguments", st.CommandLineArguments})
	}

	if st.IconLocation != "" {
		table.Append([]string{"IconLocation", st.IconLocation})
	}

	table.Render()
	return sb.String()
}

// toASCII converts English Unicode text to ASCII. First we detect if input is
// English text by checking if half of the string is 0x00, if so, remove those bytes.
func toASCII(str string) string {
	return strings.Replace(str, " ", "", -1)
}

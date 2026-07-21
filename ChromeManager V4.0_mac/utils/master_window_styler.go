package utils

type MasterWindowStyler struct {
	originalTitles map[uintptr]string
	titleToHwndMap map[string]uintptr
}

func NewMasterWindowStyler() *MasterWindowStyler {
	return &MasterWindowStyler{
		originalTitles: make(map[uintptr]string),
		titleToHwndMap: make(map[string]uintptr),
	}
}
func (m *MasterWindowStyler) SetMasterWindowStyle(hwnd uintptr) error {
	return nil
}

func (m *MasterWindowStyler) ClearMasterWindowStyle(hwnd uintptr) error {
	return nil
}

func (m *MasterWindowStyler) ForceClearMasterWindowStyle(hwnd uintptr) error {
	return nil
}

func (m *MasterWindowStyler) ClearAllMasterWindowStyles() {
	// Clears internal maps if needed
	m.originalTitles = make(map[uintptr]string)
	m.titleToHwndMap = make(map[string]uintptr)
}

func (m *MasterWindowStyler) IsSupported() bool {
	return false
}

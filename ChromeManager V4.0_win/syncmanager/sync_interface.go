package syncmanager

import (
	"chromemanager/platform/common"
	"fmt"
	"sync"
	"time"
)

type syncWindowMetadataSetter interface {
	SetSyncWindowMetadata(master *common.WindowInfo, slaves []common.WindowInfo) error
}

type cdpTabSyncStarter interface {
	StartCDPTabSyncAsync(delay time.Duration)
}

type cdpTabSyncReadyWaiter interface {
	WaitForCDPTabSyncReady(timeout time.Duration) bool
}

const syncStartupTimeout = 60 * time.Second

type SyncManager struct {
	// Platform Provider
	provider       common.SyncProvider
	settingsGetter SettingsGetter

	// State Management
	masterWindowInfo *WindowInfo
	slaveWindows     []WindowInfo
	isRunning        bool

	// Window Management
	importedWindows   map[int]WindowInfo
	selectedWindows   map[int]bool
	selectedWindowsMu sync.RWMutex
	mu                sync.RWMutex
}

func NewSyncManager(provider common.SyncProvider, settingsGetter SettingsGetter) *SyncManager {
	return &SyncManager{
		provider:        provider,
		settingsGetter:  settingsGetter,
		importedWindows: make(map[int]WindowInfo),
		selectedWindows: make(map[int]bool),
	}
}

func (sm *SyncManager) Start(args ...interface{}) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.isRunning {
		return fmt.Errorf("sync is already running")
	}

	if len(args) != 2 {
		return fmt.Errorf("Start requires exactly 2 arguments")
	}

	var masterHandle common.WindowHandle
	var slaveHandles []common.WindowHandle

	// Parse arguments
	if masterWindow, ok := args[0].(*WindowInfo); ok {
		masterHandle = common.WindowHandle(masterWindow.HWND)
		sm.masterWindowInfo = masterWindow
	} else if masterHWND, ok := args[0].(uintptr); ok {
		masterHandle = common.WindowHandle(masterHWND)
		sm.masterWindowInfo = &WindowInfo{HWND: masterHWND}
	} else {
		return fmt.Errorf("invalid master argument type")
	}

	if slaves, ok := args[1].([]WindowInfo); ok {
		slaveHandles = make([]common.WindowHandle, len(slaves))
		sm.slaveWindows = slaves
		for i, s := range slaves {
			slaveHandles[i] = common.WindowHandle(s.HWND)
		}
	} else if slaveHWNDs, ok := args[1].([]uintptr); ok {
		slaveHandles = make([]common.WindowHandle, len(slaveHWNDs))
		sm.slaveWindows = make([]WindowInfo, len(slaveHWNDs))
		for i, h := range slaveHWNDs {
			slaveHandles[i] = common.WindowHandle(h)
			sm.slaveWindows[i] = WindowInfo{HWND: h}
		}
	} else {
		return fmt.Errorf("invalid slave argument type")
	}

	if metadataSetter, ok := sm.provider.(syncWindowMetadataSetter); ok {
		if err := metadataSetter.SetSyncWindowMetadata(
			toCommonWindowInfo(sm.masterWindowInfo),
			toCommonWindowInfos(sm.slaveWindows),
		); err != nil {
			return fmt.Errorf("failed to set sync window metadata: %w", err)
		}
	}

	// Start Sync via Provider
	if err := sm.provider.Start(masterHandle, slaveHandles); err != nil {
		return err
	}

	sm.isRunning = true
	if tabSyncStarter, ok := sm.provider.(cdpTabSyncStarter); ok {
		tabSyncStarter.StartCDPTabSyncAsync(0)
		if readyWaiter, ok := sm.provider.(cdpTabSyncReadyWaiter); ok {
			if !readyWaiter.WaitForCDPTabSyncReady(syncStartupTimeout) {
				_ = sm.provider.Stop()
				sm.isRunning = false
				return fmt.Errorf("CDP/DOM sync not ready within %s", syncStartupTimeout)
			}
		}
	}
	return nil
}

func (sm *SyncManager) Stop() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return nil
	}

	if err := sm.provider.Stop(); err != nil {
		return err
	}

	sm.isRunning = false
	return nil
}

func (sm *SyncManager) StopSync() error {
	return sm.Stop()
}

func (sm *SyncManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.isRunning
}

func (sm *SyncManager) GetSyncState() SyncState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	status := SyncStatusIdle
	if sm.isRunning {
		status = SyncStatusRunning
	}

	return SyncState{
		Status:           status,
		MasterWindowInfo: sm.masterWindowInfo,
		SlaveWindows:     sm.slaveWindows,
		TotalAgents:      len(sm.slaveWindows),
		ActiveAgents:     len(sm.slaveWindows),
		Config:           DefaultSyncConfig(),
	}
}

func (sm *SyncManager) GetMasterWindow() *WindowInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.masterWindowInfo
}

func (sm *SyncManager) GetSlaveWindows() []WindowInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.slaveWindows
}

func (sm *SyncManager) UpdateWindowInfo(windowInfo WindowInfo) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.masterWindowInfo != nil && sm.masterWindowInfo.HWND == windowInfo.HWND {
		sm.masterWindowInfo = &windowInfo
	}

	for i, slave := range sm.slaveWindows {
		if slave.HWND == windowInfo.HWND {
			sm.slaveWindows[i] = windowInfo
			break
		}
	}

	return nil
}

func (sm *SyncManager) Cleanup() error {
	return sm.Stop()
}

func (sm *SyncManager) NotifyWindowsImported(windows []WindowInfo) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 全量替换，确保已经物理关闭的窗口不再出现在列表中
	sm.importedWindows = make(map[int]WindowInfo)

	for _, window := range windows {
		sm.importedWindows[window.Number] = window
	}

	return nil
}

func (sm *SyncManager) RemoveImportedWindows(ids []int) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.importedWindows == nil {
		return nil
	}

	for _, id := range ids {
		delete(sm.importedWindows, id)
	}

	return nil
}

func (sm *SyncManager) GetAllImportedWindows() []WindowInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var windows []WindowInfo
	if sm.importedWindows != nil {
		for _, window := range sm.importedWindows {
			windows = append(windows, window)
		}
	}

	return windows
}

func (sm *SyncManager) GetWindowByID(id int) *WindowInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if sm.importedWindows != nil {
		if window, exists := sm.importedWindows[id]; exists {
			return &window
		}
	}

	if sm.masterWindowInfo != nil && sm.masterWindowInfo.Number == id {
		return sm.masterWindowInfo
	}

	for _, window := range sm.slaveWindows {
		if window.Number == id {
			return &window
		}
	}

	return nil
}

func (sm *SyncManager) SetMasterWindow(windowID int) error {
	window := sm.GetWindowByID(windowID)
	if window == nil {
		return fmt.Errorf("window with ID %d not found", windowID)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.masterWindowInfo = window
	return nil
}

func (sm *SyncManager) GetState() SyncState {
	return sm.GetSyncState()
}

func (sm *SyncManager) PauseSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return nil
	}
	return sm.provider.PauseSync()
}

func (sm *SyncManager) ResumeSync() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.isRunning {
		return nil
	}
	return sm.provider.ResumeSync()
}

// SelectAllWindows 更新所有窗口的选中状态
func (sm *SyncManager) SelectAllWindows(selected bool) error {
	sm.selectedWindowsMu.Lock()
	defer sm.selectedWindowsMu.Unlock()

	if selected {
		// 选中所有已导入的窗口
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		for id := range sm.importedWindows {
			sm.selectedWindows[id] = true
		}
	} else {
		// 清空选中
		sm.selectedWindows = make(map[int]bool)
	}
	return nil
}

// SetWindowSelected 设置单个窗口的选中状态
func (sm *SyncManager) SetWindowSelected(id int, selected bool) {
	sm.selectedWindowsMu.Lock()
	defer sm.selectedWindowsMu.Unlock()
	if selected {
		sm.selectedWindows[id] = true
	} else {
		delete(sm.selectedWindows, id)
	}
}

// GetSelectedWindowNumbers 获取当前选中的窗口编号
func (sm *SyncManager) GetSelectedWindowNumbers() []int {
	sm.selectedWindowsMu.RLock()
	defer sm.selectedWindowsMu.RUnlock()

	var ids []int
	for id, selected := range sm.selectedWindows {
		if selected {
			ids = append(ids, id)
		}
	}
	return ids
}

func toCommonWindowInfo(info *WindowInfo) *common.WindowInfo {
	if info == nil {
		return nil
	}

	return &common.WindowInfo{
		Handle:      common.WindowHandle(info.HWND),
		HWND:        info.HWND,
		Title:       info.Title,
		ProcessID:   info.PID,
		Number:      info.Number,
		DebugPort:   info.DebugPort,
		UserDataDir: info.UserDataDir,
		CommandLine: info.CommandLine,
	}
}

func toCommonWindowInfos(infos []WindowInfo) []common.WindowInfo {
	if len(infos) == 0 {
		return nil
	}

	out := make([]common.WindowInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, common.WindowInfo{
			Handle:      common.WindowHandle(info.HWND),
			HWND:        info.HWND,
			Title:       info.Title,
			ProcessID:   info.PID,
			Number:      info.Number,
			DebugPort:   info.DebugPort,
			UserDataDir: info.UserDataDir,
			CommandLine: info.CommandLine,
		})
	}

	return out
}

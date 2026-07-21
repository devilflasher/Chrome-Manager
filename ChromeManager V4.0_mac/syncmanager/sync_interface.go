package syncmanager

import (
	"chromemanager/platform/common"
	"fmt"
	"sync"
)

type SyncManager struct {
	// Platform Provider
	provider       common.SyncProvider
	settingsGetter SettingsGetter

	// State Management
	masterWindowInfo *WindowInfo
	slaveWindows     []WindowInfo
	isRunning        bool

	// Window Management
	importedWindows map[int]WindowInfo
	mu              sync.RWMutex
}

func NewSyncManager(provider common.SyncProvider, settingsGetter SettingsGetter) *SyncManager {
	return &SyncManager{
		provider:        provider,
		settingsGetter:  settingsGetter,
		importedWindows: make(map[int]WindowInfo),
	}
}

// SetProgressCallback registers a callback for progress updates
func (sm *SyncManager) SetProgressCallback(cb func(string)) {
	type ProgressSetter interface {
		SetProgressCallback(func(string))
	}
	if setter, ok := sm.provider.(ProgressSetter); ok {
		setter.SetProgressCallback(cb)
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
	} else if masterWH, ok := args[0].(common.WindowHandle); ok {
		masterHandle = masterWH
		masterInfo := sm.windowInfoForHandleLocked(masterWH)
		sm.masterWindowInfo = &masterInfo
	} else {
		return fmt.Errorf("invalid master argument type: %T", args[0])
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
	} else if slaveWHs, ok := args[1].([]common.WindowHandle); ok {
		slaveHandles = slaveWHs
		sm.slaveWindows = make([]WindowInfo, len(slaveWHs))
		for i, h := range slaveWHs {
			sm.slaveWindows[i] = sm.windowInfoForHandleLocked(h)
		}
	} else {
		return fmt.Errorf("invalid slave argument type: %T", args[1])
	}

	// Start Sync via Provider
	if err := sm.provider.Start(masterHandle, slaveHandles); err != nil {
		return err
	}

	sm.isRunning = true
	return nil
}

func (sm *SyncManager) windowInfoForHandleLocked(handle common.WindowHandle) WindowInfo {
	for _, window := range sm.importedWindows {
		if window.HWND == uintptr(handle) || (window.PID > 0 && window.PID == int32(handle)) {
			return window
		}
	}
	return WindowInfo{HWND: uintptr(handle)}
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

func (sm *SyncManager) BeginProgrammaticMasterTarget(url string) {
	type programmaticTargetTracker interface {
		BeginProgrammaticMasterTarget(string)
	}
	if tracker, ok := sm.provider.(programmaticTargetTracker); ok {
		tracker.BeginProgrammaticMasterTarget(url)
	}
}

func (sm *SyncManager) ConfirmProgrammaticMasterTarget(targetID string) {
	type programmaticTargetTracker interface {
		ConfirmProgrammaticMasterTarget(string)
	}
	if tracker, ok := sm.provider.(programmaticTargetTracker); ok {
		tracker.ConfirmProgrammaticMasterTarget(targetID)
	}
}

func (sm *SyncManager) CancelProgrammaticMasterTarget() {
	type programmaticTargetTracker interface {
		CancelProgrammaticMasterTarget()
	}
	if tracker, ok := sm.provider.(programmaticTargetTracker); ok {
		tracker.CancelProgrammaticMasterTarget()
	}
}

func (sm *SyncManager) MapProgrammaticPageTargets(masterTargetID, targetURL string, targetIDs map[int]string) error {
	type programmaticTargetMapper interface {
		MapProgrammaticPageTargets(string, string, map[int]string) error
	}
	mapper, ok := sm.provider.(programmaticTargetMapper)
	if !ok {
		return nil
	}
	return mapper.MapProgrammaticPageTargets(masterTargetID, targetURL, targetIDs)
}

func (sm *SyncManager) RefreshPageTargetMappings(preferredTargetIDs map[int]string) error {
	type pageTargetRefresher interface {
		RefreshPageTargetMappings(map[int]string) error
	}
	refresher, ok := sm.provider.(pageTargetRefresher)
	if !ok {
		return fmt.Errorf("provider does not support page target refresh")
	}
	return refresher.RefreshPageTargetMappings(preferredTargetIDs)
}

// InsertTextToPID inserts text into a specific Chrome window via CDP
// This is used for batch input on macOS
func (sm *SyncManager) InsertTextToPID(pid int, text string) error {
	// Use type assertion to check if provider supports InsertTextToPID
	if inserter, ok := sm.provider.(interface{ InsertTextToPID(int, string) error }); ok {
		return inserter.InsertTextToPID(pid, text)
	}
	return fmt.Errorf("provider does not support InsertTextToPID")
}

// ExecuteBatchInput forwards batch input to the platform provider
func (sm *SyncManager) ExecuteBatchInput(pids []int, text string, delayed bool, overwrite bool) {
	if sm.provider != nil {
		if provider, ok := sm.provider.(interface {
			ExecuteBatchInput([]int, string, bool, bool)
		}); ok {
			provider.ExecuteBatchInput(pids, text, delayed, overwrite)
		}
	}
}

// EvaluateJSOnPort evaluates JavaScript on a Chrome instance identified by its debug port.
// It reuses an existing CDP connection from the sync engine if available, avoiding conflicts.
func (sm *SyncManager) EvaluateJSOnPort(port int, expression string) error {
	if sm.provider != nil {
		if provider, ok := sm.provider.(interface {
			EvaluateJSOnPort(int, string) error
		}); ok {
			return provider.EvaluateJSOnPort(port, expression)
		}
	}
	return fmt.Errorf("provider does not support EvaluateJSOnPort")
}

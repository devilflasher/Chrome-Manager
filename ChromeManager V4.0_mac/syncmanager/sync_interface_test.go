package syncmanager

import (
	"chromemanager/platform/common"
	"testing"
)

type testSyncProvider struct {
	master common.WindowHandle
	slaves []common.WindowHandle
}

func (p *testSyncProvider) Start(master common.WindowHandle, slaves []common.WindowHandle) error {
	p.master = master
	p.slaves = append([]common.WindowHandle(nil), slaves...)
	return nil
}

func (*testSyncProvider) Stop() error                                 { return nil }
func (*testSyncProvider) IsRunning() bool                             { return true }
func (*testSyncProvider) GetState() common.SyncState                  { return common.SyncState{} }
func (*testSyncProvider) SetConfig(common.SyncConfig) error           { return nil }
func (*testSyncProvider) GetConfig() common.SyncConfig                { return common.SyncConfig{} }
func (*testSyncProvider) PauseSync() error                            { return nil }
func (*testSyncProvider) ResumeSync() error                           { return nil }
func (*testSyncProvider) ExecuteBatchInput([]int, string, bool, bool) {}
func (*testSyncProvider) GetActiveTargetID(int) string                { return "" }

func TestStartRetainsImportedWindowMetadataForPIDHandles(t *testing.T) {
	provider := &testSyncProvider{}
	sm := NewSyncManager(provider, nil)
	master := WindowInfo{Number: 1, PID: 101, HWND: 9001, DebugPort: 9223, IsRunning: true}
	slave := WindowInfo{Number: 2, PID: 202, HWND: 9002, DebugPort: 9224, IsRunning: true}
	if err := sm.NotifyWindowsImported([]WindowInfo{master, slave}); err != nil {
		t.Fatal(err)
	}

	if err := sm.Start(common.WindowHandle(master.PID), []common.WindowHandle{common.WindowHandle(slave.PID)}); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetMasterWindow(); got == nil || got.Number != master.Number || got.PID != master.PID || got.DebugPort != master.DebugPort {
		t.Fatalf("master metadata was lost: %#v", got)
	}
	slaves := sm.GetSlaveWindows()
	if len(slaves) != 1 || slaves[0].Number != slave.Number || slaves[0].PID != slave.PID || slaves[0].DebugPort != slave.DebugPort {
		t.Fatalf("slave metadata was lost: %#v", slaves)
	}
}

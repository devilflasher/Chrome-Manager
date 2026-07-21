package darwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework ApplicationServices -framework CoreGraphics -framework Carbon

#include <stdlib.h>
#include <Carbon/Carbon.h>

static int IsSecureInputEnabled_C() {
    return IsSecureEventInputEnabled() ? 1 : 0;
}

// Forward declarations of C functions from sync_events.c
int StartEventTap();
void StopEventTap();
void PostEventToPid(int pid, int type, int x, int y, int keyCode, int modifiers);
void PostEventToPidCaptured(int pid, int type, int x, int y, int keyCode, int modifiers);
void PostKeyEventToPid(int pid, int type, int keyCode, int modifiers);
void PostKeyEventGlobal(int type, int keyCode, int modifiers);
int GetFrontmostPID_C();
int ActivateProcess_C(int pid);
void RunEventLoop();


*/
import "C"

// Go Wrappers for C functions

func StartEventTap() bool {
	return C.StartEventTap() == 1
}

func StopEventTap() {
	C.StopEventTap()
}

func RunEventLoop() {
	C.RunEventLoop()
}

func PostEventToProcess(pid int, evtType int, x, y int, keyCode int, modifiers int) {
	C.PostEventToPid(C.int(pid), C.int(evtType), C.int(x), C.int(y), C.int(keyCode), C.int(modifiers))
}

func PostEventToProcessCaptured(pid int, evtType int, x, y int, keyCode int, modifiers int) {
	C.PostEventToPidCaptured(C.int(pid), C.int(evtType), C.int(x), C.int(y), C.int(keyCode), C.int(modifiers))
}

func PostKeyEventToProcess(pid int, evtType int, keyCode int, modifiers int) {
	C.PostKeyEventToPid(C.int(pid), C.int(evtType), C.int(keyCode), C.int(modifiers))
}

func PostKeyEventGlobal(evtType int, keyCode int, modifiers int) {
	C.PostKeyEventGlobal(C.int(evtType), C.int(keyCode), C.int(modifiers))
}

func GetFrontmostPID() int {
	return int(C.GetFrontmostPID_C())
}

func ActivateProcess(pid int) bool {
	return C.ActivateProcess_C(C.int(pid)) == 1
}

func IsSecureInputEnabled() bool {
	return C.IsSecureInputEnabled_C() == 1
}

//export handleMouseEvent
func handleMouseEvent(evtType C.int, x C.int, y C.int, data C.int, pid C.int) C.int {
	defer func() { recover() }()
	if globalSyncManager != nil {
		if globalSyncManager.HandleNativeMouseEvent(int(evtType), int(x), int(y), int(data), int(pid)) {
			return 1
		}
	}
	return 0
}

//export handleKeyboardEvent
func handleKeyboardEvent(evtType C.int, keyCode C.int, modifiers C.int, pid C.int, chars *C.char) C.int {
	defer func() { recover() }()
	if globalSyncManager != nil {
		charStr := C.GoString(chars)
		if globalSyncManager.HandleNativeKeyboardEvent(int(evtType), int(keyCode), int(modifiers), int(pid), charStr) {
			return 1
		}
	}
	return 0
}

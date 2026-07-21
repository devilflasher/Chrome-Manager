package darwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Carbon -framework Cocoa

#import <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>

// Forward declaration of the Go callback function
extern void HotkeyCallback(int id);

// Objective-C Callback wrapper
static OSStatus hotkeyHandler(EventHandlerCallRef nextHandler, EventRef theEvent, void *userData) {
    EventHotKeyID hkCom;
    GetEventParameter(theEvent, kEventParamDirectObject, typeEventHotKeyID, NULL,
                      sizeof(hkCom), NULL, &hkCom);
    
    // Call the Go function
    HotkeyCallback(hkCom.id);
    
    return noErr;
}

// Global variable to keep track of if the handler is installed
static EventHandlerRef gEventHandler = NULL;

// Global map to hold HotKeyRefs for unregistering
// In actual C code, we can't easily hold a dynamic map without more ceremony,
// so we'll just pass pointers back and forth to Go.

static EventHotKeyRef RegisterGlobalHotkey(int id, int modifiers, int keyCode) {
    if (gEventHandler == NULL) {
        EventTypeSpec eventType;
        eventType.eventClass = kEventClassKeyboard;
        eventType.eventKind = kEventHotKeyPressed;
        
        InstallApplicationEventHandler(&hotkeyHandler, 1, &eventType, NULL, &gEventHandler);
    }
    
    EventHotKeyID hotkeyID;
    hotkeyID.signature = 'htk1';
    hotkeyID.id = id;
    
    EventHotKeyRef hotkeyRef;
    OSStatus status = RegisterEventHotKey(keyCode, modifiers, hotkeyID, GetApplicationEventTarget(), 0, &hotkeyRef);
    
    if (status != noErr) {
        return NULL;
    }
    return hotkeyRef;
}

static void UnregisterGlobalHotkey(EventHotKeyRef ref) {
    if (ref != NULL) {
        UnregisterEventHotKey(ref);
    }
}
*/
import "C"
import (
	"fmt"
	"sync"
	"chromemanager/platform/common"
)

var (
	hotkeyCallbacks = make(map[int]func())
	hotkeyRefs      = make(map[int]C.EventHotKeyRef)
	hotkeyMutex     sync.RWMutex
)

//export HotkeyCallback
func HotkeyCallback(id C.int) {
	hotkeyMutex.RLock()
	cb, ok := hotkeyCallbacks[int(id)]
	hotkeyMutex.RUnlock()

	if ok && cb != nil {
		// Run callback in a goroutine to avoid blocking the Carbon event loop
		go cb()
	}
}

// parseMacModifiers converts cross-platform modifier flags to Carbon modifiers
func parseMacModifiers(mods common.KeyModifier) int {
	carbonMods := 0
	if mods.HasModifier(common.ModWin) {
		carbonMods |= C.cmdKey
	}
	if mods.HasModifier(common.ModControl) {
		carbonMods |= C.controlKey
	}
	if mods.HasModifier(common.ModAlt) {
		carbonMods |= C.optionKey
	}
	if mods.HasModifier(common.ModShift) {
		carbonMods |= C.shiftKey
	}
	return carbonMods
}

// Convert common.HotkeySpec to Carbon parameters and register
func registerMacHotkey(id int, spec common.HotkeySpec, callback func()) error {
	hotkeyMutex.Lock()
	defer hotkeyMutex.Unlock()

	if _, exists := hotkeyRefs[id]; exists {
		return common.ErrHotkeyAlreadyRegistered
	}

	carbonMods := parseMacModifiers(spec.Modifiers)
	
	
	// Map common.KeyCode to Mac KeyCode
	keyCode := mapCharToMacKeyCode(spec.Key)
	if keyCode == -1 {
		return fmt.Errorf("unsupported hotkey character: %v", spec.Key)
	}

	ref := C.RegisterGlobalHotkey(C.int(id), C.int(carbonMods), C.int(keyCode))
	if ref == nil {
		return fmt.Errorf("failed to register hotkey with Carbon")
	}

	hotkeyRefs[id] = ref
	hotkeyCallbacks[id] = callback

	return nil
}

func unregisterMacHotkey(id int) error {
	hotkeyMutex.Lock()
	defer hotkeyMutex.Unlock()

	ref, exists := hotkeyRefs[id]
	if !exists {
		return nil // Already not registered
	}

	C.UnregisterGlobalHotkey(ref)

	delete(hotkeyRefs, id)
	delete(hotkeyCallbacks, id)

	return nil
}

func isMacHotkeyRegistered(id int) bool {
	hotkeyMutex.RLock()
	defer hotkeyMutex.RUnlock()
	_, exists := hotkeyRefs[id]
	return exists
}

func shutdownMacHotkeys() {
	hotkeyMutex.Lock()
	defer hotkeyMutex.Unlock()

	for id, ref := range hotkeyRefs {
		if ref != nil {
			C.UnregisterGlobalHotkey(ref)
		}
		delete(hotkeyRefs, id)
		delete(hotkeyCallbacks, id)
	}
}

// mapCharToMacKeyCode provides a basic mapping from JS/DOM key values to Mac virtual keycodes
func mapCharToMacKeyCode(keyCode common.KeyCode) int {
	// A simple mapping of standard common.KeyCode values to Mac Virtual KeyCodes
	// Reference from key_mapper.go
	switch keyCode {
	case common.KeyA: return 0
	case common.KeyC: return 8
	case common.KeyV: return 9
	case common.KeyEnter: return 36
	case common.KeySpace: return 49
	case common.KeyEsc: return 53
	case common.KeyTab: return 48
	case common.KeyBack: return 51
	}

	// For alphanumeric characters not specifically defined in common.KeyCode
	// If the user's "Key" in HotkeySpec happens to be defined using ASCII for simplicity (e.g., 'S')
	// Here we provide a limited fallback mapping for the 'SyncToggleHotkey' characters.
	switch rune(keyCode) {
	case 's', 'S': return 1
	case 'd', 'D': return 2
	case 'f', 'F': return 3
	case 'h', 'H': return 4
	case 'g', 'G': return 5
	case 'z', 'Z': return 6
	case 'x', 'X': return 7
	case 'b', 'B': return 11
	case 'q', 'Q': return 12
	case 'w', 'W': return 13
	case 'e', 'E': return 14
	case 'r', 'R': return 15
	case 'y', 'Y': return 16
	case 't', 'T': return 17
	case 'u', 'U': return 32
	case 'i', 'I': return 34
	case 'o', 'O': return 31
	case 'p', 'P': return 35
	case 'j', 'J': return 38
	case 'k', 'K': return 40
	case 'l', 'L': return 37
	case 'n', 'N': return 45
	case 'm', 'M': return 46
	}

	return -1
}

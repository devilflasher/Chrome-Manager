package darwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework Carbon -framework ApplicationServices

#import <Cocoa/Cocoa.h>
#import <dispatch/dispatch.h>
#import <ApplicationServices/ApplicationServices.h>

// Structure to hold window info for C-Go passing
typedef struct {
    unsigned long long id;
    int x;
    int y;
    int w;
    int h;
    char title[256];
} CRectC;

typedef struct {
    int x;
    int y;
    int w;
    int h;
    char url[2048];
} CWebAreaC;

static int CheckAccessibilityPermission_C(int prompt) {
    NSDictionary *options = @{(__bridge id)kAXTrustedCheckOptionPrompt: prompt ? @YES : @NO};
    return AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)options) ? 1 : 0;
}

// Get Main Screen Height for coordinate conversion
static CGFloat GetPrimaryScreenHeight() {
    NSArray *screens = [NSScreen screens];
    if ([screens count] > 0) {
        return [[screens objectAtIndex:0] frame].size.height;
    }
    return 0;
}

// Helper to safely run on main thread
static void RunOnMainThreadSync(void (^block)(void)) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}

static void GetMainWindowPosition(int *x, int *y, int *w, int *h, int *found) {
    *found = 0;
    RunOnMainThreadSync(^{
        NSArray *windows = [NSApp windows];
        CGFloat screenH = GetPrimaryScreenHeight();

        for (NSWindow *window in windows) {
            if (![window isVisible]) continue;
            if ([window isMiniaturized]) continue;

            NSRect frame = [window frame];
            if (frame.size.width < 100 || frame.size.height < 100) continue;

            *found = 1;
            *x = (int)frame.origin.x;
            *y = (int)(screenH - frame.origin.y - frame.size.height);
            *w = (int)frame.size.width;
            *h = (int)frame.size.height;
            return;
        }
    });
}

static void SetWindowPosition(int x, int y, int w, int h) {
    RunOnMainThreadSync(^{
        NSArray *windows = [NSApp windows];
        CGFloat screenH = GetPrimaryScreenHeight();

        for (NSWindow *window in windows) {
             if (![window isVisible]) continue;

             CGFloat cocoaY = screenH - (CGFloat)y - (CGFloat)h;
             NSRect newFrame = NSMakeRect((CGFloat)x, cocoaY, (CGFloat)w, (CGFloat)h);

             [window setFrame:newFrame display:YES];

             if ([window isMiniaturized]) {
                 [window deminiaturize:nil];
             }
             break;
        }
    });
}

static void BringWindowToTop() {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp activateIgnoringOtherApps:YES];
        for (NSWindow *window in [NSApp windows]) {
            if ([window isVisible]) {
                [window makeKeyAndOrderFront:nil];
                break;
            }
        }
    });
}

static void NativeMinimize() {
    RunOnMainThreadSync(^{
        NSArray *windows = [NSApp windows];
        for (NSWindow *window in windows) {
             if ([window isVisible]) {
                 if (![window isMiniaturized]) {
                     NSUInteger mask = [window styleMask];
                     if (!(mask & NSWindowStyleMaskMiniaturizable)) {
                         [window setStyleMask: mask | NSWindowStyleMaskMiniaturizable];
                     }
                     [window miniaturize:nil];
                 }
                 break;
             }
        }
    });
}

static void NativeTerminate() {
    RunOnMainThreadSync(^{
        [NSApp terminate:nil];
    });
}

// Graceful Terminate by PID using NSRunningApplication
static void NativeTerminatePID(int pid) {
    RunOnMainThreadSync(^{
        NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
        if (app) {
            [app terminate];
        } else {
             // Fallback usually not needed if PID is valid and AX access is granted
             kill((pid_t)pid, SIGTERM);
        }
    });
}

// NativeCloseWindow closes a specific window using its AXUIElementRef (passed as uintptr)
static void NativeCloseWindow(unsigned long long hwnd) {
    if (hwnd == 0) return;

    AXUIElementRef winRef = (AXUIElementRef)hwnd;

    // Attempt to find the Close Button
    AXUIElementRef closeButton = NULL;
    AXError err = AXUIElementCopyAttributeValue(winRef, kAXCloseButtonAttribute, (CFTypeRef *)&closeButton);

    if (err == kAXErrorSuccess && closeButton) {
        AXUIElementPerformAction(closeButton, kAXPressAction);
        CFRelease(closeButton);
    }

    CFRelease(winRef);
}

// NativeSetWindowPosition sets the position and size of an external window using AX API
static void NativeSetWindowPosition(unsigned long long hwnd, int x, int y, int w, int h) {
    if (hwnd == 0) return;

    AXUIElementRef winRef = (AXUIElementRef)hwnd;

    // 1. Set Position
    CGPoint pos = CGPointMake((CGFloat)x, (CGFloat)y);
    AXValueRef posVal = AXValueCreate(kAXValueCGPointType, &pos);
    if (posVal) {
        AXUIElementSetAttributeValue(winRef, kAXPositionAttribute, posVal);
        CFRelease(posVal);
    }

    // 2. Set Size
    CGSize size = CGSizeMake((CGFloat)w, (CGFloat)h);
    AXValueRef sizeVal = AXValueCreate(kAXValueCGSizeType, &size);
    if (sizeVal) {
        AXUIElementSetAttributeValue(winRef, kAXSizeAttribute, sizeVal);
        CFRelease(sizeVal);
    }
    // Note: treating hwnd as borrowed reference
}

static void NativeRaiseWindow(unsigned long long hwnd) {
    if (hwnd == 0) return;

    AXUIElementRef winRef = (AXUIElementRef)hwnd;
    AXUIElementPerformAction(winRef, kAXRaiseAction);
    // Note: treating hwnd as borrowed reference
}

static void NativeActivateAndRaiseWindow(unsigned long long hwnd) {
    if (hwnd == 0) return;

    AXUIElementRef winRef = (AXUIElementRef)hwnd;
    pid_t targetPid = 0;
    if (AXUIElementGetPid(winRef, &targetPid) == kAXErrorSuccess && targetPid > 0) {
        AXUIElementRef appRef = AXUIElementCreateApplication(targetPid);
        if (appRef) {
            AXUIElementSetAttributeValue(appRef, kAXFrontmostAttribute, kCFBooleanTrue);
            AXUIElementSetAttributeValue(appRef, kAXFocusedWindowAttribute, winRef);
            CFRelease(appRef);
        }
        RunOnMainThreadSync(^{
            NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:targetPid];
            if (app) {
                [app activateWithOptions:0];
            }
        });
    }
    AXUIElementSetAttributeValue(winRef, kAXFocusedAttribute, kCFBooleanTrue);
    AXUIElementPerformAction(winRef, kAXRaiseAction);
    // Note: treating hwnd as borrowed reference
}

static int ChromeZoomMenuItemMatches(AXUIElementRef element, int direction) {
    CFTypeRef roleValue = NULL;
    if (AXUIElementCopyAttributeValue(element, kAXRoleAttribute, &roleValue) != kAXErrorSuccess || !roleValue) {
        return 0;
    }
    int isMenuItem = CFGetTypeID(roleValue) == CFStringGetTypeID() &&
        CFStringCompare((CFStringRef)roleValue, kAXMenuItemRole, 0) == kCFCompareEqualTo;
    CFRelease(roleValue);
    if (!isMenuItem) return 0;

    CFTypeRef charValue = NULL;
    if (AXUIElementCopyAttributeValue(element, kAXMenuItemCmdCharAttribute, &charValue) != kAXErrorSuccess || !charValue) {
        return 0;
    }

    char shortcut[16] = {0};
    int matches = 0;
    if (CFGetTypeID(charValue) == CFStringGetTypeID() &&
        CFStringGetCString((CFStringRef)charValue, shortcut, sizeof(shortcut), kCFStringEncodingUTF8)) {
        if (direction > 0) {
            matches = strcmp(shortcut, "+") == 0 || strcmp(shortcut, "=") == 0;
        } else if (direction < 0) {
            matches = strcmp(shortcut, "-") == 0;
        } else {
            matches = strcmp(shortcut, "0") == 0;
        }
    }
    CFRelease(charValue);
    if (!matches) return 0;

    CFTypeRef modifiersValue = NULL;
    if (AXUIElementCopyAttributeValue(element, kAXMenuItemCmdModifiersAttribute, &modifiersValue) == kAXErrorSuccess && modifiersValue) {
        int modifiers = 0;
        if (CFGetTypeID(modifiersValue) == CFNumberGetTypeID()) {
            CFNumberGetValue((CFNumberRef)modifiersValue, kCFNumberIntType, &modifiers);
        }
        CFRelease(modifiersValue);
        if ((modifiers & kAXMenuItemModifierNoCommand) != 0) return 0;
    }
    return 1;
}

static AXUIElementRef FindChromeZoomMenuItem(AXUIElementRef element, int direction, int depth) {
    if (!element || depth > 8) return NULL;
    if (ChromeZoomMenuItemMatches(element, direction)) {
        CFRetain(element);
        return element;
    }

    CFTypeRef childrenValue = NULL;
    if (AXUIElementCopyAttributeValue(element, kAXChildrenAttribute, &childrenValue) != kAXErrorSuccess || !childrenValue) {
        return NULL;
    }
    if (CFGetTypeID(childrenValue) != CFArrayGetTypeID()) {
        CFRelease(childrenValue);
        return NULL;
    }

    CFArrayRef children = (CFArrayRef)childrenValue;
    AXUIElementRef result = NULL;
    for (CFIndex i = 0; i < CFArrayGetCount(children) && !result; i++) {
        CFTypeRef child = CFArrayGetValueAtIndex(children, i);
        if (child && CFGetTypeID(child) == AXUIElementGetTypeID()) {
            result = FindChromeZoomMenuItem((AXUIElementRef)child, direction, depth + 1);
        }
    }
    CFRelease(childrenValue);
    return result;
}

// Presses Chrome's native zoom menu item without changing the frontmost app.
static int NativePressChromeZoomMenu_C(int pid, int direction) {
    if (pid <= 0 || direction < -1 || direction > 1) return -1;

    AXUIElementRef appRef = AXUIElementCreateApplication((pid_t)pid);
    if (!appRef) return -2;

    CFTypeRef menuBarValue = NULL;
    AXError menuErr = AXUIElementCopyAttributeValue(appRef, kAXMenuBarAttribute, &menuBarValue);
    CFRelease(appRef);
    if (menuErr != kAXErrorSuccess || !menuBarValue || CFGetTypeID(menuBarValue) != AXUIElementGetTypeID()) {
        if (menuBarValue) CFRelease(menuBarValue);
        return -3;
    }

    AXUIElementRef menuItem = FindChromeZoomMenuItem((AXUIElementRef)menuBarValue, direction, 0);
    CFRelease(menuBarValue);
    if (!menuItem) return -4;

    AXError actionErr = AXUIElementPerformAction(menuItem, kAXPressAction);
    CFRelease(menuItem);
    return actionErr == kAXErrorSuccess ? 1 : -5;
}

static void NativeGetWindowRect(unsigned long long hwnd, int *x, int *y, int *w, int *h, int *found) {
    *found = 0;
    if (hwnd == 0) return;

    AXUIElementRef winRef = (AXUIElementRef)hwnd;
    AXValueRef posVal = NULL;
    AXValueRef sizeVal = NULL;

    if (AXUIElementCopyAttributeValue(winRef, kAXPositionAttribute, (CFTypeRef *)&posVal) != kAXErrorSuccess || !posVal) {
        return;
    }
    if (AXUIElementCopyAttributeValue(winRef, kAXSizeAttribute, (CFTypeRef *)&sizeVal) != kAXErrorSuccess || !sizeVal) {
        CFRelease(posVal);
        return;
    }

    CGPoint pos = {0, 0};
    CGSize size = {0, 0};
    AXValueGetValue(posVal, kAXValueCGPointType, &pos);
    AXValueGetValue(sizeVal, kAXValueCGSizeType, &size);

    *x = (int)pos.x;
    *y = (int)pos.y;
    *w = (int)size.width;
    *h = (int)size.height;
    *found = 1;

    CFRelease(posVal);
    CFRelease(sizeVal);
}

// GetWindowsForPID_C enumerates windows for external process using AX API
static int GetWindowsForPID_C(int pid, CRectC* windows, int maxWindows) {
    pid_t targetPid = (pid_t)pid;
    AXUIElementRef appRef = AXUIElementCreateApplication(targetPid);
    if (!appRef) return 0;

    CFArrayRef windowList = NULL;
    AXError err = AXUIElementCopyAttributeValue(appRef, kAXWindowsAttribute, (CFTypeRef *)&windowList);

    int count = 0;
    if (err == kAXErrorSuccess && windowList) {
        CFIndex winCount = CFArrayGetCount(windowList);

        CFStringRef keys[] = { kAXPositionAttribute, kAXSizeAttribute, kAXTitleAttribute };
        CFArrayRef attrNames = CFArrayCreate(kCFAllocatorDefault, (const void **)keys, 3, &kCFTypeArrayCallBacks);

        for (CFIndex i = 0; i < winCount && count < maxWindows; i++) {
             AXUIElementRef winRef = (AXUIElementRef)CFArrayGetValueAtIndex(windowList, i);

             CFArrayRef values = NULL;
             AXError batchErr = AXUIElementCopyMultipleAttributeValues(winRef, attrNames, 0, &values);

             if (batchErr == kAXErrorSuccess && values && CFArrayGetCount(values) == 3) {
                 AXValueRef posVal = (AXValueRef)CFArrayGetValueAtIndex(values, 0);
                 CGPoint pos = {0,0};
                 if (CFGetTypeID(posVal) == AXValueGetTypeID()) {
                     AXValueGetValue(posVal, kAXValueCGPointType, &pos);
                 }

                 AXValueRef sizeVal = (AXValueRef)CFArrayGetValueAtIndex(values, 1);
                 CGSize size = {0,0};
                 if (CFGetTypeID(sizeVal) == AXValueGetTypeID()) {
                     AXValueGetValue(sizeVal, kAXValueCGSizeType, &size);
                 }

                 CFStringRef titleRef = (CFStringRef)CFArrayGetValueAtIndex(values, 2);
                 char titleBuf[256] = {0};
                 if (CFGetTypeID(titleRef) == CFStringGetTypeID()) {
                     CFStringGetCString(titleRef, titleBuf, 255, kCFStringEncodingUTF8);
                 }

                 windows[count].x = (int)pos.x;
                 windows[count].y = (int)pos.y;
                 windows[count].w = (int)size.width;
                 windows[count].h = (int)size.height;
                 snprintf(windows[count].title, 256, "%s", titleBuf);

                 CFRetain(winRef);
                 windows[count].id = (unsigned long long)winRef;

                 count++;
             }
             if (values) CFRelease(values);
        }
        if (attrNames) CFRelease(attrNames);
        CFRelease(windowList);
    }
    CFRelease(appRef);
    return count;
}

static int GetFocusedWindowForPID_C(int pid, CRectC *window) {
    pid_t targetPid = (pid_t)pid;
    AXUIElementRef appRef = AXUIElementCreateApplication(targetPid);
    if (!appRef) return 0;

    AXUIElementRef winRef = NULL;
    AXError err = AXUIElementCopyAttributeValue(appRef, kAXFocusedWindowAttribute, (CFTypeRef *)&winRef);
    CFRelease(appRef);
    if (err != kAXErrorSuccess || !winRef) return 0;

    CFStringRef keys[] = { kAXPositionAttribute, kAXSizeAttribute, kAXTitleAttribute };
    CFArrayRef attrNames = CFArrayCreate(kCFAllocatorDefault, (const void **)keys, 3, &kCFTypeArrayCallBacks);
    CFArrayRef values = NULL;
    AXError batchErr = AXUIElementCopyMultipleAttributeValues(winRef, attrNames, 0, &values);
    if (attrNames) CFRelease(attrNames);

    if (batchErr != kAXErrorSuccess || !values || CFArrayGetCount(values) != 3) {
        if (values) CFRelease(values);
        CFRelease(winRef);
        return 0;
    }

    CGPoint pos = {0, 0};
    CGSize size = {0, 0};
    AXValueRef posVal = (AXValueRef)CFArrayGetValueAtIndex(values, 0);
    AXValueRef sizeVal = (AXValueRef)CFArrayGetValueAtIndex(values, 1);
    if (CFGetTypeID(posVal) == AXValueGetTypeID()) {
        AXValueGetValue(posVal, kAXValueCGPointType, &pos);
    }
    if (CFGetTypeID(sizeVal) == AXValueGetTypeID()) {
        AXValueGetValue(sizeVal, kAXValueCGSizeType, &size);
    }

    char titleBuf[256] = {0};
    CFStringRef titleRef = (CFStringRef)CFArrayGetValueAtIndex(values, 2);
    if (CFGetTypeID(titleRef) == CFStringGetTypeID()) {
        CFStringGetCString(titleRef, titleBuf, 255, kCFStringEncodingUTF8);
    }

    window->id = (unsigned long long)winRef;
    window->x = (int)pos.x;
    window->y = (int)pos.y;
    window->w = (int)size.width;
    window->h = (int)size.height;
    snprintf(window->title, 256, "%s", titleBuf);
    CFRelease(values);
    return 1;
}

static void ReleaseAXWindow_C(unsigned long long hwnd) {
    if (hwnd != 0) {
        CFRelease((AXUIElementRef)hwnd);
    }
}

static int GetTopLevelWindowAtPoint_C(int pointX, int pointY, CRectC *window, int *pid) {
    AXUIElementRef systemWide = AXUIElementCreateSystemWide();
    if (!systemWide) return 0;

    AXUIElementRef element = NULL;
    AXError hitErr = AXUIElementCopyElementAtPosition(systemWide, (float)pointX, (float)pointY, &element);
    CFRelease(systemWide);
    if (hitErr != kAXErrorSuccess || !element) return 0;

    AXUIElementRef topLevel = NULL;
    AXError topErr = AXUIElementCopyAttributeValue(element, kAXTopLevelUIElementAttribute, (CFTypeRef *)&topLevel);
    if (topErr != kAXErrorSuccess || !topLevel) {
        topErr = AXUIElementCopyAttributeValue(element, kAXWindowAttribute, (CFTypeRef *)&topLevel);
    }
    if (topErr != kAXErrorSuccess || !topLevel) {
        topLevel = element;
        CFRetain(topLevel);
    }

    pid_t ownerPid = 0;
    if (AXUIElementGetPid(topLevel, &ownerPid) != kAXErrorSuccess || ownerPid <= 0) {
        CFRelease(topLevel);
        CFRelease(element);
        return 0;
    }

    AXValueRef posVal = NULL;
    AXValueRef sizeVal = NULL;
    if (AXUIElementCopyAttributeValue(topLevel, kAXPositionAttribute, (CFTypeRef *)&posVal) != kAXErrorSuccess || !posVal ||
        AXUIElementCopyAttributeValue(topLevel, kAXSizeAttribute, (CFTypeRef *)&sizeVal) != kAXErrorSuccess || !sizeVal) {
        if (posVal) CFRelease(posVal);
        if (sizeVal) CFRelease(sizeVal);
        CFRelease(topLevel);
        CFRelease(element);
        return 0;
    }

    CGPoint pos = {0, 0};
    CGSize size = {0, 0};
    AXValueGetValue(posVal, kAXValueCGPointType, &pos);
    AXValueGetValue(sizeVal, kAXValueCGSizeType, &size);

    window->id = 0;
    window->x = (int)pos.x;
    window->y = (int)pos.y;
    window->w = (int)size.width;
    window->h = (int)size.height;
    window->title[0] = '\0';
    *pid = (int)ownerPid;

    CFRelease(posVal);
    CFRelease(sizeVal);
    CFRelease(topLevel);
    CFRelease(element);
    return 1;
}

static int GetWebAreaAtPointForPID_C(int pid, int pointX, int pointY, CWebAreaC *area) {
    area->url[0] = '\0';
    AXUIElementRef systemWide = AXUIElementCreateSystemWide();
    if (!systemWide) return 0;

    AXUIElementRef current = NULL;
    AXError hitErr = AXUIElementCopyElementAtPosition(systemWide, (float)pointX, (float)pointY, &current);
    CFRelease(systemWide);
    if (hitErr != kAXErrorSuccess || !current) return 0;

    pid_t ownerPid = 0;
    if (AXUIElementGetPid(current, &ownerPid) != kAXErrorSuccess || ownerPid != (pid_t)pid) {
        CFRelease(current);
        return 0;
    }

    for (int depth = 0; depth < 24 && current; depth++) {
        CFStringRef role = NULL;
        AXError roleErr = AXUIElementCopyAttributeValue(current, kAXRoleAttribute, (CFTypeRef *)&role);
        int isWebArea = roleErr == kAXErrorSuccess && role &&
            CFStringCompare(role, CFSTR("AXWebArea"), 0) == kCFCompareEqualTo;
        if (role) CFRelease(role);

        if (isWebArea) {
            AXValueRef posVal = NULL;
            AXValueRef sizeVal = NULL;
            AXError posErr = AXUIElementCopyAttributeValue(current, kAXPositionAttribute, (CFTypeRef *)&posVal);
            AXError sizeErr = AXUIElementCopyAttributeValue(current, kAXSizeAttribute, (CFTypeRef *)&sizeVal);
            if (posErr == kAXErrorSuccess && sizeErr == kAXErrorSuccess && posVal && sizeVal) {
                CGPoint pos = CGPointZero;
                CGSize size = CGSizeZero;
                AXValueGetValue(posVal, kAXValueCGPointType, &pos);
                AXValueGetValue(sizeVal, kAXValueCGSizeType, &size);
                area->x = (int)pos.x;
                area->y = (int)pos.y;
                area->w = (int)size.width;
                area->h = (int)size.height;

                CFTypeRef urlValue = NULL;
                if (AXUIElementCopyAttributeValue(current, kAXURLAttribute, &urlValue) == kAXErrorSuccess && urlValue) {
                    CFStringRef urlString = NULL;
                    if (CFGetTypeID(urlValue) == CFURLGetTypeID()) {
                        urlString = CFURLGetString((CFURLRef)urlValue);
                    } else if (CFGetTypeID(urlValue) == CFStringGetTypeID()) {
                        urlString = (CFStringRef)urlValue;
                    }
                    if (urlString) {
                        CFStringGetCString(urlString, area->url, sizeof(area->url), kCFStringEncodingUTF8);
                    }
                    CFRelease(urlValue);
                }

                CFRelease(posVal);
                CFRelease(sizeVal);
                CFRelease(current);
                return area->w > 0 && area->h > 0 ? 1 : 0;
            }
            if (posVal) CFRelease(posVal);
            if (sizeVal) CFRelease(sizeVal);
        }

        AXUIElementRef parent = NULL;
        AXError parentErr = AXUIElementCopyAttributeValue(current, kAXParentAttribute, (CFTypeRef *)&parent);
        CFRelease(current);
        current = parentErr == kAXErrorSuccess ? parent : NULL;
    }

    if (current) CFRelease(current);
    return 0;
}

// Chrome extension popups can be visible without appearing in the
// accessibility window list. Fall back to the compositor window list to map
// a click in the popup's exposed area.
static int GetWindowRectAtPointForPID_C(int pid, int pointX, int pointY, int *x, int *y, int *w, int *h) {
    CFArrayRef windowList = CGWindowListCopyWindowInfo(kCGWindowListOptionOnScreenOnly, kCGNullWindowID);
    if (!windowList) return 0;

    CGRect bestRect = CGRectZero;
    int found = 0;
    CGPoint point = CGPointMake((CGFloat)pointX, (CGFloat)pointY);
    CFIndex count = CFArrayGetCount(windowList);

    for (CFIndex i = 0; i < count; i++) {
        CFDictionaryRef info = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);
        CFNumberRef ownerPIDValue = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowOwnerPID);
        CFDictionaryRef boundsValue = (CFDictionaryRef)CFDictionaryGetValue(info, kCGWindowBounds);
        if (!ownerPIDValue || !boundsValue) continue;

        int ownerPID = 0;
        if (!CFNumberGetValue(ownerPIDValue, kCFNumberIntType, &ownerPID) || ownerPID != pid) continue;

        int layer = 0;
        CFNumberRef layerValue = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowLayer);
        if (layerValue && CFNumberGetValue(layerValue, kCFNumberIntType, &layer) && layer != 0) continue;

        double alpha = 1.0;
        CFNumberRef alphaValue = (CFNumberRef)CFDictionaryGetValue(info, kCGWindowAlpha);
        if (alphaValue && CFNumberGetValue(alphaValue, kCFNumberDoubleType, &alpha) && alpha <= 0.01) continue;

        CGRect rect = CGRectZero;
        if (!CGRectMakeWithDictionaryRepresentation(boundsValue, &rect) || !CGRectContainsPoint(rect, point)) continue;
        if (rect.size.width <= 0 || rect.size.height <= 0) continue;
        bestRect = rect;
        found = 1;
        break;
    }

    CFRelease(windowList);
    if (!found) return 0;

    *x = (int)bestRect.origin.x;
    *y = (int)bestRect.origin.y;
    *w = (int)bestRect.size.width;
    *h = (int)bestRect.size.height;
    return 1;
}

*/
import "C"
import (
	"chromemanager/platform/common"
	"fmt"
	"os"
	"unsafe"
)

func CheckAccessibilityPermission(prompt bool) bool {
	cPrompt := C.int(0)
	if prompt {
		cPrompt = C.int(1)
	}
	return C.CheckAccessibilityPermission_C(cPrompt) != 0
}

func NativePressChromeZoomMenu(pid, direction int) error {
	result := int(C.NativePressChromeZoomMenu_C(C.int(pid), C.int(direction)))
	if result == 1 {
		return nil
	}
	return fmt.Errorf("Chrome zoom menu action failed for pid %d direction %d (code %d)", pid, direction, result)
}

func EnumWindowsForPID(pid int32) ([]common.WindowInfo, error) {
	if pid == int32(os.Getpid()) {
		rect, err := GetMainWindowPosition()
		if err != nil {
			return nil, err
		}
		return []common.WindowInfo{{
			Title:     "ChromeManager",
			ProcessID: pid,
			Position:  rect,
		}}, nil
	}

	maxWindows := 20
	cWindows := make([]C.CRectC, maxWindows)

	count := C.GetWindowsForPID_C(C.int(pid), (*C.CRectC)(unsafe.Pointer(&cWindows[0])), C.int(maxWindows))

	if count == 0 {
		return nil, nil
	}

	var results []common.WindowInfo
	for i := 0; i < int(count); i++ {
		cw := cWindows[i]
		w := common.WindowInfo{
			ProcessID: pid,
			HWND:      uintptr(cw.id),
			Position: common.Rect{
				Left:   int(cw.x),
				Top:    int(cw.y),
				Width:  int(cw.w),
				Height: int(cw.h),
			},
			Title: C.GoString(&cw.title[0]),
		}
		results = append(results, w)
	}
	return results, nil
}

func WindowRectAtPointForPID(pid int32, x, y int) (common.Rect, bool) {
	var left, top, width, height C.int
	if C.GetWindowRectAtPointForPID_C(C.int(pid), C.int(x), C.int(y), &left, &top, &width, &height) == 0 {
		return common.Rect{}, false
	}
	return common.Rect{
		Left:   int(left),
		Top:    int(top),
		Width:  int(width),
		Height: int(height),
	}, true
}

func FocusedWindowForPID(pid int32) (common.WindowInfo, bool) {
	var cWindow C.CRectC
	if C.GetFocusedWindowForPID_C(C.int(pid), &cWindow) == 0 {
		return common.WindowInfo{}, false
	}
	return common.WindowInfo{
		ProcessID: pid,
		HWND:      uintptr(cWindow.id),
		Position: common.Rect{
			Left:   int(cWindow.x),
			Top:    int(cWindow.y),
			Width:  int(cWindow.w),
			Height: int(cWindow.h),
		},
		Title: C.GoString(&cWindow.title[0]),
	}, true
}

func ReleaseAXWindow(hwnd uintptr) {
	if hwnd != 0 {
		C.ReleaseAXWindow_C(C.ulonglong(hwnd))
	}
}

func WindowAtPoint(x, y int) (common.WindowInfo, bool) {
	var cWindow C.CRectC
	var pid C.int
	if C.GetTopLevelWindowAtPoint_C(C.int(x), C.int(y), &cWindow, &pid) == 0 {
		return common.WindowInfo{}, false
	}
	return common.WindowInfo{
		ProcessID: int32(pid),
		Position: common.Rect{
			Left:   int(cWindow.x),
			Top:    int(cWindow.y),
			Width:  int(cWindow.w),
			Height: int(cWindow.h),
		},
	}, true
}

func WebAreaAtPointForPID(pid int32, x, y int) (string, common.Rect, bool) {
	var area C.CWebAreaC
	if C.GetWebAreaAtPointForPID_C(C.int(pid), C.int(x), C.int(y), &area) == 0 {
		return "", common.Rect{}, false
	}
	return C.GoString(&area.url[0]), common.Rect{
		Left:   int(area.x),
		Top:    int(area.y),
		Width:  int(area.w),
		Height: int(area.h),
	}, true
}

func GetMainWindowPosition() (common.Rect, error) {
	var x, y, w, h, found C.int
	C.GetMainWindowPosition(&x, &y, &w, &h, &found)
	if found == 0 {
		return common.Rect{}, fmt.Errorf("no valid main window found")
	}
	return common.Rect{
		Left:   int(x),
		Top:    int(y),
		Width:  int(w),
		Height: int(h),
	}, nil
}

func SetWindowPosition(pid int32, x, y, w, h int) {
	C.SetWindowPosition(C.int(x), C.int(y), C.int(w), C.int(h))
}

func BringWindowToTop(pid int32) {
	C.BringWindowToTop()
}

func NativeMinimize() {
	C.NativeMinimize()
}

func NativeTerminate() {
	C.NativeTerminate()
}

func NativeTerminatePID(pid int32) {
	C.NativeTerminatePID(C.int(pid))
}

func NativeCloseWindow(hwnd uintptr) {
	C.NativeCloseWindow(C.ulonglong(hwnd))
}

func NativeSetWindowPosition(hwnd uintptr, x, y, w, h int) {
	C.NativeSetWindowPosition(C.ulonglong(hwnd), C.int(x), C.int(y), C.int(w), C.int(h))
}

func NativeRaiseWindow(hwnd uintptr) {
	C.NativeRaiseWindow(C.ulonglong(hwnd))
}

func NativeActivateAndRaiseWindow(hwnd uintptr) {
	C.NativeActivateAndRaiseWindow(C.ulonglong(hwnd))
}

func NativeGetWindowRect(hwnd uintptr) (common.Rect, error) {
	var x, y, w, h, found C.int
	C.NativeGetWindowRect(C.ulonglong(hwnd), &x, &y, &w, &h, &found)
	if found == 0 {
		return common.Rect{}, fmt.Errorf("window rect not found")
	}
	return common.Rect{
		Left:   int(x),
		Top:    int(y),
		Width:  int(w),
		Height: int(h),
	}, nil
}

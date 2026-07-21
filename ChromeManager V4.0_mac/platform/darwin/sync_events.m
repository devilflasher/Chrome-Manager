#include <ApplicationServices/ApplicationServices.h>
#include <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>
#include <unistd.h>

// Manual declarations of exported Go functions
// Note: These must match the Go //export signature exactly in C terms.
extern int handleMouseEvent(int type, int x, int y, int data, int pid);
extern int handleKeyboardEvent(int type, int keyCode, int modifiers, int pid,
                               char *chars);

// Globals
static CFMachPortRef _eventTap = NULL;
static CFRunLoopSourceRef _runLoopSource = NULL;
static CFRunLoopRef _eventTapRunLoop = NULL;

int GetFrontmostPID_C() {
  NSRunningApplication *app =
      [[NSWorkspace sharedWorkspace] frontmostApplication];
  return [app processIdentifier];
}

int ActivateProcess_C(int pid) {
  if (pid <= 0)
    return 0;

  NSRunningApplication *app =
      [NSRunningApplication runningApplicationWithProcessIdentifier:(pid_t)pid];
  if (!app)
    return 0;

  [app activateWithOptions:NSApplicationActivateIgnoringOtherApps];

  AXUIElementRef appRef = AXUIElementCreateApplication((pid_t)pid);
  if (appRef) {
    AXUIElementRef focusedWindow = NULL;
    if (AXUIElementCopyAttributeValue(appRef, kAXFocusedWindowAttribute,
                                      (CFTypeRef *)&focusedWindow) ==
            kAXErrorSuccess &&
        focusedWindow) {
      AXUIElementPerformAction(focusedWindow, kAXRaiseAction);
      CFRelease(focusedWindow);
    } else {
      AXUIElementPerformAction(appRef, kAXRaiseAction);
    }
    CFRelease(appRef);
  }

  return 1;
}

// Event Tap Callback
CGEventRef eventTapCallback(CGEventTapProxy proxy, CGEventType type,
                            CGEventRef event, void *refcon) {
  if (type == kCGEventTapDisabledByTimeout ||
      type == kCGEventTapDisabledByUserInput) {
    printf("[Native CGO] Event tap disabled by system.\n");
    return event;
  }

  if (CGEventGetIntegerValueField(event, kCGEventSourceUserData) ==
      0xDEADBEEF) {
    return event;
  }

  pid_t pid = 0;
  NSRunningApplication *frontApp =
      [[NSWorkspace sharedWorkspace] frontmostApplication];
  if (frontApp) {
    pid = [frontApp processIdentifier];
  }

  CGPoint loc = CGEventGetLocation(event);

  switch (type) {
  case kCGEventLeftMouseDown:
  case kCGEventLeftMouseUp:
  case kCGEventRightMouseDown:
  case kCGEventRightMouseUp:
  case kCGEventLeftMouseDragged:
  case kCGEventRightMouseDragged: {
    handleMouseEvent((int)type, (int)loc.x, (int)loc.y, 0, (int)pid);
    break;
  }
  case kCGEventScrollWheel: {
    int64_t delta =
        CGEventGetIntegerValueField(event, kCGScrollWheelEventDeltaAxis1);
    if (handleMouseEvent((int)type, (int)loc.x, (int)loc.y, (int)delta,
                         (int)pid) == 1) {
      return NULL; // Consume native scroll! We will broadcast via CDP.
    }
    break;
  }
  case kCGEventKeyDown:
  case kCGEventKeyUp: {
    CGKeyCode keyCode =
        (CGKeyCode)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
    CGEventFlags flags = CGEventGetFlags(event);

    char buffer[16] = {0};
    if (type == kCGEventKeyDown) {
      UniChar uniStr[16];
      UniCharCount actualLength = 0;
      CGEventKeyboardGetUnicodeString(event, 16, &actualLength, uniStr);
      if (actualLength > 0) {
        NSString *s = [NSString stringWithCharacters:uniStr
                                              length:actualLength];
        const char *utf8 = [s UTF8String];
        if (utf8)
          strncpy(buffer, utf8, 15);
      }
    }

    if (handleKeyboardEvent((int)type, (int)keyCode, (int)flags, (int)pid,
                            buffer) == 1) {
      return NULL;
    }
    break;
  }
  case kCGEventFlagsChanged: {
    CGKeyCode keyCode =
        (CGKeyCode)CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode);
    CGEventFlags flags = CGEventGetFlags(event);
    if (handleKeyboardEvent((int)type, (int)keyCode, (int)flags, (int)pid,
                            (char *)"") == 1) {
      return NULL;
    }
    break;
  }
  default:
    break;
  }

  return event;
}

int StartEventTap() {
  if (_eventTap)
    return 1;

  CGEventMask eventMask =
      CGEventMaskBit(kCGEventLeftMouseDown) |
      CGEventMaskBit(kCGEventLeftMouseUp) |
      CGEventMaskBit(kCGEventRightMouseDown) |
      CGEventMaskBit(kCGEventRightMouseUp) |
      CGEventMaskBit(kCGEventLeftMouseDragged) |
      CGEventMaskBit(kCGEventRightMouseDragged) |
      CGEventMaskBit(kCGEventScrollWheel) | CGEventMaskBit(kCGEventKeyDown) |
      CGEventMaskBit(kCGEventKeyUp) | CGEventMaskBit(kCGEventFlagsChanged);

  _eventTap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap,
                               kCGEventTapOptionDefault, eventMask,
                               eventTapCallback, NULL);
  if (!_eventTap) {
    printf("[Native CGO] CGEventTapCreate failed! Missing Accessibility "
           "Permissions?\n");
    return 0;
  }
  printf("[Native CGO] CGEventTapCreate succeeded!\n");

  _runLoopSource =
      CFMachPortCreateRunLoopSource(kCFAllocatorDefault, _eventTap, 0);
  _eventTapRunLoop = CFRunLoopGetCurrent();
  CFRunLoopAddSource(_eventTapRunLoop, _runLoopSource, kCFRunLoopCommonModes);
  CGEventTapEnable(_eventTap, true);

  return 1;
}

void StopEventTap() {
  if (_eventTap) {
    CGEventTapEnable(_eventTap, false);

    if (_eventTapRunLoop) {
      CFRunLoopStop(_eventTapRunLoop);
      _eventTapRunLoop = NULL;
    }

    CFRunLoopRemoveSource(CFRunLoopGetCurrent(), _runLoopSource,
                          kCFRunLoopCommonModes);
    CFRelease(_runLoopSource);
    CFRelease(_eventTap);
    _eventTap = NULL;
    _runLoopSource = NULL;
  }
}

void RunEventLoop() { CFRunLoopRun(); }

static CGEventSourceRef GetEventSource() {
  static CGEventSourceRef source = NULL;
  if (!source) {
    source = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
  }
  return source;
}

static void PostEventToPidInternal(int pid, int type, int x, int y, int keyCode,
                                   int modifiers, int markIgnored) {
  if (type == kCGEventKeyDown || type == kCGEventKeyUp) {
    CGEventSourceRef source = GetEventSource();
    CGEventRef event = CGEventCreateKeyboardEvent(source, (CGKeyCode)keyCode,
                                                  (type == kCGEventKeyDown));
    if (event) {
      if (modifiers != 0)
        CGEventSetFlags(event, (CGEventFlags)modifiers);
      if (markIgnored)
        CGEventSetIntegerValueField(event, kCGEventSourceUserData, 0xDEADBEEF);
      CGEventPost(kCGSessionEventTap, event);
      CFRelease(event);
    }
    return;
  }

  // Capture current real mouse position to restore it
  CGEventRef currentLocEvent = CGEventCreate(NULL);
  CGPoint currentLoc = CGEventGetLocation(currentLocEvent);
  CFRelease(currentLocEvent);

  CGEventSourceRef source = GetEventSource();
  if (type == kCGEventScrollWheel) {
    int32_t delta = (int32_t)keyCode;
    CGEventRef event =
        CGEventCreateScrollWheelEvent(source, kCGScrollEventUnitLine, 1, delta);
    if (event) {
      if (markIgnored)
        CGEventSetIntegerValueField(event, kCGEventSourceUserData, 0xDEADBEEF);
      CGEventSetLocation(event, CGPointMake((CGFloat)x, (CGFloat)y));
      CGEventPost(kCGSessionEventTap, event);
      CFRelease(event);

      // Quickly restore mouse
      CGWarpMouseCursorPosition(currentLoc);
    }
    return;
  }

  CGPoint loc = CGPointMake((CGFloat)x, (CGFloat)y);
  if (type == kCGEventLeftMouseDown || type == kCGEventLeftMouseUp ||
      type == kCGEventRightMouseDown || type == kCGEventRightMouseUp ||
      type == kCGEventMouseMoved || type == kCGEventLeftMouseDragged ||
      type == kCGEventRightMouseDragged) {

    CGEventRef event = CGEventCreateMouseEvent(source, (CGEventType)type, loc,
                                               kCGMouseButtonLeft);
    if (type == kCGEventRightMouseDown || type == kCGEventRightMouseUp ||
        type == kCGEventRightMouseDragged) {
      CGEventSetType(event, (CGEventType)type);
      CGEventSetIntegerValueField(event, kCGMouseEventButtonNumber,
                                  kCGMouseButtonRight);
    }

    if (event) {
      if (markIgnored)
        CGEventSetIntegerValueField(event, kCGEventSourceUserData, 0xDEADBEEF);
      CGEventSetLocation(event, CGPointMake((CGFloat)x, (CGFloat)y));
      CGEventPost(kCGSessionEventTap, event);
      CFRelease(event);

      // Quickly restore mouse location
      CGWarpMouseCursorPosition(currentLoc);
    }
    return;
  }
}

void PostEventToPid(int pid, int type, int x, int y, int keyCode,
                    int modifiers) {
  PostEventToPidInternal(pid, type, x, y, keyCode, modifiers, 1);
}

void PostEventToPidCaptured(int pid, int type, int x, int y, int keyCode,
                            int modifiers) {
  PostEventToPidInternal(pid, type, x, y, keyCode, modifiers, 0);
}

void PostKeyEventToPid(int pid, int type, int keyCode, int modifiers) {
  if (pid <= 0)
    return;
  if (type != kCGEventKeyDown && type != kCGEventKeyUp)
    return;

  CGEventSourceRef source = GetEventSource();
  CGEventRef event = CGEventCreateKeyboardEvent(source, (CGKeyCode)keyCode,
                                                (type == kCGEventKeyDown));
  if (event) {
    if (modifiers != 0)
      CGEventSetFlags(event, (CGEventFlags)modifiers);
    CGEventSetIntegerValueField(event, kCGEventSourceUserData, 0xDEADBEEF);
    CGEventPostToPid((pid_t)pid, event);
    CFRelease(event);
  }
}

void PostKeyEventGlobal(int type, int keyCode, int modifiers) {
  if (type != kCGEventKeyDown && type != kCGEventKeyUp)
    return;

  CGEventSourceRef source = GetEventSource();
  CGEventRef event = CGEventCreateKeyboardEvent(source, (CGKeyCode)keyCode,
                                                (type == kCGEventKeyDown));
  if (event) {
    if (modifiers != 0)
      CGEventSetFlags(event, (CGEventFlags)modifiers);
    CGEventSetIntegerValueField(event, kCGEventSourceUserData, 0xDEADBEEF);
    CGEventPost(kCGSessionEventTap, event);
    CFRelease(event);
  }
}

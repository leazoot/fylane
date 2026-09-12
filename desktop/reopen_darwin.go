package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdio.h>

// A click on the Dock tile while every window is hidden reaches the app
// delegate as applicationShouldHandleReopen:hasVisibleWindows:. Wails'
// delegate does not implement it, so the click does nothing and the only way
// back to a window closed with the red button or ⌘W is the tray menu. This
// adds the method to that delegate at runtime and brings its main window
// back, which is what every other Dock app does.
static BOOL fylaneReopen(id self, SEL _cmd, NSApplication *sender, BOOL hasVisibleWindows) {
	if (!hasVisibleWindows) {
		NSWindow *w = [self valueForKey:@"mainWindow"];
		[w makeKeyAndOrderFront:nil];
		[NSApp activateIgnoringOtherApps:YES];
	}
	return YES;
}

static void fylaneInstallReopen(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		id delegate = [NSApp delegate];
		if (delegate == nil) {
			return;
		}
		char types[32];
		snprintf(types, sizeof types, "%s@:@%s", @encode(BOOL), @encode(BOOL));
		class_addMethod(object_getClass(delegate),
			@selector(applicationShouldHandleReopen:hasVisibleWindows:),
			(IMP)fylaneReopen, types);
	});
}
*/
import "C"

// installDockReopen makes a Dock click reopen the hidden window.
func installDockReopen() { C.fylaneInstallReopen() }

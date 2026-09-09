package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static int fylaneDockHidden(void) {
	return [NSApp activationPolicy] == NSApplicationActivationPolicyAccessory;
}

// Runs on the main thread: AppKit refuses activation-policy changes from
// anywhere else, and Wails calls bound methods off it.
static void fylaneSetDockHidden(int hidden) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSApplicationActivationPolicy policy = hidden
			? NSApplicationActivationPolicyAccessory
			: NSApplicationActivationPolicyRegular;
		[NSApp setActivationPolicy:policy];
		if (!hidden) {
			// Coming back to the Dock does not by itself bring the app
			// forward; without this the icon appears and nothing else moves.
			[NSApp activateIgnoringOtherApps:YES];
		}
	});
}
*/
import "C"

// dockSupported reports whether this platform has a Dock icon to hide.
// macOS does: an "accessory" application keeps its windows and its menu-bar
// item and loses only the Dock tile. The tray menu's "Open Fylane" is the
// way back to the window while hidden, and it exists on every platform.
const dockSupported = true

func applyDockHidden(hidden bool) { C.fylaneSetDockHidden(boolToC(hidden)) }

func boolToC(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

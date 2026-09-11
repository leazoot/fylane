package main

import (
	_ "embed"
	goruntime "runtime"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// trayIcon is a macOS template image: one colour plus alpha, 44px for the
// 22pt menu bar at 2x. The system tints it — black in a light menu bar, white
// in a dark one — which is why the file carries no colour of its own. The
// letter's counter is knocked out rather than filled, or the shape would read
// as a blob at this size.
//
//go:embed trayicon.png
var trayIcon []byte

// trayIconWindows is the same mark as a colour .ico: the Windows tray loads
// icons through LoadImage(IMAGE_ICON), which does not read PNG at all — a
// template PNG there yields a tooltip with nothing under it. Colour rather
// than a silhouette because Windows does not tint tray icons either.
//
//go:embed trayicon.ico
var trayIconWindows []byte

// startTray puts Fylane in the system tray using the spike-verified
// external-loop pattern and returns a stop function. The tray lives in the
// UI shell; the Core keeps serving even if the shell (and its tray) dies.
func startTray(app *App) func() {
	start, stop := systray.RunWithExternalLoop(func() {
		// Both arguments are the same file: the template is what macOS
		// wants, and on the platforms that ignore templates a one-colour
		// icon with alpha is still the right thing to draw.
		if goruntime.GOOS == "windows" {
			systray.SetIcon(trayIconWindows)
		} else {
			systray.SetTemplateIcon(trayIcon, trayIcon)
		}
		systray.SetTooltip("Fylane Companion")
		open := systray.AddMenuItem("Open Fylane", "Show the Fylane window")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit Fylane UI", "Close the UI shell (the Core keeps running)")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					if app.ctx != nil {
						runtime.WindowShow(app.ctx)
					}
				case <-quit.ClickedCh:
					if app.ctx != nil {
						runtime.Quit(app.ctx)
					}
				}
			}
		}()
	}, func() {})
	start()
	return stop
}

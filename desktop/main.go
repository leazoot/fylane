// Fylane desktop UI shell. Thin, restartable process: the resident Core
// (fylane-companion) owns the tunnel, MCP server, and approvals; this shell
// only renders state and forwards decisions over the local control API (
// architecture). Closing the window hides it; the app stays in the tray.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	// The tray must start before wails.Run and drive its own loop
	// (spike-verified pattern: systray.RunWithExternalLoop on the main
	// thread ahead of the Wails runtime).
	stopTray := startTray(app)
	defer stopTray()

	err := wails.Run(&options.App{
		Title: "Fylane",
		// Desktop v2 §2 states 1224×792, which is more window than this app
		// needs on a laptop screen — every page here is a fluid column, not a
		// canvas that grows with the frame. The minimum below is the design's
		// own crowding threshold; this default sits comfortably above it.
		// Recorded deviation.
		Width:             960,
		Height:            660,
		MinWidth:          860,
		MinHeight:         620,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 0xF5, G: 0xF2, B: 0xEB, A: 1},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		Mac: &mac.Options{
			// Unified titlebar: no bar, no divider, no centred window
			// title. The traffic lights float over the content on the same
			// baseline as the app's first row of navigation, which is why
			// the content reserves 86px on the left.
			TitleBar: mac.TitleBarHiddenInset(),
			About:    &mac.AboutInfo{Title: "Fylane"},
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

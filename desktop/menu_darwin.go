package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// appMenu is the menu bar. On macOS every keyboard shortcut is a menu item,
// so a window with no "Close" item has no ⌘W — and the Window menu Wails
// installs by default carries only Minimize, Zoom and Full Screen. This one
// adds Close, and it does what the red button does: hide, not quit. The Edit
// menu stays because it is where ⌘C / ⌘V / ⌘A come from.
func appMenu(ctx func() context.Context) *menu.Menu {
	window := menu.NewMenu()
	window.Append(menu.Text("Close Window", keys.CmdOrCtrl("w"), func(*menu.CallbackData) {
		if c := ctx(); c != nil {
			runtime.WindowHide(c)
		}
	}))
	window.Append(menu.Text("Minimize", keys.CmdOrCtrl("m"), func(*menu.CallbackData) {
		if c := ctx(); c != nil {
			runtime.WindowMinimise(c)
		}
	}))
	m := menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu())
	m.Append(menu.SubMenu("Window", window))
	return m
}

//go:build !darwin

package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/menu"
)

// appMenu is nil off macOS: the shell draws its own title bar there and
// Windows has no menu-bar convention this app needs.
func appMenu(func() context.Context) *menu.Menu { return nil }

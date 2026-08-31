package main

import (
	"embed"

	"cloudix/backend/app"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	backendApp := app.NewApp()

	err := wails.Run(&options.App{
		Title:            "Cloudix",
		Width:            1180,
		Height:           760,
		MinWidth:         900,
		MinHeight:        600,
		Frameless:        false,
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		Assets:           assets,
		OnStartup:        backendApp.OnStartup,
		OnBeforeClose:    backendApp.OnBeforeClose,
		Bind:             []interface{}{backendApp},
		Mac: &mac.Options{
			WindowIsTranslucent:  true,
			WebviewIsTransparent: true,
			TitleBar:             mac.TitleBarHiddenInset(),
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}

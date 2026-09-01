package main

import (
	"embed"
	goruntime "runtime"

	"cloudix/backend/app"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	backendApp := app.NewApp()

	// Windows draws a chrome that clashes with the app's own look, so we go
	// frameless there and render our own title bar (see WindowsTitlebar in the
	// frontend). macOS keeps the native traffic lights via TitleBarHiddenInset —
	// that chrome already fits the design.
	frameless := goruntime.GOOS == "windows"

	err := wails.Run(&options.App{
		Title:            "Cloudix",
		Width:            1340,
		Height:           880,
		MinWidth:         620,
		MinHeight:        460,
		Frameless:        frameless,
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

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
	host := &desktopHost{}
	backendApp := app.NewApp(host)

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
		// Passed by reference, not wrapped in a closure: Wails only recognises
		// a lifecycle hook it can identify, and anything it does not recognise
		// it exposes to the frontend as a callable binding instead.
		OnStartup:     backendApp.OnStartup,
		OnBeforeClose: backendApp.OnBeforeClose,
		Bind:          []interface{}{backendApp},
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

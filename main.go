package main

import (
	"embed"
	"log"
	"os"
	"slices"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/cursor"
	"github.com/sid/harness/internal/app"
	"github.com/sid/harness/internal/host"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Wails uses Go's `embed` package to embed the frontend files into the binary.
// Any files in the frontend/dist folder will be embedded into the binary and
// made available to the frontend.
// See https://pkg.go.dev/embed for more information.

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Provider registry for this process, including the detached host mode.
	// Claude is pre-registered by internal/host; Cursor is registered here.
	host.RegisterProvider(cursor.ProviderID, func() agent.Provider { return &cursor.Provider{} })

	// Detached run host mode (spec 10): no GUI.
	if slices.Contains(os.Args, "--agent-host") {
		if err := host.Main(os.Args); err != nil {
			log.Fatal(err)
		}
		return
	}

	app.ApplyLoginPath()

	a, err := app.New(app.Options{})
	if err != nil {
		log.Fatal(err)
	}
	defer a.Shutdown()

	wapp := application.New(application.Options{
		Name:        "Harness",
		Description: "A lighter agent window",
		Services:    a.Services(),
		ShouldQuit:  a.ShouldQuit,
		OnShutdown:  a.Shutdown,
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	a.Attach(wapp)

	win := wapp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Harness",
		Width:  1400,
		Height: 900,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(20, 20, 20),
		URL:              "/",
	})

	// Closing the window with shells running asks the frontend first.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if !a.QuitAllowed() {
			e.Cancel()
			a.RequestQuit()
		}
	})

	if err := wapp.Run(); err != nil {
		log.Fatal(err)
	}
}

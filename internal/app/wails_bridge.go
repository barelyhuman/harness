package app

// wailsBridge is implemented on macOS where the Wails GUI is linked in.
type wailsBridge interface {
	Quit()
	EmitEvent(name string, data any)
}

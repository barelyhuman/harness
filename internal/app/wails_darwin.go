//go:build darwin

package app

import "github.com/wailsapp/wails/v3/pkg/application"

type wailsAdapter struct{ w *application.App }

func (u *wailsAdapter) Quit()                        { u.w.Quit() }
func (u *wailsAdapter) EmitEvent(name string, d any) { u.w.Event.Emit(name, d) }

// Attach gives the App the Wails application (for Quit and events).
func (a *App) Attach(w *application.App) { a.wapp = &wailsAdapter{w} }

// Services returns the Wails service registrations.
func (a *App) Services() []application.Service {
	return []application.Service{
		application.NewService(a.BusSvc),
		application.NewService(a.ProjectSvc),
		application.NewService(a.SessionSvc),
		application.NewService(a.FSSvc),
		application.NewService(a.SearchSvc),
		application.NewService(a.GitSvc),
		application.NewService(a.TermSvc),
		application.NewService(a.UIStateSvc),
		application.NewService(a.SettingsSvc),
		application.NewService(a.AppSvc),
		application.NewService(a.LSPSvc),
	}
}

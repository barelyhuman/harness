package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sid/harness/internal/agent"
	"github.com/sid/harness/internal/agent/claude"
	"github.com/sid/harness/internal/agent/cursor"
	"github.com/sid/harness/internal/fs"
	"github.com/sid/harness/internal/lsp"
	"github.com/sid/harness/internal/pty"
	"github.com/sid/harness/internal/store"
	"github.com/sid/harness/internal/stream"
	"github.com/sid/harness/internal/termlayout"
	"github.com/sid/harness/internal/workspace"
)

// Options override the default locations (tests).
type Options struct {
	DataDir   string // default ~/Library/Application Support/Harness
	ConfigDir string // default ~/.config/harness
	// SkipModelRefresh disables the background provider model probe (tests).
	SkipModelRefresh bool
	// SkipRecover disables re-attaching to live agent hosts at startup (tests).
	SkipRecover bool
}

// App owns every long-lived component and the Wails services built on them.
// main.go builds it with New, registers Services(), wires Attach and calls
// Shutdown on exit.
type App struct {
	Bus     *stream.Bus
	Store   *store.Store
	Pty     *pty.Manager
	Layouts *termlayout.Manager
	Watcher *fs.Watcher
	LSP     *lsp.Manager
	Models  *agent.ModelRegistry
	Sup     *supervisor

	BusSvc      *BusService
	ProjectSvc  *ProjectService
	SessionSvc  *SessionService
	FSSvc       *FSService
	SearchSvc   *SearchService
	GitSvc      *GitService
	TermSvc     *TermService
	UIStateSvc  *UIStateService
	SettingsSvc *SettingsService
	AppSvc      *AppService
	LSPSvc      *LSPService

	appCh *stream.Channel // `app` channel: quit requests

	wapp     wailsBridge
	quitOK   atomic.Bool
	lastQuit atomic.Int64
	once     sync.Once
}

// DefaultDataDir is ~/Library/Application Support/Harness.
func DefaultDataDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "Library", "Application Support", "Harness"), nil
}

// DefaultConfigDir is ~/.config/harness.
func DefaultConfigDir() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "harness"), nil
}

// New creates the data dir, opens the store, starts the bus and builds the
// managers and services.
func New(o Options) (*App, error) {
	var err error
	if o.DataDir == "" {
		if o.DataDir, err = DefaultDataDir(); err != nil {
			return nil, err
		}
	}
	if o.ConfigDir == "" {
		if o.ConfigDir, err = DefaultConfigDir(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	st, err := store.Open(filepath.Join(o.DataDir, "harness.db"))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	bus := stream.New()
	if err := bus.Start(); err != nil {
		st.Close()
		return nil, fmt.Errorf("bus: %w", err)
	}
	a := &App{Bus: bus, Store: st, Pty: pty.NewManager(), Layouts: termlayout.NewManager(), Watcher: fs.NewWatcher()}
	a.Pty.Register(bus)
	a.LSP = lsp.NewManager(lsp.Options{})
	a.LSP.Register(bus)
	hub := newScopeHub(bus, a.Watcher)
	projects := workspace.NewProjects(st.DB)
	sessions := workspace.NewSessions(st.DB)

	a.BusSvc = NewBusService(bus)
	a.ProjectSvc = &ProjectService{projects: projects, db: st.DB}
	out := newBusSink(bus)
	a.LSP.Subscribe(func(st []lsp.ServerStatus) { out.Send("lsp", st) })
	a.LSPSvc = &LSPService{m: a.LSP}
	out.Ensure("sessions")
	out.Ensure("lsp")
	a.Models = agent.NewModelRegistry()
	a.Models.SetProviderModels(claude.ProviderID, claude.StaticModels())
	// Cursor provider registered here (models) and in main.go (host process).
	a.Models.SetProviderModels(cursor.ProviderID, cursor.StaticModels())
	a.Sup = newSupervisor(o.DataDir, st.DB, sessions, projects, out, a.LSP.Touch)
	a.SessionSvc = &SessionService{sessions: sessions, projects: projects, db: st.DB, sup: a.Sup, models: a.Models}
	a.FSSvc = &FSService{hub: hub}
	a.SearchSvc = &SearchService{bus: bus, greps: map[string]context.CancelFunc{}}
	a.GitSvc = &GitService{hub: hub}
	a.TermSvc = newTermService(bus, a.Pty, a.Layouts, st, LoginEnv)
	a.UIStateSvc = &UIStateService{ui: &uiStore{db: st.DB}}
	a.AppSvc = &AppService{app: a}
	a.SessionSvc.term = a.TermSvc
	a.appCh = bus.Open("app")
	if a.SettingsSvc, err = newSettingsService(bus, a.Watcher, o.ConfigDir); err != nil {
		a.Shutdown()
		return nil, fmt.Errorf("settings: %w", err)
	}
	a.SessionSvc.settings = a.SettingsSvc
	a.Sup.roles = a.SessionSvc.agentRoles
	if !o.SkipRecover {
		a.Sup.wg.Add(1)
		go func() { defer a.Sup.wg.Done(); a.Sup.Recover() }()
	}
	if !o.SkipModelRefresh {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = a.Models.Refresh(ctx, &claude.Provider{}, &cursor.Provider{})
		}()
	}
	return a, nil
}

// ServiceCount is the number of Wails-registered services (stable across platforms).
func (a *App) ServiceCount() int { return 11 }

// Shutdown flushes pending layouts, kills all PTYs (agent hosts are not ours
// to kill), stops the watchers, then closes the store and the bus. Idempotent.
func (a *App) Shutdown() {
	a.once.Do(func() {
		if a.TermSvc != nil {
			a.TermSvc.Flush()
		}
		if a.Sup != nil {
			a.Sup.Close()
		}
		if a.LSP != nil {
			a.LSP.Close()
		}
		a.Pty.KillAll()
		a.Watcher.Close()
		a.Store.Close()
		a.Bus.Close()
	})
}

// ---- quit handling ----

// QuitAllowed reports whether the app may quit now: the user confirmed, no
// shell would be killed, or a second quit request arrived within 3s.
func (a *App) QuitAllowed() bool {
	if a.quitOK.Load() || a.TermSvc.ShellsRunning() == 0 {
		return true
	}
	now := time.Now().UnixMilli()
	prev := a.lastQuit.Swap(now)
	return prev != 0 && now-prev < 3000
}

// RequestQuit asks the frontend to show the quit sheet: bus channel `app`
// {type:"quit-request", shells:N} and Wails event "quit-requested" (data N).
func (a *App) RequestQuit() {
	n := a.TermSvc.ShellsRunning()
	_ = a.appCh.SendJSON(map[string]any{"type": "quit-request", "shells": n})
	if a.wapp != nil {
		a.wapp.EmitEvent("quit-requested", n)
	}
}

// ShouldQuit is application.Options.ShouldQuit: it vetoes the native quit
// (Cmd+Q) until the frontend confirms through AppService.ConfirmQuit.
func (a *App) ShouldQuit() bool {
	if a.QuitAllowed() {
		return true
	}
	a.RequestQuit()
	return false
}

// AppService exposes process-level intents.
type AppService struct{ app *App }

// ConfirmQuit is called by the frontend's quit sheet; it quits for real
// (shells are killed on shutdown, agent hosts keep running).
func (s *AppService) ConfirmQuit() {
	s.app.quitOK.Store(true)
	if s.app.wapp != nil {
		s.app.wapp.Quit()
	}
}

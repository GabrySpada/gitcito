// Gitcito: a Git client. This is the Wails entry point; the domain logic lives
// under internal/, the renderer under src/renderer.
package main

import (
	"context"
	"embed"
	"flag"
	"log"
	"log/slog"
	"os"

	"github.com/GabrySpada/gitcito/internal/app"
	"github.com/GabrySpada/gitcito/internal/gitsvc"
	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/hosting"
	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/GabrySpada/gitcito/internal/logbook"
	"github.com/GabrySpada/gitcito/internal/menu"
	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/GabrySpada/gitcito/internal/settings"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend-dist
var assets embed.FS

func main() {
	// --shot: screenshot automation. Gates the renderer's store bridge and the
	// GITCITO_SSH_DIR override; never set in normal use.
	fs := flag.NewFlagSet("gitcito", flag.ContinueOnError)
	shot := fs.Bool("shot", false, "screenshot automation mode")
	// Unknown flags belong to the CLI hand-off (`--open`, `--wait`, …), parsed
	// separately; flag errors must not stop the app.
	fs.SetOutput(nopWriter{})
	_ = fs.Parse(os.Args[1:])

	// GUI launches inherit a minimal PATH; restore the login-shell PATH so
	// spawned git (and its hooks, e.g. husky → npm) find node/npm. Runs before
	// any spawn.
	gitx.FixPath(context.Background())

	core := app.NewCore(*shot, paths.IsDev())

	dataDir, err := paths.EnsureDataDir()
	if err != nil {
		log.Fatalf("data directory: %v", err)
	}
	book := logbook.New(dataDir)
	// One keychain for the whole app: settings, the vault and token lookups share
	// the same consent and the same in-flight question.
	keys := keychain.New(keychain.Options{Emitter: app.Emitter{}, HasWindow: app.HasWindow})
	store := settings.New(settings.Options{Keychain: keys, Dialogs: settings.WailsDialogs{}})
	git := gitsvc.New(app.Emitter{}, book, gitsvc.WithTokenFunc(func(ctx context.Context, host, repoPath string) string {
		// A lookup failure (keychain refused, unreadable secrets) means "no token": git then runs
		// unauthenticated and the remote's own error reaches the user, as in the Electron build.
		tok, _ := store.ActiveProfileToken(ctx, settings.RepoHost(host), repoPath)
		return tok
	}))

	var single *application.SingleInstanceOptions
	if !app.SingleInstanceAvailable() {
		slog.Warn("single instance disabled: no D-Bus session bus")
	} else if key, err := app.InstanceKey(); err == nil {
		single = &application.SingleInstanceOptions{
			UniqueID:      app.UniqueID(),
			EncryptionKey: key,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				a := application.Get()
				if a == nil {
					return
				}
				app.Focus(a)
				core.HandleSecondInstance(data.Args)
			},
		}
	} else {
		slog.Warn("single instance disabled", "err", err)
	}

	a := application.New(application.Options{
		Name:        "Gitcito",
		Description: "A lightweight Git client",
		// Never logs bound-call arguments or results: they carry tokens.
		Logger: app.SystemLogger(os.Stderr),
		Services: []application.Service{
			application.NewService(app.NewAppService(core)),
			application.NewService(&app.ShellService{}),
			application.NewService(&app.DialogService{}),
			application.NewService(&menu.Service{}),
			application.NewService(logbook.NewService(book)),
			application.NewService(keychain.NewService(keys)),
			application.NewService(settings.NewService(store)),
			application.NewService(git),
			application.NewService(hosting.NewService(hosting.Options{OpenURL: func(u string) error {
				return application.Get().Browser.OpenURL(u)
			}})),
		},
		Assets:         application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		SingleInstance: single,
		// Used only by the `server` build tag (automation): loopback only, port from WAILS_SERVER_PORT.
		Server: application.ServerOptions{Host: "127.0.0.1"},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
	})

	a.OnShutdown(core.ReleaseAllWaits)
	a.OnShutdown(func() {
		if err := book.Flush(); err != nil {
			slog.Error("log flush", "err", err)
		}
	})
	win := app.NewMainWindow(a)
	// The window going away mid-question must not leave keychain callers hanging.
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) { keys.Cancel() })
	core.HandleColdLaunch(os.Args)

	if err := a.Run(); err != nil {
		log.Fatal(err)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

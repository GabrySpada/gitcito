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
	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/logbook"
	"github.com/GabrySpada/gitcito/internal/menu"
	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/wailsapp/wails/v3/pkg/application"
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
		Services: []application.Service{
			application.NewService(app.NewAppService(core)),
			application.NewService(&app.ShellService{}),
			application.NewService(&app.DialogService{}),
			application.NewService(&menu.Service{}),
			application.NewService(logbook.NewService(book)),
		},
		Assets:         application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		SingleInstance: single,
		Mac:            application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: false},
	})

	a.OnShutdown(core.ReleaseAllWaits)
	a.OnShutdown(func() {
		if err := book.Flush(); err != nil {
			slog.Error("log flush", "err", err)
		}
	})
	app.NewMainWindow(a)
	core.HandleColdLaunch(os.Args)

	if err := a.Run(); err != nil {
		log.Fatal(err)
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

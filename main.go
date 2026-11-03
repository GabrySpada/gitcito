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
	"path/filepath"

	"github.com/GabrySpada/gitcito/internal/ai"
	"github.com/GabrySpada/gitcito/internal/analytics"
	"github.com/GabrySpada/gitcito/internal/analyze"
	"github.com/GabrySpada/gitcito/internal/app"
	"github.com/GabrySpada/gitcito/internal/devices"
	"github.com/GabrySpada/gitcito/internal/gitsvc"
	"github.com/GabrySpada/gitcito/internal/gitx"
	"github.com/GabrySpada/gitcito/internal/grounding/aicompleter"
	"github.com/GabrySpada/gitcito/internal/hosting"
	"github.com/GabrySpada/gitcito/internal/info"
	"github.com/GabrySpada/gitcito/internal/keychain"
	"github.com/GabrySpada/gitcito/internal/launch"
	"github.com/GabrySpada/gitcito/internal/localci"
	"github.com/GabrySpada/gitcito/internal/logbook"
	"github.com/GabrySpada/gitcito/internal/menu"
	"github.com/GabrySpada/gitcito/internal/paths"
	"github.com/GabrySpada/gitcito/internal/repochat"
	"github.com/GabrySpada/gitcito/internal/secure"
	"github.com/GabrySpada/gitcito/internal/settings"
	"github.com/GabrySpada/gitcito/internal/terminal"
	"github.com/GabrySpada/gitcito/internal/todoscan"
	"github.com/GabrySpada/gitcito/internal/vault"
	"github.com/GabrySpada/gitcito/internal/wiki"
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
	// TS skipped the pre-destruction snapshot when settings.snapshotGuard === false.
	gitsvc.SetSnapshotGuardSetting(func() bool {
		v, ok := store.ReadSettings(context.Background()).Bool("snapshotGuard")
		return !ok || v
	})
	vaultStore := vault.New(vault.Options{Keychain: keys})
	ledger := analytics.New(analytics.Options{})
	// One model client for every AI feature (and, later, repo chat and the wiki).
	aiClient := ai.NewClient(ai.Options{Keys: store, Usage: app.AIUsage{Ledger: ledger}})
	git := gitsvc.New(app.Emitter{}, book, gitsvc.WithActivityRecorder(ledger), gitsvc.WithTokenFunc(func(ctx context.Context, host, repoPath string) string {
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

	// Repo chat and the wiki talk to the model through the same client as every other AI feature.
	model := aicompleter.New(aiClient)
	wikiStore := filepath.Join(dataDir, wiki.FileName)
	openURL := func(u string) error {
		if a := application.Get(); a != nil {
			return a.Browser.OpenURL(u)
		}
		return nil
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
			application.NewService(vault.NewService(vaultStore)),
			application.NewService(secure.NewService(secure.New(secure.Options{Vault: vaultStore}))),
			application.NewService(analytics.NewService(ledger)),
			application.NewService(ai.NewService(aiClient)),
			application.NewService(repochat.NewService(git, model, aicompleter.Schemas{})),
			application.NewService(wiki.NewService(git, model, app.Emitter{}, wikiStore)),
			application.NewService(terminal.NewService(app.Emitter{})),
			application.NewService(launch.NewService(app.Emitter{}, openURL)),
			application.NewService(devices.NewService(*shot)),
			application.NewService(localci.NewService(app.Emitter{})),
			application.NewService(analyze.NewService()),
			application.NewService(todoscan.NewService()),
			application.NewService(info.NewService(info.New(info.Options{}))),
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
		if err := ledger.Flush(); err != nil {
			slog.Error("analytics flush", "err", err)
		}
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

// Command proxier is the whole of Proxier in one binary (ADR 0001):
//
//	proxier serve                     the HTTP server, the scheduler and every worker pool
//	proxier manage <command> [args]   one-off admin commands, run in the container's shell
//	proxier version
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	_ "time/tzdata" // the image has no zoneinfo

	"github.com/tikhonp/proxier/internal/platform"
	"github.com/tikhonp/proxier/internal/platform/config"
	"github.com/tikhonp/proxier/internal/platform/db"
	"github.com/tikhonp/proxier/internal/platform/httpx"
	"github.com/tikhonp/proxier/internal/platform/module"
	"github.com/tikhonp/proxier/internal/platform/obs"
)

// modules are the enabled modules, in migration order. Removing one here
// removes its pages, jobs and tables from use (ADR 0002).
func modules() []module.Module {
	return nil
}

const usage = `Usage:
  proxier serve
  proxier manage migrate up               apply pending migrations of every module
  proxier manage migrate down <module>    roll back the last migration of one module
  proxier manage migrate status           list every module's migrations
  proxier manage create-admin             create the admin (asks for a username and a password)
  proxier manage reset-password           set a new admin password and end every session
  proxier version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, usage)
		return 2
	}
	switch args[0] {
	case "serve":
		return serve()
	case "manage":
		return manage(args[1:], stdout, stderr)
	case "version", "-v", "--version":
		sayln(stdout, obs.AppVersion)
		return 0
	case "help", "-h", "--help":
		say(stdout, usage)
		return 0
	}
	sayf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func serve() int {
	cfg, err := config.LoadFromEnv()
	if err != nil {
		obs.SetupLogging(slog.LevelInfo).Error("invalid configuration", "problems", strings.Split(err.Error(), "\n"))
		return 1
	}
	log := obs.SetupLogging(cfg.LogLevel)
	for _, w := range cfg.Warnings() {
		log.Warn(w)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := platform.Open(cfg, log, modules()...)
	if err != nil {
		log.Error("start", "error", err)
		return 1
	}
	defer func() { _ = app.Close() }()

	// A failing migration keeps the previous schema and stops the start.
	if err := app.Migrate(ctx); err != nil {
		log.Error("migrate", "error", err)
		return 1
	}

	log.Info("starting", "version", obs.AppVersion, "listen", cfg.Listen, "base_url", cfg.BaseURL.String(),
		"modules", len(app.Modules))
	err = httpx.Run(ctx, app.HTTP(), cfg.Listen, func(a net.Addr) { log.Info("listening", "addr", a.String()) })
	if err != nil {
		log.Error("http", "error", err)
		return 1
	}
	log.Info("stopped")
	return 0
}

func manage(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, usage)
		return 2
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		sayf(stderr, "invalid configuration:\n%v\n", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	app, err := platform.Open(cfg, log, modules()...)
	if err != nil {
		sayln(stderr, err)
		return 1
	}
	defer func() { _ = app.Close() }()
	ctx := context.Background()

	switch {
	case len(args) == 2 && args[0] == "migrate" && args[1] == "up":
		err = app.Migrate(ctx)
	case len(args) == 3 && args[0] == "migrate" && args[1] == "down":
		m, ok := app.Module(args[2])
		if !ok {
			err = fmt.Errorf("no module %q", args[2])
			break
		}
		err = db.MigrateDown(ctx, app.DB, log, m)
	case len(args) == 1 && (args[0] == "create-admin" || args[0] == "reset-password"):
		err = adminCommand(ctx, app, args[0], stdout)
	case len(args) == 2 && args[0] == "migrate" && args[1] == "status":
		var st []db.MigrationStatus
		if st, err = app.MigrationStatus(ctx); err == nil {
			printStatus(stdout, st)
		}
	default:
		err = errUnknownManage
	}
	if errors.Is(err, errUnknownManage) {
		sayf(stderr, "unknown manage command\n\n%s", usage)
		return 2
	}
	if err != nil {
		sayln(stderr, err)
		return 1
	}
	return 0
}

// adminCommand runs create-admin or reset-password on the terminal. It
// migrates first, so it works on a database that never served.
func adminCommand(ctx context.Context, app *platform.App, name string, out io.Writer) error {
	p, err := newTTYPrompter(os.Stdin, out)
	if err != nil {
		return err
	}
	if err := app.Migrate(ctx); err != nil {
		return err
	}
	if name == "create-admin" {
		return createAdmin(ctx, app.Auth, p, out)
	}
	return resetPassword(ctx, app.Auth, p, out)
}

var errUnknownManage = errors.New("unknown manage command")

// Output to the terminal is best effort.
func say(w io.Writer, a ...any)                 { _, _ = fmt.Fprint(w, a...) }
func sayln(w io.Writer, a ...any)               { _, _ = fmt.Fprintln(w, a...) }
func sayf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func printStatus(w io.Writer, st []db.MigrationStatus) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	sayln(tw, "MODULE\tVERSION\tSTATE\tFILE")
	for _, s := range st {
		state := "pending"
		if s.Applied {
			state = "applied"
		}
		sayf(tw, "%s\t%d\t%s\t%s\n", s.Module, s.Version, state, s.Path)
	}
	_ = tw.Flush()
}

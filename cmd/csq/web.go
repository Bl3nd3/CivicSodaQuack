// Copyright (c) 2026 Neomantra Corp

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/web"
)

const webUsage = `csq web — browse and run analyses in a web browser

Usage:
  csq web --db <portal.duckdb> [--db alias=file ...] [options]

Options:
  --db      Portal DuckDB to open (repeatable; 'path' or 'alias=path')
  --config  Portal YAML (repeatable; paired positionally with --db).
            Without it the page is read-only; with it, data can be
            downloaded from the page.
  --portal  Socrata host this DB came from (default: derived from the filename)
  --addr    Listen address (default 127.0.0.1:8080)
  --open    Open the page in your default browser

Every database is opened READ_ONLY for reading, so this can run alongside a csq
sync or an MCP server holding the same file. The browser chooses among the
analyses csq ships; it cannot send SQL of its own.

With --config, the page can also download the datasets an analysis needs. Those
downloads take the same advisory lock csq sync takes, and one runs at a time.

The default address is loopback, which means only this machine can reach it.
csq has no login, so binding a wider address publishes every synced dataset to
anyone who can reach the port.

Examples:
  csq web --db data.cityofchicago.org.duckdb --open
  csq web --db chicago=data.cityofchicago.org.duckdb \
          --db nyc=data.cityofnewyork.us.duckdb
  csq web --db data.cityofchicago.org.duckdb \
          --config data.cityofchicago.org.yaml --open
`

func runWeb(args []string) error {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	var (
		dbArgs      []string
		configPaths []string
		portal      string
		addr        string
		openPage    bool
	)
	fs.StringArrayVar(&dbArgs, "db", nil, "Portal DuckDB to open (repeatable)")
	fs.StringArrayVar(&configPaths, "config", nil,
		"Portal YAML (repeatable; paired with --db; enables downloading)")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from")
	fs.StringVar(&addr, "addr", web.DefaultAddr, "Listen address")
	fs.BoolVar(&openPage, "open", false, "Open the page in your browser")
	fs.Usage = func() { fmt.Fprint(os.Stderr, webUsage) }

	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(dbArgs) == 0 {
		fmt.Fprint(os.Stderr, webUsage)
		return fmt.Errorf("--db is required (at least one portal DuckDB)")
	}
	if portal != "" && len(dbArgs) != 1 {
		return fmt.Errorf("--portal applies to a single --db; %d were given", len(dbArgs))
	}

	paths, aliases, err := resolveQueryDBs(dbArgs)
	if err != nil {
		return err
	}
	specs := make([]analysis.DBSpec, len(paths))
	for i := range paths {
		specs[i] = analysis.DBSpec{Alias: aliases[i], Path: paths[i], Portal: portal}
	}

	configs, err := loadWebConfigs(paths, aliases, configPaths)
	if err != nil {
		return err
	}

	if err := ensureDatabases(paths, aliases, configs); err != nil {
		return err
	}

	srv, err := web.New(web.Options{
		DBs: specs, Addr: addr, Out: os.Stderr, Configs: configs,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	// Ctrl-C shuts the server down rather than killing it, so in-flight
	// queries finish and the read-only handles are released cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if openPage {
		go openBrowser(addr)
	}
	return srv.Serve(ctx)
}

// openBrowser best-effort launches the default browser. A failure here is not
// worth failing the command over — the URL is printed either way.
func openBrowser(addr string) {
	url := "http://" + addr
	if strings.HasPrefix(addr, ":") {
		url = "http://127.0.0.1" + addr
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// loadWebConfigs pairs --config with --db positionally, mirroring csq mcp.
//
// A portal without a config stays read-only in the page. That is the same gate
// the MCP server applies to its write tools, and it means opening the UI on a
// database is never enough on its own to start writing to it.
func loadWebConfigs(paths, aliases, configPaths []string) (map[string]*config.Config, error) {
	out := map[string]*config.Config{}
	if len(configPaths) == 0 {
		return out, nil
	}
	if len(configPaths) != len(paths) {
		return nil, fmt.Errorf(
			"--db and --config must be paired: got %d --db flags, %d --config flags",
			len(paths), len(configPaths))
	}
	for i := range paths {
		cfg, err := config.Load(configPaths[i])
		if err != nil {
			return nil, fmt.Errorf("--config %s: %w", configPaths[i], err)
		}
		if cfg.DB != "" && cfg.DB != paths[i] {
			fmt.Fprintf(os.Stderr,
				"[csq] warning: --config %s declares db: %q but --db is %q; using %q\n",
				configPaths[i], cfg.DB, paths[i], paths[i])
		}
		cfg.DB = paths[i]
		out[aliases[i]] = cfg
	}
	return out, nil
}

// ensureDatabases creates an empty csq database for any --db that does not
// exist yet, provided a --config was paired with it.
//
// Someone opening this tool for the first time has no database — getting one is
// the thing they came for. Refusing to start until they have already run a sync
// from a terminal would put the barrier back exactly where the browser UI is
// meant to remove it. Without a config there is nothing that could ever fill
// the file, so that case still errors rather than leaving an empty database
// lying around.
func ensureDatabases(paths, aliases []string, configs map[string]*config.Config) error {
	for i, path := range paths {
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("--db %s: %w", path, err)
		}
		if _, ok := configs[aliases[i]]; !ok {
			return fmt.Errorf(
				"%s does not exist yet.\n"+
					"Pair it with a config so csq can create and fill it:\n"+
					"  csq modes init <mode> --portal <host> --output portal.yaml\n"+
					"  csq web --db %s --config portal.yaml", path, path)
		}
		w, err := duckdb.Open(path) // applies the _csq migrations
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		if err := w.Close(); err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "[csq] created %s\n", path)
	}
	return nil
}

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/dnsproxy"
	"github.com/tktogether/warden-gateway/internal/gateway"
	"github.com/tktogether/warden-gateway/internal/githubproxy"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/panel"
	"github.com/tktogether/warden-gateway/internal/singbox"
	"github.com/tktogether/warden-gateway/internal/telemetry"
)

// Paths describes the host layout used by the gateway.
type Paths struct {
	ConfigPath  string
	DataDir     string
	ServicePath string
	ScriptDir   string
}

// Options configures an App.
type Options struct {
	Config *config.Config
	Paths  Paths
	Log    *logging.Logger
}

// App owns the gateway listeners and runtime state.
type App struct {
	cfg     *config.Config
	paths   Paths
	log     *logging.Logger
	stats   *telemetry.Store
	auth    *auth.Manager
	backups *backup.Manager
	panel   *panel.Server
	tls     *gateway.TLSManager
	docker  *gateway.DockerHandler
	github  *githubproxy.Handler
	dns     *dnsproxy.Proxy
	sni     *gateway.SNIProxy

	httpSrv  *http.Server
	httpsSrv *http.Server
	acmeSrv  *http.Server
	started  time.Time
}

// New builds the runtime from a validated configuration.
func New(opts Options) (*App, error) {
	if opts.Config == nil {
		return nil, errors.New("config is required")
	}
	cfg := opts.Config
	if opts.Paths.ConfigPath == "" {
		opts.Paths.ConfigPath = filepath.Join(config.DefaultConfigDir, "config.json")
	}
	if opts.Paths.DataDir == "" {
		opts.Paths.DataDir = config.DefaultDataDir
	}
	if opts.Paths.ServicePath == "" {
		opts.Paths.ServicePath = "/etc/systemd/system/warden-gateway.service"
	}
	if opts.Paths.ScriptDir == "" {
		opts.Paths.ScriptDir = config.DefaultScriptDir
	}
	logger := opts.Log
	if logger == nil {
		var err error
		logger, err = logging.New(cfg.Log.Level, cfg.Log.File)
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(opts.Paths.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	stats := telemetry.NewStore()
	authMgr, err := auth.NewManager(cfg.SessionSecret, cfg.AccessKeySHA256, 12*time.Hour)
	if err != nil {
		return nil, err
	}
	tlsMgr, err := gateway.NewTLSManager(cfg)
	if err != nil {
		return nil, err
	}
	backups := backup.New(cfg.Backup.Dir, opts.Paths.ConfigPath, opts.Paths.DataDir, opts.Paths.ServicePath, cfg.Backup.Retain)
	var singboxMgr *singbox.Manager
	if cfg.SingBox.Enabled {
		singboxMgr = singbox.New(singbox.Options{
			Root:    cfg.SingBox.Root,
			DataDir: opts.Paths.DataDir,
			Service: cfg.SingBox.Service,
			Host:    cfg.SingBox.Host,
			Log:     logger,
		})
		if singboxMgr.Host() == "" {
			singboxMgr.SetHost(cfg.PublicHost())
		}
	}
	var dnsProxy *dnsproxy.Proxy
	if cfg.DNS.Enabled {
		dnsProxy, err = dnsproxy.New(cfg.DNS, stats, logger)
		if err != nil {
			return nil, err
		}
	}
	panelSrv, err := panel.New(panel.Options{
		Config: cfg, ConfigPath: opts.Paths.ConfigPath, DataDir: opts.Paths.DataDir,
		ScriptDir:   opts.Paths.ScriptDir,
		ServiceName: "warden-gateway.service", Auth: authMgr, Backups: backups,
		Stats: stats, Log: logger, Started: time.Now(), DNS: dnsProxy, SingBox: singboxMgr,
	})
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg: cfg, paths: opts.Paths, log: logger, stats: stats,
		auth: authMgr, backups: backups, panel: panelSrv, tls: tlsMgr,
		started: time.Now(),
	}
	if cfg.Docker.Enabled {
		docker, err := gateway.NewDockerHandler(cfg.Docker, stats, logger)
		if err != nil {
			return nil, err
		}
		a.docker = docker
	}
	if cfg.GitHub.Enabled {
		a.github = githubproxy.New(cfg.GitHub, stats, logger)
	}
	a.dns = dnsProxy
	if cfg.SNI.Enabled {
		a.sni = gateway.NewSNIProxy(cfg.SNI, cfg.Listen.Entry, cfg.Listen.HTTPS, stats, logger)
	}
	return a, nil
}

// StartedAt reports process start time.
func (a *App) StartedAt() time.Time { return a.started }

// Handler returns the TLS front handler.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, a.cfg.PanelPath):
			a.panel.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/v2/"):
			if a.docker == nil {
				http.Error(w, "docker proxy is disabled", http.StatusServiceUnavailable)
				return
			}
			a.docker.ServeHTTP(w, r)
		case p == "/bootstrap.sh" || p == "/toolbox.sh":
			a.serveScript(w, r)
		case strings.HasPrefix(p, "/http://"), strings.HasPrefix(p, "/https://"):
			if a.github == nil {
				http.Error(w, "github proxy is disabled", http.StatusServiceUnavailable)
				return
			}
			a.github.ServeHTTP(w, r)
		case p == "/" || p == "":
			http.Redirect(w, r, a.cfg.PanelPath+"/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
}

// serveScript serves the installer and toolbox scripts from the script directory.
func (a *App) serveScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	path := filepath.Join(a.paths.ScriptDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

// Start opens every configured listener and blocks until ctx is cancelled.
func (a *App) Start(ctx context.Context) error {
	a.panel.Start(ctx)
	errCh := make(chan error, 8)

	httpsLn, err := net.Listen("tcp", a.cfg.Listen.HTTPS)
	if err != nil {
		return fmt.Errorf("listen https %s: %w", a.cfg.Listen.HTTPS, err)
	}
	a.httpsSrv = &http.Server{
		Handler:           a.Handler(),
		TLSConfig:         a.tls.Config,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// The SNI entry relays panel traffic to this listener, so accept the PROXY
	// protocol header it prepends and report the real client address.
	go func() {
		if err := a.httpsSrv.ServeTLS(gateway.WithProxyProtocol(httpsLn), "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	a.log.Infof("https listening on %s (tls %s)", a.cfg.Listen.HTTPS, a.tls.Mode)

	if a.cfg.Listen.HTTP != "" {
		httpLn, err := net.Listen("tcp", a.cfg.Listen.HTTP)
		if err != nil {
			return fmt.Errorf("listen http %s: %w", a.cfg.Listen.HTTP, err)
		}
		a.httpSrv = &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
		go func() {
			if err := a.httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
		a.log.Infof("http listening on %s", a.cfg.Listen.HTTP)
	}

	if a.tls.Mode == "auto" && a.cfg.Listen.ACME != "" {
		acmeLn, err := net.Listen("tcp", a.cfg.Listen.ACME)
		if err != nil {
			return fmt.Errorf("listen acme %s: %w", a.cfg.Listen.ACME, err)
		}
		a.acmeSrv = &http.Server{Handler: a.tls.HTTPHandler, ReadHeaderTimeout: 15 * time.Second}
		go func() {
			if err := a.acmeSrv.Serve(acmeLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
		a.log.Infof("acme listening on %s", a.cfg.Listen.ACME)
	}

	if a.dns != nil {
		if err := a.dns.Serve(); err != nil {
			// A busy DNS port must never take the panel, the ACME listener or the
			// proxies down: log it and keep the rest of the gateway running.
			a.log.Errorf("dns unavailable, split DNS disabled until restart: %v", err)
		}
	}
	if a.sni != nil {
		if err := a.sni.Listen(); err != nil {
			return err
		}
		go func() {
			if err := a.sni.Serve(); err != nil && !errors.Is(err, net.ErrClosed) {
				errCh <- err
			}
		}()
		a.log.Infof("sni entry listening on %s -> %s", a.cfg.Listen.Entry, a.cfg.Listen.HTTPS)
	}

	select {
	case <-ctx.Done():
		return a.Close()
	case err := <-errCh:
		_ = a.Close()
		return err
	}
}

// Close stops every listener.
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if a.sni != nil {
		_ = a.sni.Close()
	}
	if a.dns != nil {
		_ = a.dns.Close()
	}
	if a.acmeSrv != nil {
		_ = a.acmeSrv.Shutdown(ctx)
	}
	if a.httpSrv != nil {
		_ = a.httpSrv.Shutdown(ctx)
	}
	if a.httpsSrv != nil {
		_ = a.httpsSrv.Shutdown(ctx)
	}
	return nil
}

// Config returns the runtime configuration.
func (a *App) Config() *config.Config { return a.cfg }

// Batch is kept small; future extensions can hang background jobs here.

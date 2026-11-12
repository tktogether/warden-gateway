package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tktogether/warden-gateway/internal/app"
	"github.com/tktogether/warden-gateway/internal/config"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/singbox"
	"github.com/tktogether/warden-gateway/internal/version"
)

func main() {
	command := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	var err error
	switch command {
	case "serve":
		err = runServe(args)
	case "init":
		err = runInit(args)
	case "check":
		err = runCheck(args)
	case "key":
		err = runKey(args)
	case "backup":
		err = runBackup(args)
	case "backups":
		err = runBackups(args)
	case "restore":
		err = runRestore(args)
	case "config":
		err = runConfig(args)
	case "singbox":
		err = runSingbox(args)
	case "version", "--version", "-v":
		fmt.Println(version.String())
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", command)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print("warden-gateway - unified gateway for panel, Docker registry mirror and GitHub downloads\n\n" +
		"Usage:\n" +
		"  warden-gateway serve [--config PATH]\n" +
		"  warden-gateway init  [options]\n" +
		"  warden-gateway check [--config PATH]\n" +
		"  warden-gateway key   [--config PATH] [--reset]\n" +
		"  warden-gateway backup|backups|restore [options]\n" +
		"  warden-gateway config show|set [options]\n" +
		"  warden-gateway singbox [status|install|rollback-previous|rollback-preinstall|restart|uninstall] [--version V]\n" +
		"  warden-gateway version\n\n" +
		"Run \"warden-gateway init -h\" for the options that create a new configuration.\n")
}

func defaultConfigPath() string {
	if v := os.Getenv("WARDEN_GATEWAY_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(config.DefaultConfigDir, "config.json")
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	logger, err := logging.New(cfg.Log.Level, cfg.Log.File)
	if err != nil {
		return err
	}
	defer logger.Close()
	a, err := app.New(app.Options{
		Config: cfg,
		Paths: app.Paths{
			ConfigPath:  *cfgPath,
			DataDir:     config.DefaultDataDir,
			ServicePath: "/etc/systemd/system/warden-gateway.service",
		},
		Log: logger,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Infof("warden-gateway %s starting, panel %s", version.Version, cfg.PanelURL())
	return a.Start(ctx)
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	fmt.Printf("config ok: %s\n", *cfgPath)
	fmt.Printf("panel_url=%s\n", cfg.PanelURL())
	return nil
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	dataDir := fs.String("data-dir", config.DefaultDataDir, "data directory")
	publicURL := fs.String("public-url", "", "public panel URL, for example https://panel.example.com/warden")
	domain := fs.String("domain", "", "certificate domain")
	email := fs.String("email", "", "ACME account email (optional)")
	panelPath := fs.String("panel-path", config.DefaultPanelPath, "panel path")
	listenHTTP := fs.String("listen-http", config.DefaultHTTPAddr, "plain HTTP listen address (empty disables)")
	listenHTTPS := fs.String("listen-https", config.DefaultHTTPSAddr, "TLS front listen address")
	listenEntry := fs.String("listen-entry", config.DefaultEntryAddr, "SNI entry listen address (empty disables)")
	listenACME := fs.String("listen-acme", config.DefaultACMEAddr, "ACME HTTP-01 listen address (empty disables)")
	tlsMode := fs.String("tls", "auto", "TLS mode: auto, files or self")
	certFile := fs.String("cert-file", "", "certificate file for tls=files")
	keyFile := fs.String("key-file", "", "private key file for tls=files")
	noDocker := fs.Bool("no-docker", false, "disable the Docker registry mirror")
	noGitHub := fs.Bool("no-github", false, "disable the GitHub download proxy")
	dnsListen := fs.String("dns-listen", "", "DNS split-horizon listen address (empty disables)")
	dnsDomain := fs.String("dns-domain", "", "comma separated domains resolved to this gateway")
	dnsGateway := fs.String("dns-gateway", "", "gateway IP used in DNS answers")
	dnsUpstream := fs.String("dns-upstream", "1.1.1.1:53", "upstream DNS server")
	force := fs.Bool("force", false, "overwrite an existing configuration")
	keyValue := fs.String("access-key", "", "initial access key (generated when empty)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*force {
		if _, err := os.Stat(*cfgPath); err == nil {
			return fmt.Errorf("configuration %s already exists (use --force to replace it)", *cfgPath)
		}
	}
	cfg := config.Default()
	cfg.PanelPath = *panelPath
	cfg.Domain = strings.TrimSpace(*domain)
	cfg.Email = strings.TrimSpace(*email)
	cfg.Listen.HTTP = *listenHTTP
	cfg.Listen.HTTPS = *listenHTTPS
	cfg.Listen.Entry = *listenEntry
	cfg.Listen.ACME = *listenACME
	cfg.TLS.Mode = *tlsMode
	cfg.TLS.CertFile = *certFile
	cfg.TLS.KeyFile = *keyFile
	cfg.Docker.Enabled = !*noDocker
	cfg.GitHub.Enabled = !*noGitHub
	cfg.Backup.Dir = filepath.Join(*dataDir, "backups")
	cfg.Log.File = filepath.Join(*dataDir, "gateway.log")

	origin := strings.TrimSpace(*publicURL)
	if origin == "" && cfg.Domain != "" {
		origin = "https://" + cfg.Domain
	}
	if origin == "" {
		return fmt.Errorf("either --public-url or --domain is required")
	}
	origin = strings.TrimSuffix(origin, "/")
	if !strings.HasSuffix(origin, cfg.PanelPath) {
		origin += cfg.PanelPath
	}
	cfg.PublicURL = origin
	host := cfg.PublicHost()
	if cfg.Domain == "" && host != "" && net.ParseIP(host) == nil {
		cfg.Domain = host
	}
	if net.ParseIP(cfg.Domain) != nil && cfg.TLS.Mode == "auto" {
		cfg.TLS.Mode = "self"
	}
	if cfg.Domain == "" && cfg.TLS.Mode == "auto" {
		cfg.TLS.Mode = "self"
	}
	if cfg.Domain != "" {
		cfg.SNI.PanelDomain = cfg.Domain
	}
	if cfg.TLS.Mode != "auto" {
		cfg.Listen.ACME = ""
	}
	if cfg.Listen.Entry == "" || cfg.SNI.PanelDomain == "" {
		cfg.SNI.Enabled = false
	}
	if *dnsListen != "" {
		cfg.DNS.Enabled = true
		cfg.DNS.Listen = *dnsListen
		cfg.DNS.GatewayIP = *dnsGateway
		cfg.DNS.Upstream = *dnsUpstream
		if *dnsDomain != "" {
			for _, d := range strings.Split(*dnsDomain, ",") {
				if d = strings.TrimSpace(d); d != "" {
					cfg.DNS.Domains = append(cfg.DNS.Domains, d)
				}
			}
		}
	}

	key := strings.TrimSpace(*keyValue)
	generated := false
	var hash string
	if key == "" {
		var err error
		key, hash, err = config.GenerateAccessKey()
		if err != nil {
			return err
		}
		cfg.AccessKeySHA256 = hash
		generated = true
	}
	if err := config.ValidateAccessKey(key); err != nil {
		return err
	}
	cfg.AccessKeySHA256 = config.HashAccessKey(key)
	secret, err := config.GenerateSessionSecret()
	if err != nil {
		return err
	}
	cfg.SessionSecret = secret
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.TLS.CacheDir, 0o700); err != nil && cfg.TLS.Mode != "files" {
		return err
	}
	if err := cfg.Save(*cfgPath); err != nil {
		return err
	}
	if generated {
		keyPath := filepath.Join(*dataDir, "initial-access-key")
		if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("config=%s\n", *cfgPath)
	fmt.Printf("panel_url=%s\n", cfg.PanelURL())
	fmt.Printf("domain=%s\n", cfg.Domain)
	fmt.Printf("tls_mode=%s\n", cfg.TLS.Mode)
	if generated {
		fmt.Printf("access_key=%s\n", key)
	}
	return nil
}

func runKey(args []string) error {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	reset := fs.Bool("reset", false, "generate a new access key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if !*reset {
		fmt.Printf("key_fingerprint=%s\n", cfg.AccessKeySHA256[:12])
		return nil
	}
	key, hash, err := config.GenerateAccessKey()
	if err != nil {
		return err
	}
	cfg.AccessKeySHA256 = hash
	if err := cfg.Save(*cfgPath); err != nil {
		return err
	}
	fmt.Printf("access_key=%s\n", key)
	fmt.Println("restart warden-gateway to apply the new key")
	return nil
}

func runSingbox(args []string) error {
	action := "status"
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		action = rest[0]
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("singbox", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	coreVersion := fs.String("version", "", "sing-box core version to install (empty or latest)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	manager := singbox.New(singbox.Options{
		Root:    cfg.SingBox.Root,
		DataDir: config.DefaultDataDir,
		Service: cfg.SingBox.Service,
		Host:    cfg.SingBox.Host,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	logf := func(format string, a ...interface{}) {
		fmt.Printf(format+"\n", a...)
	}
	printStatus := func(status singbox.CoreStatus) {
		fmt.Printf("installed=%t\n", status.Installed)
		fmt.Printf("version=%s\n", status.Version)
		fmt.Printf("latest=%s\n", status.Latest)
		if status.LatestError != "" {
			fmt.Printf("latest_error=%s\n", status.LatestError)
		}
		fmt.Printf("update_available=%t\n", status.UpdateAvailable)
		fmt.Printf("platform=%s\n", status.Platform)
		fmt.Printf("binary=%s\n", status.Binary)
		fmt.Printf("unit_installed=%t\n", status.UnitInstalled)
		fmt.Printf("backups=%d\n", len(status.Backups))
		if status.Note != "" {
			fmt.Printf("note=%s\n", status.Note)
		}
	}
	switch action {
	case "status":
		printStatus(manager.CoreStatus(ctx))
	case "install", "update":
		status, err := manager.InstallCore(ctx, *coreVersion, logf)
		if err != nil {
			return err
		}
		printStatus(status)
	case "rollback-previous":
		status, err := manager.RollbackPrevious(ctx)
		if err != nil {
			return err
		}
		printStatus(status)
	case "rollback-preinstall":
		status, err := manager.RollbackPreinstall(ctx)
		if err != nil {
			return err
		}
		printStatus(status)
	case "restart":
		if err := manager.RestartCore(ctx); err != nil {
			return err
		}
		printStatus(manager.CoreStatus(ctx))
	case "uninstall":
		status, err := manager.UninstallCore(ctx)
		if err != nil {
			return err
		}
		printStatus(status)
	default:
		return fmt.Errorf("unknown singbox action %q (status, install, rollback-previous, rollback-preinstall, restart, uninstall)", action)
	}
	return nil
}

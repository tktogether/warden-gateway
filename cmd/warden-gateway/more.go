package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/backup"
	"github.com/tktogether/warden-gateway/internal/config"
)

const defaultServicePath = "/etc/systemd/system/warden-gateway.service"

func backupManager(cfgPath string, dataDir string) (*config.Config, *backup.Manager, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if dataDir == "" {
		dataDir = config.DefaultDataDir
	}
	return cfg, backup.New(cfg.Backup.Dir, cfgPath, dataDir, defaultServicePath, cfg.Backup.Retain), nil
}

func runBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	dataDir := fs.String("data-dir", config.DefaultDataDir, "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, mgr, err := backupManager(*cfgPath, *dataDir)
	if err != nil {
		return err
	}
	man, err := mgr.Create()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(man)
	fmt.Println(string(raw))
	return nil
}

func runBackups(args []string) error {
	fs := flag.NewFlagSet("backups", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	dataDir := fs.String("data-dir", config.DefaultDataDir, "data directory")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, mgr, err := backupManager(*cfgPath, *dataDir)
	if err != nil {
		return err
	}
	list, err := mgr.List()
	if err != nil {
		return err
	}
	if *asJSON {
		raw, _ := json.Marshal(map[string]interface{}{"backups": list})
		fmt.Println(string(raw))
		return nil
	}
	if len(list) == 0 {
		fmt.Println("no backups")
		return nil
	}
	for _, m := range list {
		fmt.Printf("%s  %s  %s  %d files\n", m.ID, m.Created, humanBytes(m.Size), len(m.Files))
	}
	return nil
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	dataDir := fs.String("data-dir", config.DefaultDataDir, "data directory")
	id := fs.String("id", "latest", "backup id or latest")
	noRestart := fs.Bool("no-restart", false, "do not restart the service")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, mgr, err := backupManager(*cfgPath, *dataDir)
	if err != nil {
		return err
	}
	if *id == "latest" {
		list, err := mgr.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return fmt.Errorf("no backups available")
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedTS > list[j].CreatedTS })
		*id = list[0].ID
	}
	man, err := mgr.Restore(*id)
	if err != nil {
		return err
	}
	fmt.Printf("restored %s (%d files)\n", man.ID, len(man.Files))
	if *noRestart {
		return nil
	}
	if err := restartService(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: restart failed: %v\n", err)
		return nil
	}
	fmt.Println("service restarted")
	return nil
}

func restartService() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "restart", "warden-gateway.service").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runConfig(args []string) error {
	sub := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "configuration file")
	domain := fs.String("domain", "", "public panel domain")
	email := fs.String("email", "", "ACME email")
	publicURL := fs.String("public-url", "", "public panel URL")
	tlsMode := fs.String("tls", "", "TLS mode: auto, files or self")
	restart := fs.Bool("restart", false, "restart the service after saving")
	docker := fs.String("docker", "", "on or off")
	github := fs.String("github", "", "on or off")
	sni := fs.String("sni", "", "on or off")
	dns := fs.String("dns", "", "on or off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		raw, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Println(string(raw))
		return nil
	case "set":
	default:
		return fmt.Errorf("unknown config command %q", sub)
	}
	changed := false
	if *domain != "" {
		cfg.Domain = strings.TrimSpace(*domain)
		changed = true
	}
	if *email != "" {
		cfg.Email = strings.TrimSpace(*email)
		changed = true
	}
	if *publicURL != "" {
		cfg.PublicURL = strings.TrimSuffix(strings.TrimSpace(*publicURL), "/")
		if cfg.Domain == "" {
			if u, err := parseURLHost(cfg.PublicURL); err == nil {
				cfg.Domain = u
			}
		}
		changed = true
	}
	if *tlsMode != "" {
		cfg.TLS.Mode = *tlsMode
		changed = true
	}
	if *docker != "" {
		cfg.Docker.Enabled = strings.EqualFold(*docker, "on") || strings.EqualFold(*docker, "yes")
		changed = true
	}
	if *github != "" {
		cfg.GitHub.Enabled = strings.EqualFold(*github, "on") || strings.EqualFold(*github, "yes")
		changed = true
	}
	if *sni != "" {
		cfg.SNI.Enabled = strings.EqualFold(*sni, "on") || strings.EqualFold(*sni, "yes")
		changed = true
	}
	if *dns != "" {
		cfg.DNS.Enabled = strings.EqualFold(*dns, "on") || strings.EqualFold(*dns, "yes")
		changed = true
	}
	if !changed {
		return fmt.Errorf("no changes requested")
	}
	if cfg.SNI.PanelDomain == "" {
		cfg.SNI.PanelDomain = cfg.Domain
	}
	if cfg.Domain != "" && net.ParseIP(cfg.Domain) != nil && cfg.TLS.Mode == "auto" {
		cfg.TLS.Mode = "self"
	}
	if err := cfg.Save(*cfgPath); err != nil {
		return err
	}
	fmt.Printf("saved %s\n", *cfgPath)
	if *restart {
		return restartService()
	}
	fmt.Println("restart warden-gateway to apply")
	return nil
}

func parseURLHost(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		return "", fmt.Errorf("missing scheme")
	}
	rest := strings.SplitN(raw, "://", 2)[1]
	host := strings.SplitN(rest, "/", 2)[0]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host), nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ensure imports used by generated wrappers stay stable.
var _ = filepath.Join

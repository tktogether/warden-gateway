package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tktogether/warden-gateway/internal/cnagent"
	"github.com/tktogether/warden-gateway/internal/logging"
	"github.com/tktogether/warden-gateway/internal/version"
)

// defaultAgentConfigPath mirrors cnagent's defaultConfigPath: the agent.json
// location provisioning writes and deregister rewrites.
const defaultAgentConfigPath = "/etc/warden-gateway-cn/agent.json"

func main() {
	command := "run"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	var err error
	switch command {
	case "run":
		err = runRun(args)
	case "once":
		err = runOnce(args)
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
	fmt.Print("warden-cn-agent - CN machine agent reporting to the warden-gateway control plane\n\n" +
		"Usage:\n" +
		"  warden-cn-agent run   [--config PATH]   heartbeat loop (default command)\n" +
		"  warden-cn-agent once  [--config PATH]   one heartbeat cycle, then exit\n" +
		"  warden-cn-agent version\n" +
		"  warden-cn-agent help\n\n" +
		"Defaults: --config " + defaultAgentConfigPath + "\n")
}

// newAgent loads and validates the config, builds the agent and points the
// deregister rewrite at the --config location actually in use. The logger
// writes to stderr so journald captures the daemon's output.
func newAgent(cfgPath string) (*cnagent.Agent, *cnagent.Config, *logging.Logger, error) {
	cfg, err := cnagent.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, nil, err
	}
	logger, err := logging.New("info", "")
	if err != nil {
		return nil, nil, nil, err
	}
	a, err := cnagent.New(cfg, logger)
	if err != nil {
		logger.Close()
		return nil, nil, nil, err
	}
	a.SetConfigPath(cfgPath)
	return a, cfg, logger, nil
}

// runRun drives the heartbeat loop until SIGTERM/SIGINT. Run returns nil on
// ctx cancellation and after a deregister (both are clean stops that already
// shut the DNS proxy down), so main exits 0 on them without extra handling.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultAgentConfigPath, "agent configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, cfg, logger, err := newAgent(*cfgPath)
	if err != nil {
		return err
	}
	defer logger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Infof("warden-cn-agent %s starting, origin %s", version.String(), cfg.Origin)
	return a.Run(ctx)
}

// runOnce executes exactly one cycle and prints a one-line result. A queued
// deregister or a completed self-upgrade is a clean exit (exit 0), not an
// error: after an upgrade the binary is already swapped on disk, so exiting 0
// lets systemd Restart=always bring the new version up.
func runOnce(args []string) error {
	fs := flag.NewFlagSet("once", flag.ExitOnError)
	cfgPath := fs.String("config", defaultAgentConfigPath, "agent configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, _, logger, err := newAgent(*cfgPath)
	if err != nil {
		return err
	}
	defer logger.Close()
	defer a.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err = a.RunOnce(ctx)
	switch {
	case err == nil:
		fmt.Println("once: cycle completed")
		return nil
	case cnagent.IsDeregistered(err):
		fmt.Println("once: agent deregistered")
		return nil
	case cnagent.IsUpgraded(err):
		fmt.Println("once: agent upgraded, exiting for restart")
		return nil
	default:
		return err
	}
}

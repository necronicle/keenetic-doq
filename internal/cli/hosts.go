package cli

import (
	"flag"
	"fmt"
	"os"
	"syscall"

	"github.com/necronicle/keenetic-doq/internal/config"
	"github.com/necronicle/keenetic-doq/internal/smartdns"
)

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if os.IsNotExist(err) {
		return config.Default(), nil
	}
	return cfg, err
}

// reportHosts describes the configured file, not the daemon's in-memory table.
func reportHosts(path string) error {
	cfg, err := loadConfig(path)
	if err != nil {
		return err
	}
	table, err := smartdns.Load(cfg.Hosts)
	if err != nil {
		return err
	}
	fmt.Printf("hosts (configured): %s, %d names (not live daemon state)\n", cfg.Hosts, table.Count())
	return nil
}

func runHosts(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: doqd hosts <export|check|status|reload> [-c CONF]")
		return 2
	}
	command := args[0]
	fs := flag.NewFlagSet("doqd hosts "+command, flag.ContinueOnError)
	conf := fs.String("c", defaultConf, "path to config file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected hosts arguments")
		return 2
	}
	var err error
	switch command {
	case "export":
		_, err = os.Stdout.Write(smartdns.Builtin())
	case "check", "status":
		err = reportHosts(*conf)
	case "reload":
		pid := daemonPID()
		if pid == 0 {
			err = fmt.Errorf("the daemon is not running")
		} else {
			err = syscall.Kill(pid, syscall.SIGHUP)
			if err == nil {
				fmt.Printf("hosts reload requested (pid %d); check daemon logs for success or failure\n", pid)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown hosts command %q\n", command)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

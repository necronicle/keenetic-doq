package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostsCommands(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	conf := filepath.Join(dir, "doqd.conf")
	if err := os.WriteFile(hosts, []byte("192.0.2.1 example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf, []byte("hosts "+hosts+"\nupstream quic://dns.quad9.net\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"check", "status"} {
		if got := Run([]string{"hosts", command, "-c", conf}); got != 0 {
			t.Errorf("hosts %s = %d", command, got)
		}
	}
	if err := os.WriteFile(hosts, []byte("invalid example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Run([]string{"hosts", "check", "-c", conf}); got != 1 {
		t.Errorf("invalid hosts check = %d", got)
	}
}

func TestRemoveRefusesInvalidConfig(t *testing.T) {
	for _, bad := range []string{"geo quic://dns.comss.one", "geo-domain example.com", "typo value"} {
		t.Run(strings.Fields(bad)[0], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "doqd.conf")
			raw := "upstream quic://dns.quad9.net\nupstream quic://dns.nextdns.io\n" + bad + "\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if got := Run([]string{"remove", "-c", path, "2"}); got != 1 {
				t.Errorf("remove invalid config = %d", got)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != raw {
				t.Fatalf("invalid config mutated: %s (%v)", after, err)
			}
		})
	}
}

func TestRemovedCommands(t *testing.T) {
	for _, cmd := range []string{"geo", "add-domain", "remove-domain"} {
		if got := Run([]string{cmd}); got != 2 {
			t.Errorf("removed command %s = %d", cmd, got)
		}
	}
}

func captureStdout(t *testing.T, run func() int) (string, int) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old; f.Close() }()
	code := run()
	if _, err = f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), code
}

func TestHostsExportAndConfiguredStatus(t *testing.T) {
	out, code := captureStdout(t, func() int { return Run([]string{"hosts", "export"}) })
	if code != 0 || !strings.Contains(out, "releases.openai.com") || strings.Contains(out, "ws.chatgpt.com") {
		t.Fatalf("invalid builtin export (code %d)", code)
	}
	hosts := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(hosts, []byte(out), 0600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(t.TempDir(), "doqd.conf")
	if err := os.WriteFile(conf, []byte("hosts "+hosts+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, code = captureStdout(t, func() int { return Run([]string{"hosts", "status", "-c", conf}) })
	if code != 0 || !strings.Contains(out, hosts) || !strings.Contains(out, "1025 names") || !strings.Contains(out, "not live daemon state") {
		t.Fatalf("status = %q (%d)", out, code)
	}
}

func TestHostsReloadStopped(t *testing.T) {
	if daemonPID() != 0 {
		t.Skip("a daemon is running")
	}
	if got := Run([]string{"hosts", "reload"}); got != 1 {
		t.Fatalf("reload stopped = %d", got)
	}
}

func TestFormerGeoServerIsOrdinary(t *testing.T) {
	out := formatList("config", []confServer{{URL: "quic://dns.comss.one"}, {URL: "quic://dns.quad9.net", Fallback: true}}, []probeResult{{}, {}})
	if !strings.Contains(out, "1. quic://dns.comss.one") || !strings.Contains(out, "[fallback]") || strings.Contains(out, "pinned") {
		t.Fatalf("ordinary listing: %s", out)
	}
}

func TestGeoFlagRejected(t *testing.T) {
	if got := Run([]string{"add", "--geo", "quic://dns.comss.one"}); got != 2 {
		t.Fatalf("geo flag = %d", got)
	}
}

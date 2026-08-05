package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lib "github.com/monobilisim/monokit_lib"
)

// TestParseHAProxyBindPorts covers the bind-line address formats HAProxy
// accepts.
func TestParseHAProxyBindPorts(t *testing.T) {
	config := `global
	daemon

frontend pg
	bind :5000
	bind *:5001
	bind 0.0.0.0:5002
	bind 127.0.0.1:1936
	default_backend pg-primary
`

	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	ports, err := parseHAProxyBindPorts(path)
	if err != nil {
		t.Fatalf("parseHAProxyBindPorts failed: %v", err)
	}

	expected := []int{5000, 5001, 5002, 1936}
	if len(ports) != len(expected) {
		t.Fatalf("expected %d ports, got %d: %v", len(expected), len(ports), ports)
	}
	for i, port := range expected {
		if ports[i] != port {
			t.Errorf("port %d: expected %d, got %d", i, port, ports[i])
		}
	}

	if _, err := parseHAProxyBindPorts(filepath.Join(t.TempDir(), "missing.cfg")); err == nil {
		t.Error("expected an error for a missing config file, got none")
	}
}

// TestCheckHAProxy runs against the real haproxy installed in the test
// container: a working config first, then a stopped service.
func TestCheckHAProxy(t *testing.T) {
	if _, err := exec.LookPath("haproxy"); err != nil {
		t.Skip("haproxy is not installed, the Containerfile should install it")
	}

	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	// A minimal config binding one local port.
	config := `global
	daemon

defaults
	mode tcp
	timeout connect 5s
	timeout client 5s
	timeout server 5s

frontend test
	bind 127.0.0.1:15432
	default_backend nowhere

backend nowhere
	server dummy 127.0.0.1:1
`
	if err := os.WriteFile("/etc/haproxy/haproxy.cfg", []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command("systemctl", "restart", "haproxy.service").CombinedOutput(); err != nil {
		t.Fatalf("failed to start haproxy: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("systemctl", "stop", "haproxy.service").Run()
	})

	// Wait for the bind port to come up.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("systemctl", "is-active", "haproxy.service").Output()
		if strings.TrimSpace(string(out)) == "active" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	lib.DBConfig.PostgreSQL.HAProxy.Enabled = true
	lib.DBConfig.PostgreSQL.HAProxy.ConfigPath = "/etc/haproxy/haproxy.cfg"

	CheckHAProxy(lib.Logger)

	// The bind port is open, so the ports module must not be down.
	alarm, err := lib.GetLastZulipAlarm(pluginName, "haproxyPorts")
	if err == nil && alarm.Status == down {
		t.Errorf("haproxyPorts should not be down while haproxy runs, content: %s", alarm.Content)
	}

	// Stop the service: the service alarm must go down.
	if out, err := exec.Command("systemctl", "stop", "haproxy.service").CombinedOutput(); err != nil {
		t.Fatalf("failed to stop haproxy: %v\n%s", err, out)
	}

	CheckHAProxy(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "haproxyService")
	if err != nil {
		t.Fatalf("failed to get last haproxyService alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected haproxyService alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	// Start it again: the service alarm must recover.
	if out, err := exec.Command("systemctl", "start", "haproxy.service").CombinedOutput(); err != nil {
		t.Fatalf("failed to restart haproxy: %v\n%s", err, out)
	}

	CheckHAProxy(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "haproxyService")
	if err != nil {
		t.Fatalf("failed to get last haproxyService alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected haproxyService alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}
}

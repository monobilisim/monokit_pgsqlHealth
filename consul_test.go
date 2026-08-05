package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lib "github.com/monobilisim/monokit_lib"
)

// startConsulStub serves the consul endpoints CheckConsul uses. The catalog
// services and per-node health are swappable between runs.
func startConsulStub(t *testing.T, services *string, nodeHealth *string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/catalog/services", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, *services)
	})
	mux.HandleFunc("/v1/agent/members", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"Name":"pg-node1","Addr":"10.0.0.1"}]`)
	})
	mux.HandleFunc("/v1/health/node/pg-node1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, *nodeHealth)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestCheckConsul runs against a stub consul: first with a rogue catalog
// service and a failing member, then healthy, asserting the transitions.
func TestCheckConsul(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	services := `{"consul":[],"rogue-app":[]}`
	nodeHealth := `[{"Status":"critical"}]`
	server := startConsulStub(t, &services, &nodeHealth)

	// A plain TCP listener stands in for the consul DNS port.
	dnsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dnsListener.Close()
	dnsPort := dnsListener.Addr().(*net.TCPAddr).Port

	lib.DBConfig.PostgreSQL.Consul.Enabled = true
	lib.DBConfig.PostgreSQL.Consul.Url = server.URL
	lib.DBConfig.PostgreSQL.Consul.DnsPort = dnsPort

	CheckConsul(lib.Logger)

	// consul.service is not installed in the test container.
	alarm, err := lib.GetLastZulipAlarm(pluginName, "consulService")
	if err != nil {
		t.Fatalf("failed to get last consulService alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected consulService alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	// Both ports are open, so no down alarm may exist for them.
	for _, moduleName := range []string{"consulPortHttp", "consulPortDns"} {
		alarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err == nil && alarm.Status == down {
			t.Errorf("%s should not be down, content: %s", moduleName, alarm.Content)
		}
	}

	// The rogue catalog service must be reported.
	alarm, err = lib.GetLastZulipAlarm(pluginName, "consulCatalog")
	if err != nil {
		t.Fatalf("failed to get last consulCatalog alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected consulCatalog alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}
	if !strings.Contains(alarm.Content, "rogue-app") {
		t.Errorf("expected the catalog alarm to mention rogue-app, content: %s", alarm.Content)
	}

	// The failing member must be reported with its status.
	alarm, err = lib.GetLastZulipAlarm(pluginName, "consulMemberHealth")
	if err != nil {
		t.Fatalf("failed to get last consulMemberHealth alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected consulMemberHealth alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}
	if !strings.Contains(alarm.Content, "pg-node1") || !strings.Contains(alarm.Content, "critical") {
		t.Errorf("expected the member alarm to mention pg-node1/critical, content: %s", alarm.Content)
	}

	// Second run: catalog is clean and the member passes.
	services = `{"consul":[],"postgresql":[]}`
	nodeHealth = `[{"Status":"passing"}]`

	CheckConsul(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "consulCatalog")
	if err != nil {
		t.Fatalf("failed to get last consulCatalog alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected consulCatalog alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}

	alarm, err = lib.GetLastZulipAlarm(pluginName, "consulMemberHealth")
	if err != nil {
		t.Fatalf("failed to get last consulMemberHealth alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected consulMemberHealth alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}
}

// TestCheckConsulClosedPorts points the check at ports nothing listens on
// and expects both port alarms to be down.
func TestCheckConsulClosedPorts(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	lib.DBConfig.PostgreSQL.Consul.Enabled = true
	lib.DBConfig.PostgreSQL.Consul.Url = "http://127.0.0.1:1" // nothing listens on port 1
	lib.DBConfig.PostgreSQL.Consul.DnsPort = 1

	CheckConsul(lib.Logger)

	for _, moduleName := range []string{"consulPortHttp", "consulPortDns"} {
		alarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			t.Fatalf("failed to get last %s alarm: %v", moduleName, err)
		}
		if alarm.Status != down {
			t.Errorf("expected %s alarm status %q, got %q. Content: %s", moduleName, down, alarm.Status, alarm.Content)
		}
	}
}

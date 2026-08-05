package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	lib "github.com/monobilisim/monokit_lib"
)

// startPatroniStub serves a Patroni-like /cluster endpoint whose response
// can be swapped between runs.
func startPatroniStub(t *testing.T, clusterJSON *string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, *clusterJSON)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// writePatroniConfig writes a minimal patroni.yml pointing at the stub API
// and returns its path. The node is named node2 so the leader-switch hook
// only fires when node2 becomes leader.
func writePatroniConfig(t *testing.T, connectAddress string) string {
	t.Helper()

	config := fmt.Sprintf(`name: node2
scope: testcluster
restapi:
  connect_address: %s
`, connectAddress)

	path := filepath.Join(t.TempDir(), "patroni.yml")
	if err := os.WriteFile(path, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCheckPatroni walks a full scenario: a degraded cluster where this node
// just became leader (role change + hook + size issue), then a healthy
// cluster (recoveries + issue resolution). State between the runs lives in
// the PatroniClusterMember table.
func TestCheckPatroni(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	clusterJSON := ""
	server := startPatroniStub(t, &clusterJSON)
	connectAddress := strings.TrimPrefix(server.URL, "http://")

	hookMarker := filepath.Join(t.TempDir(), "hook-ran")

	lib.DBConfig.PostgreSQL.Patroni.Enabled = true
	lib.DBConfig.PostgreSQL.Patroni.ConfigPath = writePatroniConfig(t, connectAddress)
	lib.DBConfig.PostgreSQL.Patroni.LeaderSwitchHook = "touch " + hookMarker

	// Previous run state: node1 was the leader, node2 a healthy replica.
	lib.DB.Where("1 = 1").Delete(&lib.PatroniClusterMember{})
	previous := []lib.PatroniClusterMember{
		{Scope: "testcluster", Name: "node1", Role: "leader", State: "running", Host: "10.0.0.1", Port: 5432, Timeline: 1},
		{Scope: "testcluster", Name: "node2", Role: "replica", State: "streaming", Host: "10.0.0.2", Port: 5432, Timeline: 1},
	}
	for _, member := range previous {
		if err := lib.DB.Create(&member).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Current state: node1 died, node2 (this node) took over the leadership.
	clusterJSON = `{"scope":"testcluster","members":[
		{"name":"node1","role":"replica","state":"stopped","host":"10.0.0.1","port":5432,"timeline":1},
		{"name":"node2","role":"leader","state":"running","host":"10.0.0.2","port":5432,"timeline":2}
	]}`

	CheckPatroni(lib.Logger)

	// The leader-switch hook must have run, since node2 is this node.
	if _, err := os.Stat(hookMarker); err != nil {
		t.Error("leader-switch hook did not run")
	}

	// node1 is stopped, so the node-states alarm must be down.
	alarm, err := lib.GetLastZulipAlarm(pluginName, "patroniNodeStates")
	if err != nil {
		t.Fatalf("failed to get last patroniNodeStates alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected patroniNodeStates alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}
	if !strings.Contains(alarm.Content, "node1") {
		t.Errorf("expected the unhealthy node table to mention node1, content: %s", alarm.Content)
	}

	// Only one member is running, so the cluster-size alarm and issue open.
	alarm, err = lib.GetLastZulipAlarm(pluginName, "patroniClusterSize")
	if err != nil {
		t.Fatalf("failed to get last patroniClusterSize alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected patroniClusterSize alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	issue, err := lib.GetLastRedmineIssue(pluginName, "patroniClusterSize")
	if err != nil {
		t.Fatalf("failed to get last patroniClusterSize issue: %v", err)
	}
	if issue.Status != down {
		t.Errorf("expected patroniClusterSize issue status %q, got %q", down, issue.Status)
	}

	// The role change must have been reported.
	if _, err := lib.GetLastZulipAlarm(pluginName, "patroniRoleChange"); err != nil {
		t.Error("expected a patroniRoleChange alarm, got none")
	}

	// The new cluster state must have been persisted.
	var saved []lib.PatroniClusterMember
	if err := lib.DB.Find(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("expected 2 persisted members, got %d", len(saved))
	}
	for _, member := range saved {
		if member.Name == "node2" && member.Role != "leader" {
			t.Errorf("persisted role of node2 should be leader, got %s", member.Role)
		}
	}

	// Second run: node1 recovered as a replica; everything is healthy.
	clusterJSON = `{"scope":"testcluster","members":[
		{"name":"node1","role":"replica","state":"streaming","host":"10.0.0.1","port":5432,"timeline":2},
		{"name":"node2","role":"leader","state":"running","host":"10.0.0.2","port":5432,"timeline":2}
	]}`

	CheckPatroni(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "patroniNodeStates")
	if err != nil {
		t.Fatalf("failed to get last patroniNodeStates alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected patroniNodeStates alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}

	alarm, err = lib.GetLastZulipAlarm(pluginName, "patroniClusterSize")
	if err != nil {
		t.Fatalf("failed to get last patroniClusterSize alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected patroniClusterSize alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}

	issue, err = lib.GetLastRedmineIssue(pluginName, "patroniClusterSize")
	if err != nil {
		t.Fatalf("failed to get last patroniClusterSize issue: %v", err)
	}
	if issue.Status != up {
		t.Errorf("expected patroniClusterSize issue status %q, got %q", up, issue.Status)
	}
}

// TestCheckPatroniUnreachableAPI verifies the API alarm fires when the REST
// endpoint is gone.
func TestCheckPatroniUnreachableAPI(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	lib.DBConfig.PostgreSQL.Patroni.Enabled = true
	lib.DBConfig.PostgreSQL.Patroni.ConfigPath = writePatroniConfig(t, "127.0.0.1:1") // nothing listens on port 1
	lib.DBConfig.PostgreSQL.Patroni.LeaderSwitchHook = ""

	CheckPatroni(lib.Logger)

	alarm, err := lib.GetLastZulipAlarm(pluginName, "patroniApi")
	if err != nil {
		t.Fatalf("failed to get last patroniApi alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected patroniApi alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}
}

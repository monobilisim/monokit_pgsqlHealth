package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	lib "github.com/monobilisim/monokit_lib"
)

// stubContainer describes one container the docker stub reports.
type stubContainer struct {
	ID       string
	Name     string
	Image    string
	State    string
	Health   string
	ExitCode int
	HostPort string
}

// writeDockerStub puts a fake docker executable at the front of PATH. `ps`
// and `inspect` report the given containers like docker does, `exec` acts as
// a container that has wal-g (when walgInContainer is set) reporting the
// given integrity status. Every call is appended to the returned log file.
func writeDockerStub(t *testing.T, containers []stubContainer, walgInContainer bool, integrity string) string {
	t.Helper()

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls")

	var ids []string
	var inspect []map[string]any
	for _, container := range containers {
		ids = append(ids, container.ID)

		state := map[string]any{"Status": container.State, "ExitCode": container.ExitCode}
		if container.Health != "" {
			state["Health"] = map[string]any{"Status": container.Health}
		}

		portBindings := map[string]any{}
		if container.HostPort != "" {
			portBindings["5432/tcp"] = []map[string]string{{"HostIp": "", "HostPort": container.HostPort}}
		}

		inspect = append(inspect, map[string]any{
			"Id":         container.ID,
			"Name":       "/" + container.Name,
			"Config":     map[string]any{"Image": container.Image},
			"State":      state,
			"HostConfig": map[string]any{"PortBindings": portBindings},
		})
	}

	inspectJSON, err := json.Marshal(inspect)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"ids":          strings.Join(ids, "\n") + "\n",
		"inspect.json": string(inspectJSON),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	walgExit := 1
	if walgInContainer {
		walgExit = 0
	}

	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]s/calls
case "$1" in
ps)
	cat %[1]s/ids
	;;
inspect)
	cat %[1]s/inspect.json
	;;
exec)
	shift
	[ "$1" = "--user" ] && shift 2
	shift
	case "$1" in
	sh)
		exit %[2]d
		;;
	wal-g)
		case "$2" in
		wal-verify)
			echo "integrity check status: %[3]s"
			echo "timeline check status: OK"
			;;
		backup-list)
			echo '[{"backup_name":"base_000000010000000000000002","time":"2099-01-01T00:00:00Z"}]'
			;;
		esac
		;;
	esac
	;;
esac
`, dir, walgExit, integrity)

	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return callLog
}

// containerNames returns the names of the containers, for comparisons.
func containerNames(containers []pgContainer) []string {
	var names []string
	for _, container := range containers {
		names = append(names, container.Name)
	}
	return names
}

func TestDockerIsPostgresImage(t *testing.T) {
	cases := map[string]bool{
		"postgres":                                      true,
		"postgres:16":                                   true,
		"postgres:16-alpine":                            true,
		"docker.io/library/postgres:15":                 true,
		"pgvector/pgvector:pg16":                        true,
		"bitnami/postgresql:16.2.0":                     true,
		"registry.local:5000/team/postgres:15":          true,
		"postgres@sha256:0123456789abcdef":              true,
		"registry.local:5000/postgres:15@sha256:abcdef": true,
		"POSTGRES:16":                                   true,
		"prometheuscommunity/postgres-exporter":         false,
		"postgrest/postgrest:v12":                       false,
		"postgres/other:1":                              false,
		"redis:7":                                       false,
		"sha256:0123456789abcdef":                       false,
		"":                                              false,
	}

	for image, want := range cases {
		if got := isPostgresImage(image); got != want {
			t.Errorf("isPostgresImage(%q) = %v, want %v", image, got, want)
		}
	}
}

// TestDockerFindContainers lists containers through the docker stub: only
// PostgreSQL images count, and the ones publishing the configured port win.
func TestDockerFindContainers(t *testing.T) {
	lib.InitConfig(configFiles...)

	writeDockerStub(t, []stubContainer{
		{ID: "a1", Name: "pg16", Image: "postgres:16", State: "running", Health: "healthy", HostPort: "5432"},
		{ID: "b2", Name: "pg15-old", Image: "docker.io/library/postgres:15", State: "exited", ExitCode: 0, HostPort: "5432"},
		{ID: "c3", Name: "vector", Image: "pgvector/pgvector:pg16", State: "running", HostPort: "5433"},
		{ID: "d4", Name: "bitnami", Image: "registry.local:5000/bitnami/postgresql:15", State: "running"},
		{ID: "e5", Name: "exporter", Image: "prometheuscommunity/postgres-exporter", State: "running", HostPort: "9187"},
		{ID: "f6", Name: "api", Image: "postgrest/postgrest:v12", State: "running", HostPort: "3000"},
	}, false, "OK")

	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Host = "localhost"

	// The configured port is published: only those containers are ours.
	lib.DBConfig.PostgreSQL.Credentials.Port = 5432

	containers, err := FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if names := containerNames(containers); !slices.Equal(names, []string{"pg16", "pg15-old"}) {
		t.Errorf("expected containers [pg16 pg15-old] for port 5432, got %v", names)
	}
	if len(containers) > 0 && (containers[0].Health != "healthy" || !slices.Equal(containers[0].HostPorts, []int{5432})) {
		t.Errorf("unexpected pg16 details: %+v", containers[0])
	}

	// The port comes from the connection string in string mode too.
	lib.DBConfig.PostgreSQL.Credentials.Mode = "string"
	lib.DBConfig.PostgreSQL.Credentials.ConnectionString = "postgres://postgres:1234@localhost:5433/postgres"

	containers, err = FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if names := containerNames(containers); !slices.Equal(names, []string{"vector"}) {
		t.Errorf("expected containers [vector] for port 5433, got %v", names)
	}

	// Nothing publishes the port: every PostgreSQL container counts.
	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Port = 6543

	containers, err = FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if names := containerNames(containers); !slices.Equal(names, []string{"pg16", "pg15-old", "vector", "bitnami"}) {
		t.Errorf("expected every PostgreSQL container for an unpublished port, got %v", names)
	}
}

// TestDockerCheckContainers drives the dockerContainer alarm through
// unhealthy, recovery, a crash loop, a full stop and the final recovery.
func TestDockerCheckContainers(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	steps := []struct {
		Name       string
		Containers []pgContainer
		Want       string
		Contains   string
	}{
		{
			Name:       "unhealthy",
			Containers: []pgContainer{{Name: "pg", Image: "postgres:16", State: "running", Health: "unhealthy"}},
			Want:       down,
			Contains:   "pg (postgres:16) is running, unhealthy",
		},
		{
			Name: "healthy next to a stopped leftover",
			Containers: []pgContainer{
				{Name: "pg", Image: "postgres:16", State: "running", Health: "healthy"},
				{Name: "pg-old", Image: "postgres:15", State: "exited"},
			},
			Want: up,
		},
		{
			Name:       "crash loop",
			Containers: []pgContainer{{Name: "pg", Image: "postgres:16", State: "restarting", ExitCode: 1}},
			Want:       down,
			Contains:   "pg (postgres:16) is restarting, exit code 1",
		},
		{
			// Stopped for good: left to the connection check, not recovered.
			Name:       "stopped",
			Containers: []pgContainer{{Name: "pg", Image: "postgres:16", State: "exited", ExitCode: 1}},
			Want:       down,
			Contains:   "restarting",
		},
		{
			Name:       "running again",
			Containers: []pgContainer{{Name: "pg", Image: "postgres:16", State: "running"}},
			Want:       up,
		},
		{
			Name:       "paused",
			Containers: []pgContainer{{Name: "pg", Image: "postgres:16", State: "paused"}},
			Want:       down,
			Contains:   "pg (postgres:16) is paused",
		},
		{
			// PostgreSQL moved out of Docker: nothing left to alarm about.
			Name:       "no containers",
			Containers: nil,
			Want:       up,
		},
	}

	for _, step := range steps {
		CheckPostgresContainers(lib.Logger, step.Containers)

		alarm, err := lib.GetLastZulipAlarm(pluginName, "dockerContainer")
		if err != nil {
			t.Fatalf("%s: failed to get last dockerContainer alarm: %v", step.Name, err)
		}
		if alarm.Status != step.Want {
			t.Errorf("%s: expected dockerContainer alarm status %q, got %q. Content: %s", step.Name, step.Want, alarm.Status, alarm.Content)
		}
		if step.Contains != "" && !strings.Contains(alarm.Content, step.Contains) {
			t.Errorf("%s: expected alarm to mention %q. Content: %s", step.Name, step.Contains, alarm.Content)
		}
	}
}

// TestDockerCheckUp fails to connect while the PostgreSQL container
// is stopped and expects the down alarm to say so.
func TestDockerCheckUp(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	// A port nothing listens on.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Host = "127.0.0.1"
	lib.DBConfig.PostgreSQL.Credentials.Port = port

	containers := []pgContainer{{Name: "pg", Image: "postgres:16", State: "exited", ExitCode: 137}}

	conn, err := CheckUp(lib.Logger, containers)
	if err == nil {
		conn.Close(t.Context())
		t.Fatal("expected the connection to fail")
	}

	alarm, err := lib.GetLastZulipAlarm(pluginName, "ping")
	if err != nil {
		t.Fatalf("failed to get last ping alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected ping alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}
	if !strings.Contains(alarm.Content, "pg (postgres:16) is exited, exit code 137") {
		t.Errorf("expected the ping alarm to list the stopped container. Content: %s", alarm.Content)
	}
}

// TestDockerWalG runs the WAL-G checks through `docker exec` when the
// host has no wal-g but the PostgreSQL container does.
func TestDockerWalG(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	lib.DBConfig.PostgreSQL.WalG.Enabled = true
	lib.DBConfig.PostgreSQL.WalG.RunAsUser = "postgres"
	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Port = 5432

	t.Cleanup(func() { walgContainer = "" })

	pg := stubContainer{ID: "a1", Name: "pg", Image: "postgres:16", State: "running", HostPort: "5432"}

	// No wal-g in the container either: the checks are skipped.
	writeDockerStub(t, []stubContainer{pg}, false, "OK")
	containers, err := FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if findWalG(lib.Logger, containers) {
		t.Error("expected findWalG to fail without wal-g in the container")
	}

	// Two running candidates: no way to tell which one to verify.
	other := stubContainer{ID: "b2", Name: "pg-other", Image: "postgres:16", State: "running"}
	writeDockerStub(t, []stubContainer{other, {ID: "c3", Name: "pg-third", Image: "postgres:15", State: "running"}}, true, "OK")
	containers, err = FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if findWalG(lib.Logger, containers) {
		t.Error("expected findWalG to refuse several running containers")
	}

	// wal-g in the container, failing integrity check.
	callLog := writeDockerStub(t, []stubContainer{pg}, true, "FAILURE")
	containers, err = FindPostgresContainers(lib.Logger)
	if err != nil {
		t.Fatalf("FindPostgresContainers failed: %v", err)
	}
	if !findWalG(lib.Logger, containers) {
		t.Fatal("expected findWalG to find wal-g in the container")
	}
	if walgContainer != "a1" {
		t.Fatalf("expected wal-g to run in container a1, got %q", walgContainer)
	}

	CheckWalGVerify(lib.Logger)
	CheckWalGBackupAge(lib.Logger)

	alarm, err := lib.GetLastZulipAlarm(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected walgIntegrity alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"exec --user postgres a1 wal-g wal-verify integrity timeline",
		"exec --user postgres a1 wal-g backup-list --json",
	} {
		if !strings.Contains(string(calls), want) {
			t.Errorf("expected docker call %q, got:\n%s", want, calls)
		}
	}

	// Integrity is OK again inside the container.
	writeDockerStub(t, []stubContainer{pg}, true, "OK")

	CheckWalGVerify(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected walgIntegrity alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}
}

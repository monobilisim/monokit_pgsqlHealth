// Test bootstrap for the pgsqlHealth orchestration tests.
//
// The Containerfile installs PostgreSQL 12, 15 and 18 from PGDG. TestMain
// makes sure every installed cluster is running and that the postgres user
// has the password from config/db.yml, so each test can run its check
// against every version (the version matrix).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	lib "github.com/monobilisim/monokit_lib"
)

const testDBPassword = "1234"

type pgCluster struct {
	Version string
	Port    string
}

// pgClusters parses `pg_lsclusters` and returns every installed cluster.
// Suite containers without PostgreSQL (walg, patroni, consul, haproxy)
// return an empty list and the PostgreSQL-bound tests skip themselves.
func pgClusters() ([]pgCluster, error) {
	if _, err := exec.LookPath("pg_lsclusters"); err != nil {
		return nil, nil
	}

	out, err := exec.Command("pg_lsclusters", "--no-header").Output()
	if err != nil {
		return nil, fmt.Errorf("pg_lsclusters failed: %w", err)
	}

	var clusters []pgCluster
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// 15 main 5433 online postgres /var/lib/postgresql/15/main ...
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		clusters = append(clusters, pgCluster{Version: fields[0], Port: fields[2]})
	}

	return clusters, nil
}

func TestMain(m *testing.M) {
	clusters, err := pgClusters()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not list PostgreSQL clusters: %v\n", err)
		os.Exit(1)
	}

	// No PostgreSQL in this suite container: nothing to bootstrap, the
	// PostgreSQL-bound tests will skip themselves.
	if len(clusters) == 0 {
		os.Exit(m.Run())
	}

	for _, cluster := range clusters {
		// Start the cluster; "already running" is fine.
		if out, err := exec.Command("pg_ctlcluster", cluster.Version, "main", "start").CombinedOutput(); err != nil {
			if !strings.Contains(string(out), "already running") {
				fmt.Fprintf(os.Stderr, "failed to start PostgreSQL %s: %v\n%s\n", cluster.Version, err, out)
				os.Exit(1)
			}
		}

		// Give the postgres user the password used by config/db.yml, via
		// peer authentication as the postgres OS user.
		alter := fmt.Sprintf(`psql -p %s -c "ALTER USER postgres PASSWORD '%s'"`, cluster.Port, testDBPassword)
		if out, err := exec.Command("su", "-s", "/bin/sh", "postgres", "-c", alter).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to set postgres password on %s: %v\n%s\n", cluster.Version, err, out)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

// connectCluster points the plugin at one PostgreSQL cluster and opens the
// global Connection, so a check can run against that version.
func connectCluster(t *testing.T, cluster pgCluster) {
	t.Helper()

	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Host = "localhost"
	lib.DBConfig.PostgreSQL.Credentials.Port = mustAtoi(t, cluster.Port)
	lib.DBConfig.PostgreSQL.Credentials.User = "postgres"
	lib.DBConfig.PostgreSQL.Credentials.Password = testDBPassword
	lib.DBConfig.PostgreSQL.Credentials.DBName = "postgres"

	conn, err := ConnectPSQL(lib.Logger)
	if err != nil {
		t.Fatalf("failed to connect to PostgreSQL %s on port %s: %v", cluster.Version, cluster.Port, err)
	}

	Connection = conn
	t.Cleanup(func() {
		Connection.Close(t.Context())
		Connection = nil
	})
}

func mustAtoi(t *testing.T, text string) int {
	t.Helper()

	var value int
	if _, err := fmt.Sscanf(text, "%d", &value); err != nil {
		t.Fatalf("not a number: %q", text)
	}
	return value
}

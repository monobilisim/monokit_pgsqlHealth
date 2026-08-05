package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	lib "github.com/monobilisim/monokit_lib"
)

// TestConnectPSQL connects to every installed PostgreSQL version in manual
// mode and verifies the server actually is that version.
func TestConnectPSQL(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	clusters, err := pgClusters()
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) == 0 {
		t.Skip("no PostgreSQL clusters in this suite container")
	}

	for _, cluster := range clusters {
		t.Run("PostgreSQL-"+cluster.Version, func(t *testing.T) {
			connectCluster(t, cluster)

			var serverVersion string
			if err := Connection.QueryRow(t.Context(), "SHOW server_version").Scan(&serverVersion); err != nil {
				t.Fatalf("failed to query server_version: %v", err)
			}

			t.Logf("connected to PostgreSQL %s (server_version: %s)", cluster.Version, serverVersion)

			if !strings.HasPrefix(serverVersion, cluster.Version+".") && serverVersion != cluster.Version {
				t.Errorf("expected server version %s, got %s", cluster.Version, serverVersion)
			}
		})
	}
}

// TestConnectPSQLStringMode connects using a connection string instead of the
// discrete credential fields.
func TestConnectPSQLStringMode(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	clusters, err := pgClusters()
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) == 0 {
		t.Skip("no PostgreSQL clusters in this suite container")
	}

	cluster := clusters[0]

	lib.DBConfig.PostgreSQL.Credentials.Mode = "string"
	lib.DBConfig.PostgreSQL.Credentials.ConnectionString = fmt.Sprintf(
		"postgres://postgres:%s@localhost:%s/postgres", testDBPassword, cluster.Port)

	conn, err := ConnectPSQL(lib.Logger)
	if err != nil {
		t.Fatalf("failed to connect with connection string: %v", err)
	}
	defer conn.Close(context.Background())

	var one int
	if err := conn.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("SELECT 1 failed: %v", err)
	}
}

// TestConnectPSQLUnknownMode must fail with a clear error instead of
// connecting anywhere.
func TestConnectPSQLUnknownMode(t *testing.T) {
	lib.InitConfig(configFiles...)

	lib.DBConfig.PostgreSQL.Credentials.Mode = "banana"

	if _, err := ConnectPSQL(lib.Logger); err == nil {
		t.Error("expected an error for unknown credentials mode, got none")
	}
}

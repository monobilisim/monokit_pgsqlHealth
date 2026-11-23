package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

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

// TestConnectPSQLQuotedPassword connects in manual mode with a password that
// has a space, a quote and a backslash, which must be quoted in the
// connection string.
func TestConnectPSQLQuotedPassword(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	clusters, err := pgClusters()
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) == 0 {
		t.Skip("no PostgreSQL clusters in this suite container")
	}

	const password = `p a's\s`

	for _, cluster := range clusters {
		t.Run("PostgreSQL-"+cluster.Version, func(t *testing.T) {
			connectCluster(t, cluster)

			// SQL literal: ' doubled, backslash kept (standard_conforming_strings).
			literal := strings.ReplaceAll(password, "'", "''")
			if _, err := Connection.Exec(t.Context(), "CREATE ROLE monokit_quoted LOGIN PASSWORD '"+literal+"'"); err != nil {
				t.Fatalf("failed to create role: %v", err)
			}
			t.Cleanup(func() {
				Connection.Exec(context.Background(), "DROP ROLE monokit_quoted")
			})

			lib.DBConfig.PostgreSQL.Credentials.User = "monokit_quoted"
			lib.DBConfig.PostgreSQL.Credentials.Password = password

			conn, err := ConnectPSQL(lib.Logger)
			if err != nil {
				t.Fatalf("failed to connect with a quoted password: %v", err)
			}
			conn.Close(context.Background())
		})
	}
}

// TestConnectPSQLTimeout points the plugin at a server that accepts TCP but
// never answers, like a paused container, and expects the connection attempt
// to give up instead of hanging the plugin.
func TestConnectPSQLTimeout(t *testing.T) {
	lib.InitConfig(configFiles...)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()

	lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
	lib.DBConfig.PostgreSQL.Credentials.Host = "127.0.0.1"
	lib.DBConfig.PostgreSQL.Credentials.Port = listener.Addr().(*net.TCPAddr).Port

	start := time.Now()
	conn, err := ConnectPSQL(lib.Logger)
	elapsed := time.Since(start)

	if err == nil {
		conn.Close(context.Background())
		t.Fatal("expected the connection to a silent server to fail")
	}
	if elapsed > connectTimeout+5*time.Second {
		t.Errorf("connection attempt took %s, longer than the %s timeout", elapsed, connectTimeout)
	}
	t.Logf("gave up after %s: %v", elapsed, err)
}

// TestCheckUp fails to connect on a closed port, expects the ping alarm, then
// connects to every PostgreSQL version and expects the recovery alarm.
func TestCheckUp(t *testing.T) {
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
			lib.DBConfig.PostgreSQL.Credentials.Mode = "manual"
			lib.DBConfig.PostgreSQL.Credentials.Host = "localhost"
			lib.DBConfig.PostgreSQL.Credentials.User = "postgres"
			lib.DBConfig.PostgreSQL.Credentials.Password = testDBPassword
			lib.DBConfig.PostgreSQL.Credentials.DBName = "postgres"

			// A port nothing listens on.
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			lib.DBConfig.PostgreSQL.Credentials.Port = listener.Addr().(*net.TCPAddr).Port
			listener.Close()

			if conn, err := CheckUp(lib.Logger, nil); err == nil {
				conn.Close(context.Background())
				t.Fatal("expected the connection to a closed port to fail")
			}

			alarm, err := lib.GetLastZulipAlarm(pluginName, "ping")
			if err != nil {
				t.Fatalf("failed to get last ping alarm: %v", err)
			}
			if alarm.Status != down {
				t.Errorf("expected ping alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
			}

			lib.DBConfig.PostgreSQL.Credentials.Port = mustAtoi(t, cluster.Port)

			conn, err := CheckUp(lib.Logger, nil)
			if err != nil {
				t.Fatalf("failed to connect to PostgreSQL %s: %v", cluster.Version, err)
			}
			conn.Close(context.Background())

			alarm, err = lib.GetLastZulipAlarm(pluginName, "ping")
			if err != nil {
				t.Fatalf("failed to get last ping alarm: %v", err)
			}
			if alarm.Status != up {
				t.Errorf("expected ping alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
			}
		})
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

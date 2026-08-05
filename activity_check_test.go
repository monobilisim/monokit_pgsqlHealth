package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	lib "github.com/monobilisim/monokit_lib"
)

// TestCheckActivity drives the process / active-query / connection-percent
// thresholds into down and back to up against every PostgreSQL version.
func TestCheckActivity(t *testing.T) {
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

			lib.DBConfig.PostgreSQL.Alarm.Enabled = true
			lib.DBConfig.PostgreSQL.LongQuery.Enabled = false

			// Impossible limits force every threshold below its count.
			lib.DBConfig.PostgreSQL.ProcessLimit = 0
			lib.DBConfig.PostgreSQL.ActiveQueryLimit = 0
			lib.DBConfig.PostgreSQL.ConnectionLimitPercent = 0

			CheckActivity(lib.Logger)

			for _, moduleName := range []string{"Process", "ActiveQuery", "Connection"} {
				alarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
				if err != nil {
					t.Fatalf("failed to get last %s alarm: %v", moduleName, err)
				}
				if alarm.Status != down {
					t.Errorf("expected %s alarm status %q, got %q. Content: %s", moduleName, down, alarm.Status, alarm.Content)
				}
			}

			// Generous limits bring every threshold back above its count.
			lib.DBConfig.PostgreSQL.ProcessLimit = 10000
			lib.DBConfig.PostgreSQL.ActiveQueryLimit = 10000
			lib.DBConfig.PostgreSQL.ConnectionLimitPercent = 100

			CheckActivity(lib.Logger)

			for _, moduleName := range []string{"Process", "ActiveQuery", "Connection"} {
				alarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
				if err != nil {
					t.Fatalf("failed to get last %s alarm: %v", moduleName, err)
				}
				if alarm.Status != up {
					t.Errorf("expected %s alarm status %q, got %q. Content: %s", moduleName, up, alarm.Status, alarm.Content)
				}
			}
		})
	}
}

// TestCheckLongRunningQueries starts a slow query, expects the LongQuery
// alarm to fire, then expects the recovery alarm once the query is gone.
func TestCheckLongRunningQueries(t *testing.T) {
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

			lib.DBConfig.PostgreSQL.Alarm.Enabled = true
			lib.DBConfig.PostgreSQL.LongQuery.Enabled = true
			lib.DBConfig.PostgreSQL.LongQuery.Duration = 1

			// Neutral thresholds so only LongQuery is exercised here.
			lib.DBConfig.PostgreSQL.ProcessLimit = 10000
			lib.DBConfig.PostgreSQL.ActiveQueryLimit = 10000
			lib.DBConfig.PostgreSQL.ConnectionLimitPercent = 100

			// Run pg_sleep on a second connection so pg_stat_activity shows
			// an active query older than the 1 second limit.
			sleeper, err := pgx.Connect(context.Background(), connectionStringFor(cluster))
			if err != nil {
				t.Fatalf("failed to open sleeper connection: %v", err)
			}
			defer sleeper.Close(context.Background())

			sleepDone := make(chan error, 1)
			go func() {
				_, err := sleeper.Exec(context.Background(), "SELECT pg_sleep(10)")
				sleepDone <- err
			}()

			// Give the query time to start and cross the 1s limit.
			time.Sleep(3 * time.Second)

			CheckActivity(lib.Logger)

			alarm, err := lib.GetLastZulipAlarm(pluginName, "LongQuery")
			if err != nil {
				t.Fatalf("failed to get last LongQuery alarm: %v", err)
			}
			if alarm.Status != down {
				t.Errorf("expected LongQuery alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
			}

			// Cancel the sleeper and verify recovery.
			sleeper.Close(context.Background())
			<-sleepDone
			time.Sleep(1 * time.Second)

			CheckActivity(lib.Logger)

			alarm, err = lib.GetLastZulipAlarm(pluginName, "LongQuery")
			if err != nil {
				t.Fatalf("failed to get last LongQuery alarm: %v", err)
			}
			if alarm.Status != up {
				t.Errorf("expected LongQuery alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
			}
		})
	}
}

// connectionStringFor builds a connection string for a test cluster, used
// where a test needs its own extra connection.
func connectionStringFor(cluster pgCluster) string {
	return "postgres://postgres:" + testDBPassword + "@localhost:" + cluster.Port + "/postgres"
}

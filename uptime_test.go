package main

import (
	"testing"
	"time"

	lib "github.com/monobilisim/monokit_lib"
)

// TestGetUptime reads the server start time from every PostgreSQL version
// and sanity-checks the values.
func TestGetUptime(t *testing.T) {
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

			uptime, err := GetUptime(lib.Logger)
			if err != nil {
				t.Fatalf("GetUptime failed: %v", err)
			}

			t.Logf("PostgreSQL %s started %s, uptime %s", cluster.Version, uptime.StartTime, uptime.Uptime)

			if uptime.StartTime.IsZero() || uptime.StartTime.After(time.Now()) {
				t.Errorf("implausible start time: %s", uptime.StartTime)
			}

			if uptime.Uptime <= 0 {
				t.Errorf("implausible uptime: %s", uptime.Uptime)
			}
		})
	}
}

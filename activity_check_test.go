package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

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

			// Impossible limits force every threshold below its count.
			lib.DBConfig.PostgreSQL.ProcessLimit = 0
			lib.DBConfig.PostgreSQL.ActiveQueryLimit = 0
			lib.DBConfig.PostgreSQL.ConnectionLimitPercent = 0

			CheckProcessCount(lib.Logger)
			CheckActiveQueryCount(lib.Logger)
			CheckConnectionPercent(lib.Logger)

			for _, moduleName := range []string{"Process", "ActiveQuery", "Connection"} {
				alarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
				if err != nil {
					t.Fatalf("failed to get last %s alarm: %v", moduleName, err)
				}
				if alarm.Status != down {
					t.Errorf("expected %s alarm status %q, got %q. Content: %s", moduleName, down, alarm.Status, alarm.Content)
				}
			}

			// The active-query alarm lists the queries; the only one running
			// is the check's own pg_stat_activity query.
			activeAlarm, err := lib.GetLastZulipAlarm(pluginName, "ActiveQuery")
			if err != nil {
				t.Fatalf("failed to get last ActiveQuery alarm: %v", err)
			}
			if !strings.Contains(activeAlarm.Content, "| PID | Duration |") || !strings.Contains(activeAlarm.Content, "FROM pg_stat_activity") {
				t.Errorf("expected the ActiveQuery alarm to list the running queries, got: %s", activeAlarm.Content)
			}

			// The connection alarm points at the snapshot, which lists this
			// test's own connection.
			connectionAlarm, err := lib.GetLastZulipAlarm(pluginName, "Connection")
			if err != nil {
				t.Fatalf("failed to get last Connection alarm: %v", err)
			}
			_, snapshotPath, found := strings.Cut(connectionAlarm.Content, "pg_stat_activity snapshot: ")
			if !found {
				t.Fatalf("expected the Connection alarm to name the snapshot, got: %s", connectionAlarm.Content)
			}
			snapshot, err := os.ReadFile(strings.TrimSpace(snapshotPath))
			if err != nil {
				t.Fatalf("failed to read the snapshot: %v", err)
			}
			ownPID := strconv.Itoa(int(Connection.PgConn().PID()))
			if !strings.Contains(string(snapshot), "\n"+ownPID+" ") {
				t.Errorf("expected the snapshot to list PID %s, got:\n%s", ownPID, snapshot)
			}

			// Generous limits bring every threshold back above its count.
			lib.DBConfig.PostgreSQL.ProcessLimit = 10000
			lib.DBConfig.PostgreSQL.ActiveQueryLimit = 10000
			lib.DBConfig.PostgreSQL.ConnectionLimitPercent = 100

			CheckProcessCount(lib.Logger)
			CheckActiveQueryCount(lib.Logger)
			CheckConnectionPercent(lib.Logger)

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

			CheckLongRunningQueries(lib.Logger)

			alarm, err := lib.GetLastZulipAlarm(pluginName, "LongQuery")
			if err != nil {
				t.Fatalf("failed to get last LongQuery alarm: %v", err)
			}
			if alarm.Status != down {
				t.Errorf("expected LongQuery alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
			}
			sleeperRow := fmt.Sprintf("| %d |", sleeper.PgConn().PID())
			if !strings.Contains(alarm.Content, sleeperRow) || !strings.Contains(alarm.Content, "pg_sleep(10)") {
				t.Errorf("expected the LongQuery alarm to list the sleeper (PID %d), got: %s", sleeper.PgConn().PID(), alarm.Content)
			}

			// Cancel the sleeper on the server and verify recovery. Closing
			// the client isn't enough: the backend keeps sleeping until
			// pg_sleep ends, still active in pg_stat_activity.
			if _, err := Connection.Exec(t.Context(), "SELECT pg_cancel_backend($1)", sleeper.PgConn().PID()); err != nil {
				t.Fatalf("failed to cancel the sleeper: %v", err)
			}
			<-sleepDone

			CheckLongRunningQueries(lib.Logger)

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

// TestCheckLongRunningQueriesIgnoresWalsender streams WAL to pg_receivewal,
// whose walsender stays "active" in pg_stat_activity with a query_start as
// old as the stream. That is replication, not a slow query, so the LongQuery
// alarm must not fire. PostgreSQL 9.6 doesn't list walsenders in
// pg_stat_activity at all, so it is skipped.
func TestCheckLongRunningQueriesIgnoresWalsender(t *testing.T) {
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
			if strings.HasPrefix(cluster.Version, "9.") {
				t.Skip("walsenders are not in pg_stat_activity before PostgreSQL 10")
			}

			connectCluster(t, cluster)
			startWalReceiver(t, cluster)

			lib.DBConfig.PostgreSQL.Alarm.Enabled = true
			lib.DBConfig.PostgreSQL.LongQuery.Enabled = true
			lib.DBConfig.PostgreSQL.LongQuery.Duration = 1

			// Wait until the walsender has streamed past the 1s limit, so
			// the check sees what a long-lived replica looks like.
			deadline := time.Now().Add(15 * time.Second)
			for {
				var age float64
				err := Connection.QueryRow(t.Context(), "SELECT COALESCE(max(EXTRACT(EPOCH FROM clock_timestamp() - query_start)), 0) FROM pg_stat_activity WHERE backend_type = 'walsender' AND state = 'active'").Scan(&age)
				if err != nil {
					t.Fatalf("failed to look up the walsender: %v", err)
				}
				if age > 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no active walsender older than 2s showed up in pg_stat_activity")
				}
				time.Sleep(500 * time.Millisecond)
			}

			report := fetchActivity(lib.Logger)
			if report == nil {
				t.Fatal("fetchActivity failed")
			}
			for _, activity := range report.Active {
				if activity.Fields["backend_type"] != "client backend" {
					t.Errorf("expected only client backends among active queries, got %q (PID %s)", activity.Fields["backend_type"], activity.Fields["pid"])
				}
			}

			CheckLongRunningQueries(lib.Logger)

			alarm, err := lib.GetLastZulipAlarm(pluginName, "LongQuery")
			if err != nil {
				t.Fatalf("failed to get last LongQuery alarm: %v", err)
			}
			if alarm.Status == down {
				t.Errorf("expected no LongQuery alarm for a walsender, got: %s", alarm.Content)
			}
		})
	}
}

// startWalReceiver runs pg_receivewal against cluster as the postgres OS
// user (peer authentication over the Unix socket) until the test ends.
func startWalReceiver(t *testing.T, cluster pgCluster) {
	t.Helper()

	postgresUser, err := user.Lookup("postgres")
	if err != nil {
		t.Fatalf("failed to look up the postgres user: %v", err)
	}
	uid, gid := mustAtoi(t, postgresUser.Uid), mustAtoi(t, postgresUser.Gid)

	walDir, err := os.MkdirTemp("", "pg_receivewal-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(walDir, uid, gid); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	receiver := exec.Command("/usr/lib/postgresql/"+cluster.Version+"/bin/pg_receivewal",
		"--directory", walDir, "--host", "/var/run/postgresql", "--port", cluster.Port, "--no-password")
	receiver.Stderr = &stderr
	receiver.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
	}
	if err := receiver.Start(); err != nil {
		t.Fatalf("failed to start pg_receivewal: %v", err)
	}

	t.Cleanup(func() {
		receiver.Process.Kill()
		receiver.Wait()
		os.RemoveAll(walDir)
		if t.Failed() {
			t.Logf("pg_receivewal stderr:\n%s", stderr.String())
		}
	})
}

// TestActivityFormatting covers how pg_stat_activity rows are rendered into
// alarm bodies; no PostgreSQL needed.
func TestActivityFormatting(t *testing.T) {
	duration := func(d time.Duration) *time.Duration { return &d }

	activities := []activityInfo{
		{Duration: duration(2 * time.Second), Fields: map[string]string{"pid": "200", "query": "SELECT 2"}},
		{Duration: nil, Fields: map[string]string{"pid": "300", "query": "SELECT 3"}},
		{Duration: duration(90*time.Second + 400*time.Millisecond), Fields: map[string]string{
			"pid": "100", "usename": "app", "datname": "shop", "client_addr": "10.0.0.5/32",
			"query": "SELECT a\n  FROM t\n WHERE b = 'x|y'",
		}},
	}

	table := activityTable(activities)

	// Longest first, the row without a query_start last.
	first, second, third := strings.Index(table, "| 100 |"), strings.Index(table, "| 200 |"), strings.Index(table, "| 300 |")
	if first < 0 || second < 0 || third < 0 || first > second || second > third {
		t.Errorf("expected rows ordered 100, 200, 300 by duration, got:\n%s", table)
	}
	wantRow := `| 100 | 1m30s | app | shop | 10.0.0.5 | SELECT a FROM t WHERE b = 'x\|y' |`
	if !strings.Contains(table, wantRow) {
		t.Errorf("expected row %q, got:\n%s", wantRow, table)
	}
	if !strings.Contains(table, "| 300 | - |  |  | local | SELECT 3 |") {
		t.Errorf("expected the NULL-duration Unix socket row to render as '-' and 'local', got:\n%s", table)
	}

	many := make([]activityInfo, alarmQueryRows+3)
	for i := range many {
		many[i] = activityInfo{Duration: duration(time.Duration(i) * time.Second), Fields: map[string]string{"pid": strconv.Itoa(i)}}
	}
	if table := activityTable(many); !strings.Contains(table, "...and 3 more") || strings.Count(table, "\n| ") != alarmQueryRows+1 {
		t.Errorf("expected %d rows and a '...and 3 more' line, got:\n%s", alarmQueryRows, table)
	}

	if got := clientAddr(activityInfo{Fields: map[string]string{"client_addr": "::1/128"}}); got != "::1" {
		t.Errorf("expected ::1, got %q", got)
	}

	long := strings.Repeat("ğ", activityFieldLimit+10)
	got := truncate(long, activityFieldLimit)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != activityFieldLimit || !strings.HasSuffix(got, "...") {
		t.Errorf("expected %d valid runes ending in '...', got %d runes (valid: %v)", activityFieldLimit, utf8.RuneCountInString(got), utf8.ValidString(got))
	}
	if got := truncate("short", activityFieldLimit); got != "short" {
		t.Errorf("expected short text unchanged, got %q", got)
	}
}

// connectionStringFor builds a connection string for a test cluster, used
// where a test needs its own extra connection.
func connectionStringFor(cluster pgCluster) string {
	return "postgres://postgres:" + testDBPassword + "@localhost:" + cluster.Port + "/postgres"
}

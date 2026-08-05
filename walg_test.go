package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	lib "github.com/monobilisim/monokit_lib"
)

// writeWalGStub puts a fake wal-g executable at the front of PATH. The stub
// prints the given wal-verify statuses and backup list, which is enough for
// CheckWalG since it only reads wal-g's stdout.
func writeWalGStub(t *testing.T, integrity string, timeline string, backupTime time.Time) {
	t.Helper()

	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
wal-verify)
	echo "integrity check status: %s"
	echo "timeline check status: %s"
	;;
backup-list)
	echo '[{"backup_name":"base_000000010000000000000002","time":"%s"}]'
	;;
esac
`, integrity, timeline, backupTime.UTC().Format(time.RFC3339))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wal-g"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestCheckWalG drives the integrity/timeline checks and the backup-age
// check into down with a failing wal-g stub, then into up with an OK stub.
func TestCheckWalG(t *testing.T) {
	lib.InitConfig(configFiles...)
	lib.InitializeDatabase()

	lib.DBConfig.PostgreSQL.WalG.Enabled = true
	lib.DBConfig.PostgreSQL.WalG.VerifyHour = "03:00" // ignored: test mode always runs
	lib.DBConfig.PostgreSQL.WalG.RunAsUser = ""       // run the stub directly

	// Failing verify plus a backup that is three days old.
	writeWalGStub(t, "FAILURE", "OK", time.Now().Add(-72*time.Hour))

	CheckWalG(lib.Logger)

	alarm, err := lib.GetLastZulipAlarm(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected walgIntegrity alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	issue, err := lib.GetLastRedmineIssue(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity issue: %v", err)
	}
	if issue.Status != down {
		t.Errorf("expected walgIntegrity issue status %q, got %q", down, issue.Status)
	}

	alarm, err = lib.GetLastZulipAlarm(pluginName, "walgBackupAge")
	if err != nil {
		t.Fatalf("failed to get last walgBackupAge alarm: %v", err)
	}
	if alarm.Status != down {
		t.Errorf("expected walgBackupAge alarm status %q, got %q. Content: %s", down, alarm.Status, alarm.Content)
	}

	// Timeline was OK the whole time, so it must not be in down state.
	alarm, err = lib.GetLastZulipAlarm(pluginName, "walgTimeline")
	if err == nil && alarm.Status == down {
		t.Errorf("walgTimeline should not be down, content: %s", alarm.Content)
	}

	// Everything healthy: verify passes and the backup is fresh.
	writeWalGStub(t, "OK", "OK", time.Now().Add(-1*time.Hour))

	CheckWalG(lib.Logger)

	alarm, err = lib.GetLastZulipAlarm(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected walgIntegrity alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}

	issue, err = lib.GetLastRedmineIssue(pluginName, "walgIntegrity")
	if err != nil {
		t.Fatalf("failed to get last walgIntegrity issue: %v", err)
	}
	if issue.Status != up {
		t.Errorf("expected walgIntegrity issue status %q, got %q", up, issue.Status)
	}

	alarm, err = lib.GetLastZulipAlarm(pluginName, "walgBackupAge")
	if err != nil {
		t.Fatalf("failed to get last walgBackupAge alarm: %v", err)
	}
	if alarm.Status != up {
		t.Errorf("expected walgBackupAge alarm status %q, got %q. Content: %s", up, alarm.Status, alarm.Content)
	}
}

// TestParseWalVerifyStatus covers the wal-verify output parsing on its own.
func TestParseWalVerifyStatus(t *testing.T) {
	output := `[INFO] Building check runner: integrity
integrity check status: OK
[INFO] Building check runner: timeline
timeline check status: FAILURE
`

	if got := parseWalVerifyStatus(output, "integrity"); got != "OK" {
		t.Errorf("integrity: expected OK, got %q", got)
	}
	if got := parseWalVerifyStatus(output, "timeline"); got != "FAILURE" {
		t.Errorf("timeline: expected FAILURE, got %q", got)
	}
	if got := parseWalVerifyStatus("no status lines here", "integrity"); got != "unknown" {
		t.Errorf("missing: expected unknown, got %q", got)
	}
}

// WAL-G backup verification.
//
// Once per day (at wal-g.verify-hour) this runs `wal-g wal-verify integrity
// timeline` and `wal-g backup-list --json`. Each check type (integrity,
// timeline) raises its own Zulip alarm and Redmine issue when it is not OK,
// and resolves them when it is OK again. The newest basebackup's age is
// checked as well. When the plugin runs as root, wal-g is executed as
// wal-g.run-as-user so it can use peer authentication like the postgres user.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

// backupMaxAge is how old the newest basebackup may get before we alarm.
const backupMaxAge = 48 * time.Hour

// CheckWalGVerify runs `wal-g wal-verify integrity timeline`, alarms when it
// cannot run at all, and checks the integrity and timeline results each under
// its own module with a Zulip alarm and a Redmine issue.
func CheckWalGVerify(logger zerolog.Logger) {
	var moduleName string = "walgVerify"

	logger.Info().Msg("Running WAL-G verification...")

	verifyOutput, err := runWalG(lib.DBConfig.PostgreSQL.WalG.RunAsUser, "wal-verify", "integrity", "timeline")

	// wal-verify itself failed to run
	if err != nil {
		logger.Error().Err(err).Msg("wal-g wal-verify failed to run")

		alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G verification could not run: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	// wal-verify runs again
	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G verification is running again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}

	// Check both wal-verify results the same way, each under its own module.
	checks := []struct {
		Type   string
		Module string
		Status string
	}{
		{"integrity", "walgIntegrity", parseWalVerifyStatus(verifyOutput, "integrity")},
		{"timeline", "walgTimeline", parseWalVerifyStatus(verifyOutput, "timeline")},
	}

	for _, check := range checks {
		moduleName = check.Module
		issueSubject := fmt.Sprintf("%s için WAL-G %s kontrolü başarısız", lib.GlobalConfig.Hostname, check.Type)

		logger.Info().Str("check", check.Type).Str("status", check.Status).Msg("WAL-G check result")

		// check is not OK
		if check.Status != "OK" {
			alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G %s check failed, status: %s", pluginName, lib.GlobalConfig.Hostname, check.Type, check.Status)

			// Zulip alarm
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)

			// Redmine issue
			lastIssue, err := lib.GetLastRedmineIssue(pluginName, moduleName)
			if err != nil {
				lib.Logger.Error().Err(err).Msg("Failed to get last issue from database")
				continue
			}

			var issue lib.Issue

			if lastIssue.Status == up {
				issue = lib.Issue{
					Subject:    issueSubject,
					Notes:      fmt.Sprintf("Sorun devam ediyor.\n\n%s durumu: %s", check.Type, check.Status),
					StatusId:   lib.IssueStatus.Feedback,
					PriorityId: lib.IssuePriority.Urgent,
					Service:    pluginName,
					Module:     moduleName,
					Status:     down,
				}
			} else {
				issue = lib.Issue{
					Subject:     issueSubject,
					Description: fmt.Sprintf("WAL-G %s kontrolü başarısız oldu.\n\n%s durumu: %s", check.Type, check.Type, check.Status),
					StatusId:    lib.IssueStatus.Feedback,
					PriorityId:  lib.IssuePriority.Urgent,
					Service:     pluginName,
					Module:      moduleName,
					Status:      down,
				}
			}

			lib.CreateRedmineIssue(issue)
		}

		// check is OK now
		if check.Status == "OK" {
			// Zulip alarm
			lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to get last alarm from database")
				continue
			}

			if lastAlarm.Status == down {
				alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G %s check is OK again", pluginName, lib.GlobalConfig.Hostname, check.Type)
				lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
			}

			// Redmine issue
			lastIssue, err := lib.GetLastRedmineIssue(pluginName, moduleName)
			if err != nil {
				lib.Logger.Error().Err(err).Msg("Failed to get last issue from database")
				continue
			}

			if lastIssue.Status == down {
				issue := lib.Issue{
					Subject:    issueSubject,
					Notes:      fmt.Sprintf("%s için WAL-G %s kontrolü tekrar başarılı, kapatılıyor.", lib.GlobalConfig.Hostname, check.Type),
					StatusId:   lib.IssueStatus.Resolved,
					PriorityId: lib.IssuePriority.Urgent,
					Service:    pluginName,
					Module:     moduleName,
					Status:     up,
				}

				lib.CreateRedmineIssue(issue)
			}
		}
	}
}

// CheckWalGBackupAge alarms when the newest wal-g basebackup is older than
// backupMaxAge, or when no backup exists at all.
func CheckWalGBackupAge(logger zerolog.Logger) {
	var moduleName string = "walgBackupAge"

	output, err := runWalG(lib.DBConfig.PostgreSQL.WalG.RunAsUser, "backup-list", "--json")
	if err != nil {
		logger.Error().Err(err).Msg("wal-g backup-list failed to run")

		alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G backup list could not be read: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	var backups []walgBackup
	if err := json.Unmarshal([]byte(output), &backups); err != nil {
		logger.Error().Err(err).Msg("Failed to parse wal-g backup-list output")

		alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G backup list could not be parsed: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	if len(backups) == 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G has no backups at all", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	newest := backups[0]
	for _, backup := range backups[1:] {
		if backup.Time.After(newest.Time) {
			newest = backup
		}
	}

	age := time.Since(newest.Time)
	logger.Info().Str("backup", newest.BackupName).Dur("age", age).Msg("Newest WAL-G backup")

	// newest backup is too old
	if age > backupMaxAge {
		alarmMessage := fmt.Sprintf("[%s] - %s - Newest WAL-G backup (%s) is %d hours old", pluginName, lib.GlobalConfig.Hostname, newest.BackupName, int(age.Hours()))
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	// backups are fresh now
	if age <= backupMaxAge {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - WAL-G backups are fresh again, newest: %s", pluginName, lib.GlobalConfig.Hostname, newest.BackupName)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// walgDue reports whether the daily WAL-G verify should run in this
// invocation. The verify runs once a day at the configured HH:MM; an empty
// verify-hour means "run on every invocation". Test mode always runs.
func walgDue() bool {
	verifyHour := lib.DBConfig.PostgreSQL.WalG.VerifyHour
	if verifyHour == "" || lib.IsTestMode() {
		return true
	}
	return time.Now().Format("15:04") == verifyHour
}

// hasWalG checks if the wal-g binary is available.
func hasWalG() bool {
	_, err := exec.LookPath("wal-g")
	return err == nil
}

// runWalG executes wal-g with the given arguments. When the plugin runs as
// root and run-as-user is set, the command is wrapped with `su` so wal-g sees
// the database user's environment and peer authentication.
func runWalG(runAsUser string, args ...string) (string, error) {
	var cmd *exec.Cmd

	if runAsUser != "" && os.Geteuid() == 0 {
		cmd = exec.Command("su", "-s", "/bin/sh", runAsUser, "-c", "wal-g "+strings.Join(args, " "))
	} else {
		cmd = exec.Command("wal-g", args...)
	}

	out, err := cmd.Output()
	return string(out), err
}

// parseWalVerifyStatus extracts e.g. "OK" from a line like
// "integrity check status: OK" in the wal-verify output.
func parseWalVerifyStatus(output string, checkType string) string {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, checkType+" check status:") {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[1])
		}
	}

	return "unknown"
}

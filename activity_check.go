package main

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

const (
	activityQuery = "SELECT clock_timestamp() - pg_stat_activity.query_start AS duration, * FROM pg_stat_activity"

	// alarmQueryRows caps the query table in an alarm body; the rest are
	// summarised as "...and N more".
	alarmQueryRows = 10

	// activityFieldLimit truncates every pg_stat_activity column, mostly so a
	// huge query text can't blow up an alarm or the snapshot file.
	activityFieldLimit = 150
)

// CheckLongRunningQueries alarms when an active query has been running longer
// than the configured long-query duration.
func CheckLongRunningQueries(logger zerolog.Logger) {
	var moduleName string = "LongQuery"

	report := fetchActivity(logger)
	if report == nil {
		return
	}

	longRunningActivities := make([]activityInfo, 0, len(report.Active))

	for _, activity := range report.Active {
		if activity.Duration == nil {
			continue
		}
		if activity.Duration.Seconds() > float64(lib.DBConfig.PostgreSQL.LongQuery.Duration) {
			longRunningActivities = append(longRunningActivities, activity)
		}
	}

	if len(longRunningActivities) > 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL has %d query(ies) running longer than %d seconds:\n\n%s", pluginName, lib.GlobalConfig.Hostname, len(longRunningActivities), lib.DBConfig.PostgreSQL.LongQuery.Duration, activityTable(longRunningActivities))

		if lib.GlobalConfig.ZulipAlarm.Enabled {
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		}
	}

	if len(longRunningActivities) == 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL long running queries ended", pluginName, lib.GlobalConfig.Hostname)

		if lib.GlobalConfig.ZulipAlarm.Enabled {
			lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to get last Zulip alarm")
			}

			if lastAlarm.Status == down {
				lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
			}
		}
	}
}

// CheckProcessCount alarms when the total process count crosses the
// configured process limit.
func CheckProcessCount(logger zerolog.Logger) {
	report := fetchActivity(logger)
	if report == nil {
		return
	}

	checkThreshold(len(report.All), lib.DBConfig.PostgreSQL.ProcessLimit, "Process", "", logger)
}

// CheckActiveQueryCount alarms when the number of active queries crosses the
// configured active-query limit.
func CheckActiveQueryCount(logger zerolog.Logger) {
	report := fetchActivity(logger)
	if report == nil {
		return
	}

	checkThreshold(len(report.Active), lib.DBConfig.PostgreSQL.ActiveQueryLimit, "ActiveQuery", activityTable(report.Active), logger)
}

// CheckConnectionPercent alarms when client-backend connections exceed the
// configured percentage of the server's max_connections. Over the limit it
// also saves every connection to the weekday's pg_stat_activity snapshot.
func CheckConnectionPercent(logger zerolog.Logger) {
	report := fetchActivity(logger)
	if report == nil {
		return
	}

	var maxConnections int
	if err := Connection.QueryRow(context.Background(), "SELECT setting::int FROM pg_settings WHERE name = 'max_connections'").Scan(&maxConnections); err != nil {
		logger.Error().Err(err).Msg("Failed to query max_connections")
		return
	}
	if maxConnections <= 0 {
		return
	}

	connectionLimit := maxConnections * lib.DBConfig.PostgreSQL.ConnectionLimitPercent / 100

	details := ""
	if len(report.Connections) > connectionLimit {
		if path := writeActivitySnapshot(logger, report.Connections); path != "" {
			details = "pg_stat_activity snapshot: " + path
		}
	}

	checkThreshold(len(report.Connections), connectionLimit, "Connection", details, logger)
}

// fetchActivity queries pg_stat_activity once and returns the snapshot the
// activity checks run on, or nil when the query fails.
func fetchActivity(logger zerolog.Logger) *activityReport {
	logger.Info().Msg("Checking PostgreSQL processes...")

	activities := make([]activityInfo, 0, 30)
	activeActivities := make([]activityInfo, 0, 10)
	connectionActivities := make([]activityInfo, 0, 20)

	rows, err := Connection.Query(context.Background(), activityQuery)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to query pg_stat_activity")
		return nil
	}
	defer rows.Close()

	// backend_type exists since PostgreSQL 10. Before that pg_stat_activity
	// lists client backends only, so every row is a connection.
	hasBackendType := false
	for _, fd := range rows.FieldDescriptions() {
		if fd.Name == "backend_type" {
			hasBackendType = true
		}
	}

	for rows.Next() {
		columns, err := rows.Values()
		if err != nil {
			logger.Error().Err(err).Msg("Failed to scan a row of pg_stat_activity")
			continue
		}
		row := activityInfo{
			Fields: map[string]string{},
		}
		for i, fd := range rows.FieldDescriptions() {
			// NULL (no database, no client address, ...) reads as empty.
			columnStr := ""
			if columns[i] != nil {
				columnStr = truncate(fmt.Sprint(columns[i]), activityFieldLimit)
			}
			row.Fields[fd.Name] = columnStr
		}

		if columns[0] != nil {
			dur := ToDuration(columns[0].(pgtype.Interval))
			row.Duration = &dur
		}

		activities = append(activities, row)

		// Only client backends are connections and queries. On PostgreSQL 10+
		// walsenders, autovacuum and parallel workers are also listed, and a
		// walsender stays "active" for as long as the replica streams.
		if !hasBackendType || row.Fields["backend_type"] == "client backend" {
			connectionActivities = append(connectionActivities, row)

			if row.Fields["state"] == "active" {
				activeActivities = append(activeActivities, row)
			}
		}

	}
	if err := rows.Err(); err != nil {
		logger.Error().Err(err).Msg("Error occurred during rows iteration")
		return nil
	}

	logger.Info().Msgf("Successfully retrieved PostgreSQL processes. %d processes found.", len(activities))
	logger.Debug().Interface("activities", activities).Msg("PostgreSQL process details")

	return &activityReport{
		All:         activities,
		Active:      activeActivities,
		Connections: connectionActivities,
	}
}

func ToDuration(i pgtype.Interval) time.Duration {
	if !i.Valid {
		return -1
	}
	const usecPerDay = 24 * 3600 * 1_000_000
	totalUsec := i.Microseconds +
		(int64(i.Days) * usecPerDay) +
		(int64(i.Months) * 30 * usecPerDay)

	return time.Duration(totalUsec) * time.Microsecond
}

// checkThreshold sends the down alarm when the count is above the limit for
// the given module, and the recovery alarm when it drops back below. details,
// when set, is appended to the down alarm.
func checkThreshold(activityCount int, activityThreshold int, thresholdThing string, details string, logger zerolog.Logger) {
	// Down alarm if process count is above threshold
	if activityCount > activityThreshold {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL %s count has been more than the set limit %d, (%d)", pluginName, lib.GlobalConfig.Hostname, thresholdThing, activityThreshold, activityCount)
		if details != "" {
			alarmMessage += "\n\n" + details
		}

		if lib.GlobalConfig.ZulipAlarm.Enabled {
			lib.SendZulipAlarm(alarmMessage, pluginName, thresholdThing, down)
		}

	}

	// UP alarm if process count is below threshold
	if activityCount < activityThreshold {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL %s count is back to normal (%d)", pluginName, lib.GlobalConfig.Hostname, thresholdThing, activityCount)

		if lib.GlobalConfig.ZulipAlarm.Enabled {
			lastAlarm, err := lib.GetLastZulipAlarm(pluginName, thresholdThing)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to get last Zulip alarm")
			}

			if lastAlarm.Status == down {
				lib.SendZulipAlarm(alarmMessage, pluginName, thresholdThing, up)
			}
		}
	}
}

// activityTable renders activities as a markdown table for an alarm body,
// longest-running first and capped at alarmQueryRows rows.
func activityTable(activities []activityInfo) string {
	sorted := sortByDuration(activities)
	shown := sorted[:min(len(sorted), alarmQueryRows)]

	rows := make([][]string, 0, len(shown))
	for _, activity := range shown {
		rows = append(rows, []string{
			activity.Fields["pid"],
			formatDuration(activity.Duration),
			activity.Fields["usename"],
			activity.Fields["datname"],
			clientAddr(activity),
			strings.ReplaceAll(oneLine(activity.Fields["query"]), "|", `\|`),
		})
	}

	table := lib.CreateMarkdownTable([]string{"PID", "Duration", "User", "Database", "Client", "Query"}, rows)
	if hidden := len(sorted) - len(shown); hidden > 0 {
		table += fmt.Sprintf("\n...and %d more\n", hidden)
	}
	return table
}

// writeActivitySnapshot saves connections to
// <log dir>/pgsql-stat_activity-<Weekday>.log and returns its path, or "" when
// the file can't be written. Each weekday has its own file, overwritten by the
// next snapshot that day, so the last week stays on disk (monokit1 wrote the
// same files under /var/log/monodb).
func writeActivitySnapshot(logger zerolog.Logger, connections []activityInfo) string {
	dir := lib.LogDir
	if dir == "" {
		dir = "/var/log"
	}
	path := filepath.Join(dir, fmt.Sprintf("pgsql-stat_activity-%s.log", time.Now().Weekday()))

	var snapshot strings.Builder
	fmt.Fprintf(&snapshot, "pg_stat_activity snapshot at %s, %d connections\n\n", time.Now().Format(time.RFC3339), len(connections))

	table := tabwriter.NewWriter(&snapshot, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "PID\tDURATION\tUSER\tDATABASE\tCLIENT\tSTATE\tQUERY")
	for _, activity := range sortByDuration(connections) {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			activity.Fields["pid"],
			formatDuration(activity.Duration),
			activity.Fields["usename"],
			activity.Fields["datname"],
			clientAddr(activity),
			activity.Fields["state"],
			oneLine(activity.Fields["query"]),
		)
	}
	table.Flush()

	// Query texts can carry literals, so keep the file away from other users.
	if err := os.WriteFile(path, []byte(snapshot.String()), 0o640); err != nil {
		logger.Error().Err(err).Str("path", path).Msg("Failed to write the pg_stat_activity snapshot")
		return ""
	}

	logger.Info().Str("path", path).Msg("Wrote the pg_stat_activity snapshot")
	return path
}

// sortByDuration returns a copy of activities, longest-running first. Rows
// without a query_start sort last.
func sortByDuration(activities []activityInfo) []activityInfo {
	sorted := slices.Clone(activities)
	slices.SortStableFunc(sorted, func(a, b activityInfo) int {
		return cmp.Compare(durationOf(b), durationOf(a))
	})
	return sorted
}

func durationOf(activity activityInfo) time.Duration {
	if activity.Duration == nil {
		return -1
	}
	return *activity.Duration
}

func formatDuration(duration *time.Duration) string {
	if duration == nil || *duration < 0 {
		return "-"
	}
	return duration.Round(time.Second).String()
}

// clientAddr is the client address without the /32 or /128 pgx renders for a
// single-host inet, or "local" for a Unix socket connection.
func clientAddr(activity activityInfo) string {
	addr := activity.Fields["client_addr"]
	if addr == "" {
		return "local"
	}
	addr = strings.TrimSuffix(addr, "/32")
	return strings.TrimSuffix(addr, "/128")
}

// oneLine collapses newlines and runs of whitespace, so a multi-line query
// fits one table row.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// truncate cuts text to at most limit runes, ending in "..." when cut. It
// counts runes so a multi-byte character is never split.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-3]) + "..."
}

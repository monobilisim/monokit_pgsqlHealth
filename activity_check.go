package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

const (
	activityQuery = "SELECT clock_timestamp() - pg_stat_activity.query_start AS duration, * FROM pg_stat_activity"
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
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL has %d query(ies) running longer than %d seconds", pluginName, lib.GlobalConfig.Hostname, len(longRunningActivities), lib.DBConfig.PostgreSQL.LongQuery.Duration)

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

	checkThreshold(len(report.All), lib.DBConfig.PostgreSQL.ProcessLimit, "Process", logger)
}

// CheckActiveQueryCount alarms when the number of active queries crosses the
// configured active-query limit.
func CheckActiveQueryCount(logger zerolog.Logger) {
	report := fetchActivity(logger)
	if report == nil {
		return
	}

	checkThreshold(len(report.Active), lib.DBConfig.PostgreSQL.ActiveQueryLimit, "ActiveQuery", logger)
}

// CheckConnectionPercent alarms when client-backend connections exceed the
// configured percentage of the server's max_connections.
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
	checkThreshold(len(report.Connections), connectionLimit, "Connection", logger)
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
			columnStr := fmt.Sprint(columns[i])
			if len(columnStr) > 150 {
				columnStr = columnStr[:147] + "..."
			}
			row.Fields[fd.Name] = columnStr
		}

		if columns[0] != nil {
			dur := ToDuration(columns[0].(pgtype.Interval))
			row.Duration = &dur
		}

		activities = append(activities, row)

		if row.Fields["state"] == "active" {
			activeActivities = append(activeActivities, row)
		}

		if row.Fields["backend_type"] == "client backend" {
			connectionActivities = append(connectionActivities, row)
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
// the given module, and the recovery alarm when it drops back below.
func checkThreshold(activityCount int, activityThreshold int, thresholdThing string, logger zerolog.Logger) {
	// Down alarm if process count is above threshold
	if activityCount > activityThreshold {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL %s count has been more than the set limit %d, (%d)", pluginName, lib.GlobalConfig.Hostname, thresholdThing, activityThreshold, activityCount)

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

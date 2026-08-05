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

type activityInfo struct {
	Duration *time.Duration
	Fields   map[string]string
}

func CheckActivity(logger zerolog.Logger) {
	logger.Info().Msg("Checking PostgreSQL processes...")

	activities := make([]activityInfo, 0, 30)
	activeActivities := make([]activityInfo, 0, 10)
	connectionActivities := make([]activityInfo, 0, 20)

	rows, err := Connection.Query(context.Background(), activityQuery)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to query pg_stat_activity")
		return
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
		return
	}

	logger.Info().Msgf("Successfully retrieved PostgreSQL processes. %d processes found.", len(activities))
	logger.Debug().Interface("activities", activities).Msg("PostgreSQL process details")

	checkLongRunningQueries(activeActivities, logger)
	checkThreshold(len(activeActivities), lib.DBConfig.PostgreSQL.ActivityLimit, "ActiveActivities", logger)
	checkThreshold(len(activeActivities), lib.DBConfig.PostgreSQL.ConnectionLimit, "Connection", logger)
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

func checkLongRunningQueries(activeActivities []activityInfo, logger zerolog.Logger) {
	if !lib.DBConfig.PostgreSQL.Alarm.Enabled ||
		!lib.DBConfig.PostgreSQL.Alarm.LongQuery.Enabled {
		return
	}

	moduleName := "LongQuery"
	longRunningActivities := make([]activityInfo, 0, len(activeActivities))

	for _, activity := range activeActivities {
		if activity.Duration == nil {
			continue
		}
		if activity.Duration.Seconds() > float64(lib.DBConfig.PostgreSQL.Alarm.LongQuery.DurationSeconds) {
			longRunningActivities = append(longRunningActivities, activity)
		}
	}

	if len(longRunningActivities) > 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL has %d query(ies) running longer than %d seconds", pluginName, lib.GlobalConfig.Hostname, len(longRunningActivities), lib.DBConfig.PostgreSQL.Alarm.LongQuery.DurationSeconds)

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

func checkThreshold(activityCount int, activityThreshold int, thresholdThing string, logger zerolog.Logger) {
	// Down alarm if process count is above threshold
	if lib.DBConfig.PostgreSQL.Alarm.Enabled {
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
}

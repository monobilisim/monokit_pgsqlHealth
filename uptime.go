package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

const uptimeQuery = "SELECT pg_postmaster_start_time(), now() - pg_postmaster_start_time()"

func LogUptime(logger zerolog.Logger) {
	uptime, err := GetUptime(logger)
	if err != nil {
		return
	}
	logger.Debug().Interface("uptime", uptime).Msg("PostgreSQL uptime")
}

func GetUptime(logger zerolog.Logger) (uptime UptimeInfo, err error) {
	var interval pgtype.Interval
	err = Connection.QueryRow(context.Background(), uptimeQuery).Scan(&uptime.StartTime, &interval)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to select pg_postmaster_start_time()")
		return
	}
	uptime.Uptime = ToDuration(interval)
	return
}

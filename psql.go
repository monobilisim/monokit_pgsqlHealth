package main

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

func ConnectPSQL(logger zerolog.Logger) (*pgx.Conn, error) {
	conn, err := pgx.Connect(context.Background(), lib.DBConfig.PostgreSQL.ConnectionString)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to connect to PostgreSQL")
		return nil, err
	}

	return conn, nil
}

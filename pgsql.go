package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

func ConnectPSQL(logger zerolog.Logger) (*pgx.Conn, error) {
	credentials := lib.DBConfig.PostgreSQL.Credentials

	var connString string
	switch credentials.Mode {
	case "string":
		connString = credentials.ConnectionString
	case "manual", "":
		connString = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s",
			credentials.Host, credentials.Port, credentials.User, credentials.Password, credentials.DBName)
	default:
		return nil, fmt.Errorf("unknown credentials mode: %q (expected \"manual\" or \"string\")", credentials.Mode)
	}

	conn, err := pgx.Connect(context.Background(), connString)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to connect to PostgreSQL")
		return nil, err
	}

	return conn, nil
}

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

// connectTimeout bounds the connection attempt, so a frozen server (e.g. a
// paused container that still accepts TCP) can't hang the plugin.
const connectTimeout = 10 * time.Second

// connValueQuoter escapes a value for a libpq keyword/value connection string.
var connValueQuoter = strings.NewReplacer(`\`, `\\`, `'`, `\'`)

// connectionString builds the connection string from the configured
// credentials mode.
func connectionString() (string, error) {
	credentials := lib.DBConfig.PostgreSQL.Credentials

	switch credentials.Mode {
	case "string":
		return credentials.ConnectionString, nil
	case "manual", "":
		// Values are quoted so passwords with spaces or quotes survive.
		return fmt.Sprintf("host='%s' port=%d user='%s' password='%s' dbname='%s'",
			connValueQuoter.Replace(credentials.Host),
			credentials.Port,
			connValueQuoter.Replace(credentials.User),
			connValueQuoter.Replace(credentials.Password),
			connValueQuoter.Replace(credentials.DBName)), nil
	default:
		return "", fmt.Errorf("unknown credentials mode: %q (expected \"manual\" or \"string\")", credentials.Mode)
	}
}

// postgresPort is the TCP port the plugin connects to, used to tell which
// Docker container is the monitored server. 0 when it can't be determined.
func postgresPort() int {
	connString, err := connectionString()
	if err != nil {
		return 0
	}

	config, err := pgx.ParseConfig(connString)
	if err != nil {
		return 0
	}

	return int(config.Port)
}

func ConnectPSQL(logger zerolog.Logger) (*pgx.Conn, error) {
	connString, err := connectionString()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to connect to PostgreSQL")
		return nil, err
	}

	return conn, nil
}

// CheckUp connects to PostgreSQL, alarms when the server can't be reached and
// sends the recovery alarm once it can again. When PostgreSQL runs in Docker
// the down alarm lists the containers' state, which usually tells why.
func CheckUp(logger zerolog.Logger, containers []pgContainer) (*pgx.Conn, error) {
	var moduleName string = "ping"

	conn, err := ConnectPSQL(logger)

	// can't connect
	if err != nil {
		alarmMessage := fmt.Sprintf("[%s] - %s - Can't connect to PostgreSQL: %v", pluginName, lib.GlobalConfig.Hostname, err)

		if len(containers) > 0 {
			alarmMessage += "\n\nPostgreSQL containers:"
			for _, container := range containers {
				alarmMessage += "\n- " + describeContainer(container)
			}
		}

		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return nil, err
	}

	// connected again
	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return conn, nil
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL connection is restored", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}

	return conn, nil
}

package main

import (
	"os"

	"github.com/jackc/pgx/v5"
	lib "github.com/monobilisim/monokit_lib"
)

// comes from -ldflags "-X 'main.version=version'" flag in ci build
var (
	version     string
	pluginName  string   = "pgsqlHealth"
	up          string   = "up"
	down        string   = "down"
	configFiles []string = []string{"db.yml"}
)

var Connection *pgx.Conn

func main() {
	if len(os.Args) > 1 {
		lib.HandleCommonPluginArgs(os.Args, version, configFiles)
		return
	}

	err := lib.InitConfig(configFiles...)
	if err != nil {
		panic("Failed to initialize config: " + err.Error())
	}

	logger, err := lib.InitLogger()
	if err != nil {
		panic("Failed to initialize logger: " + err.Error())
	}

	lib.InitializeDatabase()

	if !lib.DBConfig.PostgreSQL.Alarm.Enabled {
		logger.Info().Msg("PostgreSQL Health monitoring plugin is disabled in configuration. Exiting plugin.")
		return
	}

	logger.Info().Msg("Starting PostgreSQL Health monitoring plugin...")

	Connection, err = ConnectPSQL(logger)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to establish PostgreSQL connection. Exiting plugin.")
		Connection = nil
		return
	}

	if Connection == nil {
		logger.Error().Msg("PostgreSQL connection is not established. Exiting plugin.")
		return
	}

	psqlInDocker := IsPsqlInDocker(logger)
	if psqlInDocker {
		logger.Info().Msg("PostgreSQL appears to be running in Docker. This may affect connection methods and performance.")
	}

	CheckActivity(logger)

	CheckPMM(logger)
}

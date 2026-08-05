package main

import (
	"fmt"
	"os/exec"
	"strings"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

const pmmServiceName = "pmm-agent"

func CheckPMM(logger zerolog.Logger) {
	moduleName := "pmm-agent"

	if _, err := exec.LookPath("pmm-agent"); err != nil {
		logger.Debug().Msg("pmm-agent not found, skipping PMM agent check.")
		return
	}

	if _, err := exec.LookPath("systemctl"); err != nil {
		logger.Debug().Msg("systemctl not found, skipping PMM agent check.")
		return
	}

	// out can be
	// active, inactive, failed, activating, deactivating, unknown, empty string
	out, _ := exec.Command("systemctl", "is-active", pmmServiceName).Output()
	activeState := strings.TrimSpace(string(out))

	isActive := activeState == "active"

	logger.Debug().Msgf("pmm-agent active state: %s", activeState)

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to retrieve last PMM alarm from database.")
		return
	}

	// DOWN
	if !isActive {
		msg := fmt.Sprintf("[%s] - %s - %s service is not running (state: %s).",
			pluginName, lib.GlobalConfig.Hostname, pmmServiceName, activeState)
		logger.Warn().Msg(msg)
		lib.SendZulipAlarm(msg, pluginName, moduleName, down)
	}

	// UP
	if isActive {
		if lastAlarm.Status == down {
			msg := fmt.Sprintf("[%s] - %s - %s service is running again.",
				pluginName, lib.GlobalConfig.Hostname, pmmServiceName)
			logger.Info().Msg(msg)
			lib.SendZulipAlarm(msg, pluginName, moduleName, up)
		}
	}
}

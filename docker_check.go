package main

import (
	"os/exec"
	"strings"

	"github.com/rs/zerolog"
)

var imageNames = [...]string{"postgres", "postgresql", "pgvector"}

func IsPsqlInDocker(logger zerolog.Logger) bool {
	if _, err := exec.LookPath("docker"); err != nil {
		logger.Debug().Msg("IsPsqlInDocker: docker not found, assuming not in Docker")
		return false
	}

	out, err := exec.Command(
		"docker", "ps",
		"-a",
		"--format", "{{.Image}}",
	).Output()
	if err != nil {
		logger.Debug().Err(err).Msg("IsPsqlInDocker: docker ps failed, assuming not in Docker")
		return false
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		lower := strings.ToLower(line)
		for _, image := range imageNames {
			if strings.Contains(lower, image) {
				logger.Debug().Str("image", line).Msg("IsPsqlInDocker: detected via docker ps")
				return true
			}
		}
	}

	logger.Debug().Msg("IsPsqlInDocker: no Docker indicators found")
	return false
}

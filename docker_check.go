// Docker-hosted PostgreSQL.
//
// PostgreSQL containers are recognised by their image repository (postgres,
// postgresql, pgvector; any registry or tag). When some of them publish the
// port the plugin connects to, only those count, so other database containers
// on the host are left alone. The containers feed three checks: the
// dockerContainer alarm below, the container list in the connection-down
// alarm, and WAL-G, which runs inside the container when the host has no
// wal-g.
package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

var imageNames = []string{"postgres", "postgresql", "pgvector"}

// FindPostgresContainers lists the PostgreSQL containers on this host, in
// any state. It returns nil when Docker isn't installed, and an error when
// Docker is there but can't be queried.
func FindPostgresContainers(logger zerolog.Logger) ([]pgContainer, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		logger.Debug().Msg("docker not found, PostgreSQL is not in Docker")
		return nil, nil
	}

	out, err := exec.Command("docker", "ps", "--all", "--quiet", "--no-trunc").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps failed: %w", err)
	}

	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}

	out, err = exec.Command("docker", append([]string{"inspect"}, ids...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect failed: %w", err)
	}

	var inspected []dockerInspect
	if err := json.Unmarshal(out, &inspected); err != nil {
		return nil, fmt.Errorf("failed to parse docker inspect output: %w", err)
	}

	port := postgresPort()

	var containers, published []pgContainer
	for _, inspect := range inspected {
		if !isPostgresImage(inspect.Config.Image) {
			continue
		}

		container := pgContainer{
			ID:       inspect.ID,
			Name:     strings.TrimPrefix(inspect.Name, "/"),
			Image:    inspect.Config.Image,
			State:    inspect.State.Status,
			ExitCode: inspect.State.ExitCode,
		}
		if inspect.State.Health != nil {
			container.Health = inspect.State.Health.Status
		}
		for _, bindings := range inspect.HostConfig.PortBindings {
			for _, binding := range bindings {
				if hostPort, err := strconv.Atoi(binding.HostPort); err == nil {
					container.HostPorts = append(container.HostPorts, hostPort)
				}
			}
		}

		containers = append(containers, container)
		if port != 0 && slices.Contains(container.HostPorts, port) {
			published = append(published, container)
		}
	}

	if len(published) > 0 {
		containers = published
	}

	if len(containers) > 0 {
		logger.Info().Interface("containers", containers).Msg("PostgreSQL runs in Docker")
	}

	return containers, nil
}

// isPostgresImage matches the repository name of an image reference, so
// "postgres:16", "pgvector/pgvector:pg16" and "registry:5000/bitnami/postgresql"
// match but "postgres-exporter" or "postgrest/postgrest" don't.
func isPostgresImage(image string) bool {
	repository, _, _ := strings.Cut(image, "@")
	repository = repository[strings.LastIndex(repository, "/")+1:]
	repository, _, _ = strings.Cut(repository, ":")

	return slices.Contains(imageNames, strings.ToLower(repository))
}

// describeContainer is the one-line state of a container used in alarms,
// e.g. "pg (postgres:16) is exited, exit code 137".
func describeContainer(container pgContainer) string {
	description := fmt.Sprintf("%s (%s) is %s", container.Name, container.Image, container.State)

	switch {
	case container.State == "exited" || container.State == "dead" || container.State == "restarting":
		description += fmt.Sprintf(", exit code %d", container.ExitCode)
	case container.Health != "":
		description += ", " + container.Health
	}

	return description
}

// hasContainerProblem reports whether a container is crash-looping, paused,
// dead or failing its healthcheck. Stopped containers are no problem here: a
// stopped server fails the connection check, and an old container left next
// to its replacement is harmless.
func hasContainerProblem(container pgContainer) bool {
	switch container.State {
	case "restarting", "paused", "dead":
		return true
	case "running":
		return container.Health == "unhealthy"
	}

	return false
}

// CheckPostgresContainers alarms when a PostgreSQL container has a problem
// and sends the recovery alarm once none has and one of them is running.
func CheckPostgresContainers(logger zerolog.Logger, containers []pgContainer) {
	var moduleName string = "dockerContainer"

	var problems []string
	for _, container := range containers {
		if hasContainerProblem(container) {
			problems = append(problems, "- "+describeContainer(container))
		}
	}

	// container problem
	if len(problems) > 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL container is not healthy:\n%s", pluginName, lib.GlobalConfig.Hostname, strings.Join(problems, "\n"))
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	// every container stopped: not recovered, the connection check alarms for it
	isRunning := func(container pgContainer) bool { return container.State == "running" }
	if len(containers) > 0 && !slices.ContainsFunc(containers, isRunning) {
		return
	}

	// containers are healthy again
	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - PostgreSQL container is healthy again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}
}

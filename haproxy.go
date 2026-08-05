// HAProxy health checks for PostgreSQL setups fronted by HAProxy.
//
// Checks the haproxy systemd service, parses the bind ports out of
// haproxy.cfg and verifies that each one accepts TCP connections.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

func CheckHAProxy(logger zerolog.Logger) {
	var moduleName string

	config := lib.DBConfig.PostgreSQL.HAProxy

	if !config.Enabled {
		logger.Debug().Msg("HAProxy check is disabled in configuration, skipping.")
		return
	}

	configPath := config.ConfigPath
	if configPath == "" {
		configPath = "/etc/haproxy/haproxy.cfg"
	}

	// haproxy.service must exist and be active
	moduleName = "haproxyService"

	unitsOut, _ := exec.Command("systemctl", "list-unit-files", "haproxy.service", "--no-legend", "--no-pager").Output()
	installed := strings.TrimSpace(string(unitsOut)) != ""

	activeOut, _ := exec.Command("systemctl", "is-active", "haproxy.service").Output()
	active := strings.TrimSpace(string(activeOut)) == "active"

	if !installed {
		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy service is not installed", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	if !active {
		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy service is not running", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy service is running again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}

	// The bind ports must be parseable from haproxy.cfg
	moduleName = "haproxyConfig"
	ports, err := parseHAProxyBindPorts(configPath)

	if err != nil {
		logger.Error().Err(err).Str("path", configPath).Msg("Failed to parse HAProxy bind ports")

		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy bind ports could not be read: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err = lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy config is readable again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}

	// Every bind port must accept TCP connections; alarm once listing the
	// ports that do not.
	moduleName = "haproxyPorts"

	closedPorts := []string{}
	for _, port := range ports {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 2*time.Second)
		if err != nil {
			logger.Warn().Int("port", port).Msg("HAProxy bind port is not accessible")
			closedPorts = append(closedPorts, strconv.Itoa(port))
			continue
		}
		conn.Close()
	}

	// one or more ports are not accessible
	if len(closedPorts) > 0 {
		alarmMessage := fmt.Sprintf("[%s] - %s - HAProxy bind port(s) not accessible: %s", pluginName, lib.GlobalConfig.Hostname, strings.Join(closedPorts, ", "))
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	// all ports are accessible now
	if len(closedPorts) == 0 {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - All HAProxy bind ports are accessible again", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// parseHAProxyBindPorts extracts the port of every `bind` line in the given
// haproxy.cfg. Addresses look like :80, *:443, 0.0.0.0:5432 or 127.0.0.1:1936.
func parseHAProxyBindPorts(path string) ([]int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open haproxy config %s: %w", path, err)
	}
	defer file.Close()

	var ports []int
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "bind ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// The port is whatever follows the last colon of the address.
		address := fields[1]
		portText := address
		if strings.Contains(address, ":") {
			parts := strings.Split(address, ":")
			portText = parts[len(parts)-1]
		}

		if port, err := strconv.Atoi(portText); err == nil {
			ports = append(ports, port)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading haproxy config %s: %w", path, err)
	}

	if len(ports) == 0 {
		return nil, fmt.Errorf("no bind ports found in %s", path)
	}

	return ports, nil
}

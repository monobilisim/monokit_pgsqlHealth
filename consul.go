// Consul health checks for Patroni setups that use Consul as the DCS.
//
// Checks the consul systemd service, the HTTP (8500) and DNS (8600) ports,
// looks for unexpected services in the catalog, and verifies that every
// agent member reports passing health checks.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
)

// CheckConsulService alarms when the consul systemd service is missing or
// not running.
func CheckConsulService(logger zerolog.Logger) {
	var moduleName string = "consulService"

	unitsOut, _ := exec.Command("systemctl", "list-unit-files", "consul.service", "--no-legend", "--no-pager").Output()
	installed := strings.TrimSpace(string(unitsOut)) != ""

	activeOut, _ := exec.Command("systemctl", "is-active", "consul.service").Output()
	active := strings.TrimSpace(string(activeOut)) == "active"

	if !installed {
		alarmMessage := fmt.Sprintf("[%s] - %s - Consul service is not installed", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	if !active {
		alarmMessage := fmt.Sprintf("[%s] - %s - Consul service is not running", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - Consul service is running again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}
}

// CheckConsulPorts dials the HTTP API port from the configured URL and the
// DNS port on the same host.
func CheckConsulPorts(logger zerolog.Logger) {
	consulURL, dnsPort := consulSettings()

	parsed, err := url.Parse(consulURL)
	if err != nil {
		logger.Error().Err(err).Str("url", consulURL).Msg("Invalid consul URL")
		return
	}

	host := parsed.Hostname()
	httpPort := parsed.Port()
	if httpPort == "" {
		httpPort = "8500"
	}

	ports := []struct {
		Module string
		Label  string
		Port   string
	}{
		{"consulPortHttp", "HTTP", httpPort},
		{"consulPortDns", "DNS", fmt.Sprint(dnsPort)},
	}

	for _, portCheck := range ports {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, portCheck.Port), 5*time.Second)
		open := err == nil
		if open {
			conn.Close()
		}

		// port is closed
		if !open {
			alarmMessage := fmt.Sprintf("[%s] - %s - Consul %s port %s is not open", pluginName, lib.GlobalConfig.Hostname, portCheck.Label, portCheck.Port)
			lib.SendZulipAlarm(alarmMessage, pluginName, portCheck.Module, down)
		}

		// port is open now
		if open {
			lastAlarm, err := lib.GetLastZulipAlarm(pluginName, portCheck.Module)
			if err != nil {
				logger.Error().Err(err).Msg("Failed to get last alarm from database")
				continue
			}

			if lastAlarm.Status == down {
				alarmMessage := fmt.Sprintf("[%s] - %s - Consul %s port %s is open again", pluginName, lib.GlobalConfig.Hostname, portCheck.Label, portCheck.Port)
				lib.SendZulipAlarm(alarmMessage, pluginName, portCheck.Module, up)
			}
		}
	}
}

// CheckConsulCatalog fetches the service catalog and alarms when services
// other than consul/postgres/patroni are registered (a Patroni DCS should
// stay clean).
func CheckConsulCatalog(logger zerolog.Logger) {
	var moduleName string = "consulCatalog"

	consulURL, _ := consulSettings()

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(consulURL + "/v1/catalog/services")

	if err != nil {
		logger.Error().Err(err).Msg("Failed to read consul catalog")

		alarmMessage := fmt.Sprintf("[%s] - %s - Consul catalog is not readable: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}
	defer response.Body.Close()

	var services map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&services); err != nil {
		logger.Error().Err(err).Msg("Failed to parse consul catalog")

		alarmMessage := fmt.Sprintf("[%s] - %s - Consul catalog could not be parsed: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	var unexpected []string
	for name := range services {
		if name != "consul" && !strings.HasPrefix(name, "postgres") && !strings.HasPrefix(name, "patroni") {
			unexpected = append(unexpected, name)
		}
	}

	// unexpected services registered
	if len(unexpected) > 0 {
		logger.Warn().Strs("services", unexpected).Msg("Unexpected services in the Consul catalog")

		alarmMessage := fmt.Sprintf("[%s] - %s - Unexpected services in the Consul catalog: %s", pluginName, lib.GlobalConfig.Hostname, strings.Join(unexpected, ", "))
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	// catalog is clean now
	if len(unexpected) == 0 {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - Consul catalog only contains expected services again", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// CheckConsulMembers verifies that the agent member list is readable and that
// every member's node health checks report passing.
func CheckConsulMembers(logger zerolog.Logger) {
	var moduleName string = "consulMembers"

	consulURL, _ := consulSettings()

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(consulURL + "/v1/agent/members")

	if err != nil {
		logger.Error().Err(err).Msg("Failed to read consul members")

		alarmMessage := fmt.Sprintf("[%s] - %s - Consul members are not readable: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}
	defer response.Body.Close()

	var members []struct {
		Name string `json:"Name"`
		Addr string `json:"Addr"`
	}
	if err := json.NewDecoder(response.Body).Decode(&members); err != nil {
		logger.Error().Err(err).Msg("Failed to parse consul members")

		alarmMessage := fmt.Sprintf("[%s] - %s - Consul members response could not be parsed: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - Consul members are readable again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}

	// Collect every member whose node health is not passing and alarm once.
	moduleName = "consulMemberHealth"

	type failingMember struct {
		Name   string
		Addr   string
		Status string
	}
	failingMembers := []failingMember{}

	for _, member := range members {
		status := consulNodeHealth(client, consulURL, member.Name)
		if status == "passing" {
			continue
		}

		logger.Warn().Str("member", member.Name).Str("status", status).Msg("Consul member is not passing")
		failingMembers = append(failingMembers, failingMember{Name: member.Name, Addr: member.Addr, Status: status})
	}

	// one or more members are not passing
	if len(failingMembers) > 0 {
		tableHeaders := []string{"Member", "Address", "Status"}
		tableValues := [][]string{}
		for _, member := range failingMembers {
			tableValues = append(tableValues, []string{member.Name, member.Addr, member.Status})
		}
		table := lib.CreateMarkdownTable(tableHeaders, tableValues)

		alarmMessage := fmt.Sprintf("[%s] - %s - One or more Consul members are not passing:\n\n", pluginName, lib.GlobalConfig.Hostname)
		alarmMessage += table

		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	// all members are passing now
	if len(failingMembers) == 0 {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - All Consul members are passing again", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// consulSettings returns the configured consul URL and DNS port, falling back
// to the defaults.
func consulSettings() (string, int) {
	consulURL := lib.DBConfig.PostgreSQL.Consul.Url
	if consulURL == "" {
		consulURL = "http://localhost:8500"
	}

	dnsPort := lib.DBConfig.PostgreSQL.Consul.DnsPort
	if dnsPort == 0 {
		dnsPort = 8600
	}

	return consulURL, dnsPort
}

// consulNodeHealth returns the first health check status of a node, or
// "unknown" when it cannot be determined.
func consulNodeHealth(client *http.Client, consulURL string, node string) string {
	response, err := client.Get(consulURL + "/v1/health/node/" + node)
	if err != nil {
		return "unknown"
	}
	defer response.Body.Close()

	var checks []struct {
		Status string `json:"Status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&checks); err != nil || len(checks) == 0 {
		return "unknown"
	}

	return checks[0].Status
}

// Patroni cluster monitoring.
//
// Reads the Patroni config to find the REST API, checks the patroni service,
// fetches /cluster, compares member roles against the state persisted in the
// database (lib.PatroniClusterMember), alarms on role changes and unhealthy
// members, runs the leader-switch hook when this node becomes the leader, and
// keeps a Redmine issue open while the cluster has one or zero running nodes.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	lib "github.com/monobilisim/monokit_lib"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// CheckPatroniConfig alarms when the Patroni configuration file is not
// readable. The config holds the REST API address every other Patroni check
// depends on.
func CheckPatroniConfig(logger zerolog.Logger) {
	var moduleName string = "patroniConfig"

	_, err := loadPatroniConfig(patroniConfigPath())

	if err != nil {
		logger.Error().Err(err).Str("path", patroniConfigPath()).Msg("Failed to read Patroni configuration")

		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni config could not be read: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni config is readable again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}
}

// CheckPatroniService alarms when patroni.service is not active.
func CheckPatroniService(logger zerolog.Logger) {
	var moduleName string = "patroniService"

	serviceOut, _ := exec.Command("systemctl", "is-active", "patroni.service").Output()
	serviceActive := strings.TrimSpace(string(serviceOut)) == "active"

	if !serviceActive {
		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni service is not running", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	if serviceActive {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - Patroni service is running again", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// CheckPatroniAPI alarms when the Patroni REST API does not answer /cluster.
func CheckPatroniAPI(logger zerolog.Logger) {
	var moduleName string = "patroniApi"

	patroni, err := loadPatroniConfig(patroniConfigPath())
	if err != nil {
		// CheckPatroniConfig alarms on an unreadable config.
		logger.Debug().Err(err).Msg("Patroni config is not readable, skipping the API check")
		return
	}

	_, err = fetchPatroniCluster(patroni, logger)

	if err != nil {
		logger.Error().Err(err).Msg("Failed to fetch Patroni cluster state")

		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni REST API is not reachable: %v", pluginName, lib.GlobalConfig.Hostname, err)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
		return
	}

	lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get last alarm from database")
		return
	}

	if lastAlarm.Status == down {
		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni REST API is reachable again", pluginName, lib.GlobalConfig.Hostname)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
	}
}

// CheckPatroniRoleChanges alarms when a member's role differs from the state
// saved by the previous run, and runs the leader-switch hook when this node
// has become the leader.
func CheckPatroniRoleChanges(logger zerolog.Logger) {
	var moduleName string = "patroniRoleChange"

	patroni, cluster := getPatroniCluster(logger)
	if cluster == nil {
		return
	}

	previous := loadPreviousPatroniMembers(logger)

	previousRoles := make(map[string]string, len(previous))
	for _, member := range previous {
		previousRoles[member.Name] = member.Role
	}

	for _, member := range cluster.Members {
		oldRole, known := previousRoles[member.Name]
		if !known || oldRole == member.Role {
			continue
		}

		// A role change is an event, not a persistent down state, so it is
		// reported once without a matching recovery alarm.
		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni role of %s changed: %s -> %s",
			pluginName, lib.GlobalConfig.Hostname, member.Name, oldRole, member.Role)
		logger.Info().Msg(alarmMessage)
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)

		isLeaderRole := member.Role == "leader" || member.Role == "master"
		if !isLeaderRole || member.Name != patroni.Name {
			continue
		}

		// This node was promoted to leader: run the configured shell command
		// and report success or failure under its own module.
		hookModule := "patroniLeaderHook"

		hook := lib.DBConfig.PostgreSQL.Patroni.LeaderSwitchHook
		if hook == "" {
			continue
		}

		logger.Info().Str("hook", hook).Msg("This node became the Patroni leader, running leader-switch hook")

		err := exec.Command("sh", "-c", hook).Run()

		if err != nil {
			alarmMessage := fmt.Sprintf("[%s] - %s - Patroni leader-switch hook failed: %v", pluginName, lib.GlobalConfig.Hostname, err)
			lib.SendZulipAlarm(alarmMessage, pluginName, hookModule, down)
		}

		if err == nil {
			alarmMessage := fmt.Sprintf("[%s] - %s - Patroni leader-switch hook ran successfully", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, hookModule, up)
		}
	}
}

// CheckPatroniMemberStates alarms once, listing every member that is not
// running/streaming.
func CheckPatroniMemberStates(logger zerolog.Logger) {
	var moduleName string = "patroniNodeStates"

	_, cluster := getPatroniCluster(logger)
	if cluster == nil {
		return
	}

	unhealthyMembers := []patroniMember{}

	for _, member := range cluster.Members {
		if member.State == "running" || member.State == "streaming" {
			continue
		}

		logger.Warn().Str("member", member.Name).Str("state", member.State).Msg("Patroni node is not healthy")
		unhealthyMembers = append(unhealthyMembers, member)
	}

	// one or more nodes are not healthy
	if len(unhealthyMembers) > 0 {
		tableHeaders := []string{"Member", "Role", "State"}
		tableValues := [][]string{}
		for _, member := range unhealthyMembers {
			tableValues = append(tableValues, []string{member.Name, member.Role, member.State})
		}
		table := lib.CreateMarkdownTable(tableHeaders, tableValues)

		alarmMessage := fmt.Sprintf("[%s] - %s - One or more Patroni nodes are not healthy:\n\n", pluginName, lib.GlobalConfig.Hostname)
		alarmMessage += table

		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)
	}

	// all nodes are healthy now
	if len(unhealthyMembers) == 0 {
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - All Patroni nodes are healthy again", pluginName, lib.GlobalConfig.Hostname)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}
	}
}

// CheckPatroniClusterSize keeps a Redmine issue open while one or zero
// members are running, embedding a markdown table of the current members.
func CheckPatroniClusterSize(logger zerolog.Logger) {
	var moduleName string = "patroniClusterSize"
	issueSubject := fmt.Sprintf("%s için Patroni cluster boyutu düştü", lib.GlobalConfig.Hostname)

	_, cluster := getPatroniCluster(logger)
	if cluster == nil {
		return
	}

	total := len(cluster.Members)
	if total == 0 {
		return
	}

	runningCount := 0
	for _, member := range cluster.Members {
		if member.State == "running" || member.State == "streaming" {
			runningCount++
		}
	}

	// A previous run may have seen more members than the API reports now
	// (e.g. a node dropped out of the cluster entirely).
	previous := loadPreviousPatroniMembers(logger)
	expectedTotal := total
	if len(previous) > expectedTotal {
		expectedTotal = len(previous)
	}

	// A single-node setup that never had more members is considered healthy.
	healthy := runningCount > 1 || (runningCount == 1 && expectedTotal == 1)

	tableHeaders := []string{"Member", "Role", "State", "Host", "Port", "Timeline"}
	tableValues := [][]string{}
	for _, member := range cluster.Members {
		tableValues = append(tableValues, []string{
			member.Name, member.Role, member.State, member.Host, fmt.Sprint(member.Port), fmt.Sprint(member.Timeline),
		})
	}
	table := lib.CreateMarkdownTable(tableHeaders, tableValues)

	sizeText := fmt.Sprintf("%d/%d", runningCount, expectedTotal)

	// cluster is degraded
	if !healthy {
		alarmMessage := fmt.Sprintf("[%s] - %s - Patroni cluster size is degraded: %s\n\n", pluginName, lib.GlobalConfig.Hostname, sizeText)
		alarmMessage += table

		// Zulip alarm
		lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, down)

		// Redmine issue
		lastIssue, err := lib.GetLastRedmineIssue(pluginName, moduleName)
		if err != nil {
			lib.Logger.Error().Err(err).Msg("Failed to get last issue from database")
			return
		}

		var issue lib.Issue

		if lastIssue.Status == up {
			issue = lib.Issue{
				Subject:    issueSubject,
				Notes:      fmt.Sprintf("Sorun devam ediyor.\n\nPatroni cluster boyutu: %s\n\n%s", sizeText, table),
				StatusId:   lib.IssueStatus.Feedback,
				PriorityId: lib.IssuePriority.Urgent,
				Service:    pluginName,
				Module:     moduleName,
				Status:     down,
			}
		} else {
			issue = lib.Issue{
				Subject:     issueSubject,
				Description: fmt.Sprintf("Patroni cluster boyutu: %s\n\n%s", sizeText, table),
				StatusId:    lib.IssueStatus.Feedback,
				PriorityId:  lib.IssuePriority.Urgent,
				Service:     pluginName,
				Module:      moduleName,
				Status:      down,
			}
		}

		lib.CreateRedmineIssue(issue)
	}

	// cluster is healthy now
	if healthy {
		// Zulip alarm
		lastAlarm, err := lib.GetLastZulipAlarm(pluginName, moduleName)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to get last alarm from database")
			return
		}

		if lastAlarm.Status == down {
			alarmMessage := fmt.Sprintf("[%s] - %s - Patroni cluster size is back to normal: %s", pluginName, lib.GlobalConfig.Hostname, sizeText)
			lib.SendZulipAlarm(alarmMessage, pluginName, moduleName, up)
		}

		// Redmine issue
		lastIssue, err := lib.GetLastRedmineIssue(pluginName, moduleName)
		if err != nil {
			lib.Logger.Error().Err(err).Msg("Failed to get last issue from database")
			return
		}

		if lastIssue.Status == down {
			issue := lib.Issue{
				Subject:    issueSubject,
				Notes:      fmt.Sprintf("Patroni cluster boyutu normale döndü: %s\n\n%s", sizeText, table),
				StatusId:   lib.IssueStatus.Resolved,
				PriorityId: lib.IssuePriority.Urgent,
				Service:    pluginName,
				Module:     moduleName,
				Status:     up,
			}

			lib.CreateRedmineIssue(issue)
		}
	}
}

// SavePatroniMembers replaces the persisted cluster state with the current
// one. Runs after the role-change and cluster-size checks, since both compare
// against the previously saved state.
func SavePatroniMembers(logger zerolog.Logger) {
	_, cluster := getPatroniCluster(logger)
	if cluster == nil {
		return
	}

	if err := lib.DB.Where("1 = 1").Delete(&lib.PatroniClusterMember{}).Error; err != nil {
		logger.Error().Err(err).Msg("Failed to clear previous Patroni cluster state")
		return
	}

	for _, member := range cluster.Members {
		record := lib.PatroniClusterMember{
			Scope:    cluster.Scope,
			Name:     member.Name,
			Role:     member.Role,
			State:    member.State,
			Host:     member.Host,
			Port:     member.Port,
			Timeline: member.Timeline,
		}
		if err := lib.DB.Create(&record).Error; err != nil {
			logger.Error().Err(err).Str("member", member.Name).Msg("Failed to save Patroni cluster member")
		}
	}
}

// patroniConfigPath returns the configured Patroni config path or its
// default location.
func patroniConfigPath() string {
	configPath := lib.DBConfig.PostgreSQL.Patroni.ConfigPath
	if configPath == "" {
		configPath = "/etc/patroni/patroni.yml"
	}
	return configPath
}

func loadPatroniConfig(path string) (*patroniConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config patroniConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	if config.RestAPI.ConnectAddress == "" {
		return nil, fmt.Errorf("patroni config has no restapi.connect_address")
	}

	return &config, nil
}

// getPatroniCluster loads the Patroni config and fetches the current cluster
// state. Errors are only logged here: CheckPatroniConfig and CheckPatroniAPI
// own the alarms for an unreadable config and an unreachable API.
func getPatroniCluster(logger zerolog.Logger) (*patroniConfig, *patroniClusterResponse) {
	patroni, err := loadPatroniConfig(patroniConfigPath())
	if err != nil {
		logger.Debug().Err(err).Msg("Patroni config is not readable, skipping the cluster check")
		return nil, nil
	}

	cluster, err := fetchPatroniCluster(patroni, logger)
	if err != nil {
		logger.Debug().Err(err).Msg("Patroni REST API is not reachable, skipping the cluster check")
		return nil, nil
	}

	return patroni, cluster
}

// fetchPatroniCluster GETs /cluster from the Patroni REST API, using HTTPS
// with the certificates from the Patroni config when they are set up.
func fetchPatroniCluster(patroni *patroniConfig, logger zerolog.Logger) (*patroniClusterResponse, error) {
	scheme := "http"
	transport := &http.Transport{}

	if patroni.RestAPI.CertFile != "" {
		scheme = "https"
		tlsConfig := &tls.Config{}

		cert, err := tls.LoadX509KeyPair(patroni.RestAPI.CertFile, patroni.RestAPI.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load patroni client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}

		if patroni.RestAPI.CAFile != "" {
			caData, err := os.ReadFile(patroni.RestAPI.CAFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read patroni CA file: %w", err)
			}
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(caData)
			tlsConfig.RootCAs = pool
		}

		transport.TLSClientConfig = tlsConfig
	}

	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	clusterURL := scheme + "://" + strings.TrimSuffix(patroni.RestAPI.ConnectAddress, "/") + "/cluster"

	response, err := client.Get(clusterURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("patroni API returned HTTP %d", response.StatusCode)
	}

	var cluster patroniClusterResponse
	if err := json.NewDecoder(response.Body).Decode(&cluster); err != nil {
		return nil, fmt.Errorf("failed to decode patroni cluster response: %w", err)
	}

	logger.Debug().Interface("cluster", cluster).Msg("Patroni cluster state")
	return &cluster, nil
}

// loadPreviousPatroniMembers returns the cluster state saved by the previous
// run, used for role-change and cluster-size comparisons.
func loadPreviousPatroniMembers(logger zerolog.Logger) []lib.PatroniClusterMember {
	var members []lib.PatroniClusterMember
	if err := lib.DB.Find(&members).Error; err != nil {
		logger.Error().Err(err).Msg("Failed to load previous Patroni cluster state")
		return nil
	}
	return members
}

package main

import "time"

type activityInfo struct {
	Duration *time.Duration
	Fields   map[string]string
}

// activityReport is one pg_stat_activity snapshot, pre-split into the subsets
// the activity checks need.
type activityReport struct {
	All         []activityInfo
	Active      []activityInfo
	Connections []activityInfo
}

type UptimeInfo struct {
	StartTime time.Time
	Uptime    time.Duration
}

// pgContainer is a Docker container running a PostgreSQL image.
type pgContainer struct {
	ID        string
	Name      string
	Image     string
	State     string // running, restarting, paused, exited, created, dead
	Health    string // healthy, unhealthy, starting; empty without a healthcheck
	ExitCode  int
	HostPorts []int
}

// dockerInspect is the part of `docker inspect` output the checks read.
type dockerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status   string `json:"Status"`
		ExitCode int    `json:"ExitCode"`
		Health   *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
}

type walgBackup struct {
	BackupName string    `json:"backup_name"`
	Time       time.Time `json:"time"`
}

type patroniConfig struct {
	Name    string `yaml:"name"`
	Scope   string `yaml:"scope"`
	RestAPI struct {
		ConnectAddress string `yaml:"connect_address"`
		CertFile       string `yaml:"certfile"`
		KeyFile        string `yaml:"keyfile"`
		CAFile         string `yaml:"cafile"`
	} `yaml:"restapi"`
}

type patroniMember struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	State    string `json:"state"`
	Host     string `json:"host"`
	Port     int64  `json:"port"`
	Timeline int64  `json:"timeline"`
}

type patroniClusterResponse struct {
	Members []patroniMember `json:"members"`
	Scope   string          `json:"scope"`
}

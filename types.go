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

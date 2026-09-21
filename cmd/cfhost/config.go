package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type CFSTConfig struct {
	Threads           int     `json:"threads"`
	PingTimes         int     `json:"pingTimes"`
	DownloadCount     int     `json:"downloadCount"`
	DownloadSeconds   int     `json:"downloadSeconds"`
	MaxDelayMS        int     `json:"maxDelayMs"`
	MaxLossRate       float64 `json:"maxLossRate"`
	MinSpeedMB        float64 `json:"minSpeedMB"`
	DownloadURL       string  `json:"downloadUrl"`
	IPv6              bool    `json:"ipv6"`
	RunTimeoutMinutes int     `json:"runTimeoutMinutes"`
}

type RepairConfig struct {
	CandidateTTLMinutes      int `json:"candidateTTLMinutes"`
	FailureThreshold         int `json:"failureThreshold"`
	RefreshCooldownMinutes   int `json:"refreshCooldownMinutes"`
	RefreshMaxBackoffMinutes int `json:"refreshMaxBackoffMinutes"`
}

type VerifyConfig struct {
	HTTPRetries      int    `json:"httpRetries"`
	CandidateLimit   int    `json:"candidateLimit"`
	StrictHTTP       bool   `json:"strictHttp"`
	MinBodyBytes     int    `json:"minBodyBytes"`
	MaxRedirects     int    `json:"maxRedirects"`
	BlockPatterns    string `json:"blockPatterns"`
}

type BandwidthConfig struct {
	MaxLossRate float64 `json:"maxLossRate"`
	MaxDelayMS  int     `json:"maxDelayMs"`
	MinSpeedMB  float64 `json:"minSpeedMB"`
}

type OptimizeConfig struct {
	Enabled         bool `json:"enabled,omitempty"` // legacy; automatic global optimize is retired by default
	ScheduledFull   bool `json:"scheduledFull"`
	IntervalMinutes int  `json:"intervalMinutes"`
	RetryMinutes    int  `json:"retryMinutes"`
}

type HostsConfig struct {
	BackupRetention    int    `json:"backupRetention"`
	LegacyStateMapPath string `json:"legacyStateMapPath"`
}

type DownloaderClientConfig struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type TrackerConfig struct {
	RealAnnounce            bool                   `json:"realAnnounce"`
	SamplesPath             string                 `json:"samplesPath"`
	AutoDiscover            bool                   `json:"autoDiscover"`
	AutoSamplesPath         string                 `json:"autoSamplesPath"`
	DiscoveryTimeoutSeconds int                    `json:"discoveryTimeoutSeconds"`
	MaxTrackerLookups       int                    `json:"maxTrackerLookups"`
	Transmission            DownloaderClientConfig `json:"transmission"`
	QBittorrent             DownloaderClientConfig `json:"qbittorrent"`
	Retries                 int                    `json:"retries"`
	UserAgent               string                 `json:"userAgent"`
	PeerIDPrefix            string                 `json:"peerIdPrefix"`
	AnnouncePort            int                    `json:"announcePort"`
}

type SyncConfig struct {
	Enabled       bool   `json:"enabled"`
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	TokenFile     string `json:"tokenFile"`
	CommitMessage string `json:"commitMessage"`
}

type Domain struct {
	Host     string `json:"host"`
	Group    string `json:"group"`
	Class    string `json:"class"`
	Mode     string `json:"mode"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

type Config struct {
	Listen                string          `json:"listen"`
	AutoRepair            bool            `json:"autoRepair"`
	RepairIntervalMinutes int             `json:"repairIntervalMinutes"`
	AutoApply             bool            `json:"autoApply"`
	HostsPath             string          `json:"hostsPath"`
	VerifyTimeoutSeconds  int             `json:"verifyTimeoutSeconds"`
	CFST                  CFSTConfig      `json:"cfst"`
	Repair                RepairConfig    `json:"repair"`
	Verify                VerifyConfig    `json:"verify"`
	Bandwidth             BandwidthConfig `json:"bandwidth"`
	Optimize              OptimizeConfig  `json:"optimize"`
	Hosts                 HostsConfig     `json:"hosts"`
	Tracker               TrackerConfig   `json:"tracker"`
	Sync                  SyncConfig      `json:"sync"`
	Domains               []Domain        `json:"domains"`
}

func defaultConfig() Config {
	hostsPath := getenv("HOSTS_PATH", "/host/etc/hosts")
	return Config{
		Listen:                ":8080",
		AutoRepair:            false,
		RepairIntervalMinutes: 15,
		AutoApply:             false,
		HostsPath:             hostsPath,
		VerifyTimeoutSeconds:  8,
		CFST: CFSTConfig{
			Threads:           200,
			PingTimes:         4,
			DownloadCount:     20,
			DownloadSeconds:   5,
			MaxDelayMS:        350,
			MaxLossRate:       0.2,
			MinSpeedMB:        0.1,
			DownloadURL:       "https://cf.xiu2.xyz/url",
			RunTimeoutMinutes: 30,
		},
		Repair: RepairConfig{
			CandidateTTLMinutes:      1440,
			FailureThreshold:         2,
			RefreshCooldownMinutes:   60,
			RefreshMaxBackoffMinutes: 720,
		},
		Verify: VerifyConfig{
			HTTPRetries:    2,
			CandidateLimit: 10,
			StrictHTTP:     true,
			MinBodyBytes:   512,
			MaxRedirects:   3,
			BlockPatterns:  "just a moment|attention required|checking your browser|cf-error-details|站点创建成功|welcome to nginx|default page|it works!|index of /|domain (is )?for sale|this domain is parked",
		},
		Bandwidth: BandwidthConfig{
			MaxLossRate: 0,
			MaxDelayMS:  180,
			MinSpeedMB:  0.5,
		},
		Optimize: OptimizeConfig{
			Enabled:         false,
			ScheduledFull:   false,
			IntervalMinutes: 1440,
			RetryMinutes:    60,
		},
		Hosts: HostsConfig{
			BackupRetention:    10,
			LegacyStateMapPath: "/data/legacy-hosts-map.tsv",
		},
		Tracker: TrackerConfig{
			RealAnnounce:            true,
			SamplesPath:             "/data/tracker-samples.tsv",
			AutoDiscover:            true,
			AutoSamplesPath:         "/data/tracker-samples.auto.tsv",
			DiscoveryTimeoutSeconds: 15,
			MaxTrackerLookups:       200,
			Retries:                 2,
			UserAgent:               "Transmission/4.1.3",
			PeerIDPrefix:            "-TR4130-",
			AnnouncePort:            51413,
		},
		Sync: SyncConfig{
			Enabled:       false,
			Repository:    "zyk1172/cloudflare-hosts-sync",
			Branch:        "main",
			TokenFile:     "/data/github-token",
			CommitMessage: "CFHost: update hosts map",
		},
		Domains: []Domain{
			{Host: "tracker.m-team.cc", Group: "mteam", Class: "latency", Mode: "tracker", Endpoint: "/announce", Enabled: true},
			{Host: "tracker.m-team.io", Group: "mteam", Class: "latency", Mode: "tracker", Endpoint: "/announce", Enabled: true},
			{Host: "kp.m-team.cc", Group: "mteam", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "zp.m-team.io", Group: "mteam", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "ob.m-team.cc", Group: "mteam", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "h5.m-team.cc", Group: "mteam", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "api.m-team.cc", Group: "mteam", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.ptcafe.club", Group: "ptcafe", Class: "latency", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "ptcafe.club", Group: "ptcafe", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.xingyungept.org", Group: "xingyungept", Class: "latency", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "pt.xingyungept.org", Group: "xingyungept", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "www.xingyungept.org", Group: "xingyungept", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.hdtime.org", Group: "hdtime", Class: "latency", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "hdtime.org", Group: "hdtime", Class: "latency", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "www.pttime.org", Group: "pttime", Class: "latency", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "ptzone.xyz", Group: "ptzone", Class: "latency", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
		},
	}
}

func normalizeConfig(c *Config) error {
	if c.Listen == "" { c.Listen = ":8080" }
	if c.RepairIntervalMinutes < 1 { c.RepairIntervalMinutes = 15 }
	if c.VerifyTimeoutSeconds < 1 { c.VerifyTimeoutSeconds = 8 }
	if c.CFST.Threads < 1 { c.CFST.Threads = 200 }
	if c.CFST.PingTimes < 1 { c.CFST.PingTimes = 4 }
	if c.CFST.DownloadCount < 1 { c.CFST.DownloadCount = 20 }
	if c.CFST.DownloadSeconds < 1 { c.CFST.DownloadSeconds = 5 }
	if c.CFST.MaxDelayMS < 1 { c.CFST.MaxDelayMS = 350 }
	if c.CFST.MaxLossRate < 0 || c.CFST.MaxLossRate > 1 { return errors.New("maxLossRate must be between 0 and 1") }
	if c.CFST.MinSpeedMB < 0 { return errors.New("minSpeedMB cannot be negative") }
	if c.CFST.RunTimeoutMinutes < 1 { c.CFST.RunTimeoutMinutes = 30 }

	if c.Repair.CandidateTTLMinutes < 1 { c.Repair.CandidateTTLMinutes = 1440 }
	if c.Repair.FailureThreshold < 1 { c.Repair.FailureThreshold = 2 }
	if c.Repair.RefreshCooldownMinutes < 1 { c.Repair.RefreshCooldownMinutes = 60 }
	if c.Repair.RefreshMaxBackoffMinutes < c.Repair.RefreshCooldownMinutes { c.Repair.RefreshMaxBackoffMinutes = c.Repair.RefreshCooldownMinutes }

	if c.Verify.HTTPRetries < 1 { c.Verify.HTTPRetries = 2 }
	if c.Verify.CandidateLimit < 1 { c.Verify.CandidateLimit = 10 }
	if c.Verify.MinBodyBytes < 0 { c.Verify.MinBodyBytes = 0 }
	if c.Verify.MaxRedirects < 0 { c.Verify.MaxRedirects = 0 }
	if c.Verify.BlockPatterns == "" { c.Verify.BlockPatterns = defaultConfig().Verify.BlockPatterns }
	if _, err := regexp.Compile("(?i)" + c.Verify.BlockPatterns); err != nil { return errors.New("invalid verify blockPatterns: " + err.Error()) }

	if c.Bandwidth.MaxLossRate < 0 || c.Bandwidth.MaxLossRate > 1 { return errors.New("bandwidth maxLossRate must be between 0 and 1") }
	if c.Bandwidth.MaxDelayMS < 1 { c.Bandwidth.MaxDelayMS = 180 }
	if c.Bandwidth.MinSpeedMB < 0 { c.Bandwidth.MinSpeedMB = 0.5 }

	// v0.x used optimize.enabled=true to schedule a global remap every 24h.
	// Keep the field readable for old config files, but do not migrate it into
	// ScheduledFull: automatic maintenance is per-domain repair by default.
	c.Optimize.Enabled = false
	if c.Optimize.IntervalMinutes < 1 { c.Optimize.IntervalMinutes = 1440 }
	if c.Optimize.RetryMinutes < 1 { c.Optimize.RetryMinutes = 60 }

	if c.Hosts.BackupRetention < 1 { c.Hosts.BackupRetention = 10 }
	if strings.TrimSpace(c.Hosts.LegacyStateMapPath) == "" { c.Hosts.LegacyStateMapPath = "/data/legacy-hosts-map.tsv" }

	trackerUnset := c.Tracker.SamplesPath == "" && c.Tracker.Retries == 0 && c.Tracker.UserAgent == "" && c.Tracker.PeerIDPrefix == "" && c.Tracker.AnnouncePort == 0
	if trackerUnset { c.Tracker.RealAnnounce = true }
	if c.Tracker.SamplesPath == "" { c.Tracker.SamplesPath = "/data/tracker-samples.tsv" }
	if c.Tracker.AutoSamplesPath == "" { c.Tracker.AutoSamplesPath = "/data/tracker-samples.auto.tsv" }
	if c.Tracker.DiscoveryTimeoutSeconds < 1 { c.Tracker.DiscoveryTimeoutSeconds = 15 }
	if c.Tracker.MaxTrackerLookups < 1 { c.Tracker.MaxTrackerLookups = 200 }
	c.Tracker.Transmission.URL = strings.TrimSpace(c.Tracker.Transmission.URL)
	c.Tracker.Transmission.Username = strings.TrimSpace(c.Tracker.Transmission.Username)
	c.Tracker.QBittorrent.URL = strings.TrimSpace(c.Tracker.QBittorrent.URL)
	c.Tracker.QBittorrent.Username = strings.TrimSpace(c.Tracker.QBittorrent.Username)
	if c.Tracker.AutoSamplesPath == c.Tracker.SamplesPath {
		return errors.New("tracker autoSamplesPath must differ from samplesPath")
	}
	if c.Tracker.Transmission.Enabled {
		if c.Tracker.Transmission.URL == "" { return errors.New("Transmission URL is required when tracker discovery is enabled") }
		if _, err := normalizeTransmissionURL(c.Tracker.Transmission.URL); err != nil { return err }
	}
	if c.Tracker.QBittorrent.Enabled {
		if c.Tracker.QBittorrent.URL == "" { return errors.New("qBittorrent URL is required when tracker discovery is enabled") }
		if _, err := normalizeBaseURL(c.Tracker.QBittorrent.URL, "qBittorrent"); err != nil { return err }
	}
	if c.Tracker.Retries < 1 { c.Tracker.Retries = 2 }
	if c.Tracker.UserAgent == "" { c.Tracker.UserAgent = "Transmission/4.1.3" }
	if c.Tracker.PeerIDPrefix == "" { c.Tracker.PeerIDPrefix = "-TR4130-" }
	if len(c.Tracker.PeerIDPrefix) >= 20 { return errors.New("tracker peerIdPrefix must be shorter than 20 bytes") }
	if c.Tracker.AnnouncePort < 1 || c.Tracker.AnnouncePort > 65535 { c.Tracker.AnnouncePort = 51413 }

	if strings.TrimSpace(c.Sync.Repository) == "" { c.Sync.Repository = "zyk1172/cloudflare-hosts-sync" }
	if strings.TrimSpace(c.Sync.Branch) == "" { c.Sync.Branch = "main" }
	if strings.TrimSpace(c.Sync.TokenFile) == "" { c.Sync.TokenFile = "/data/github-token" }
	if strings.TrimSpace(c.Sync.CommitMessage) == "" { c.Sync.CommitMessage = "CFHost: update hosts map" }

	seen := make(map[string]bool)
	clean := make([]Domain, 0, len(c.Domains))
	for _, d := range c.Domains {
		d.Host = strings.ToLower(strings.TrimSpace(d.Host))
		d.Group = strings.TrimSpace(d.Group)
		d.Mode = strings.ToLower(strings.TrimSpace(d.Mode))
		d.Endpoint = strings.TrimSpace(d.Endpoint)
		if d.Host == "" || seen[d.Host] { continue }
		if d.Class != "bandwidth" { d.Class = "latency" }
		if d.Mode != "tracker" { d.Mode = "http" }
		if d.Endpoint == "" { d.Endpoint = "/" }
		if !strings.HasPrefix(d.Endpoint, "/") { d.Endpoint = "/" + d.Endpoint }
		seen[d.Host] = true
		clean = append(clean, d)
	}
	c.Domains = clean
	return nil
}

func loadConfig(dataDir string) (Config, error) {
	path := filepath.Join(dataDir, "config.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := defaultConfig()
		if err := normalizeConfig(&c); err != nil { return Config{}, err }
		if err := saveConfig(dataDir, c); err != nil { return Config{}, err }
		return c, nil
	}
	if err != nil { return Config{}, err }

	var c Config
	if err := json.Unmarshal(b, &c); err != nil { return Config{}, err }
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	d := defaultConfig()
	if _, ok := raw["verify"]; !ok { c.Verify = d.Verify }
	if _, ok := raw["bandwidth"]; !ok { c.Bandwidth = d.Bandwidth }
	if _, ok := raw["optimize"]; !ok { c.Optimize = d.Optimize }
	if _, ok := raw["hosts"]; !ok { c.Hosts = d.Hosts }
	if trackerRaw, ok := raw["tracker"]; !ok {
		c.Tracker = d.Tracker
	} else {
		var trackerFields map[string]json.RawMessage
		_ = json.Unmarshal(trackerRaw, &trackerFields)
		if _, ok := trackerFields["autoDiscover"]; !ok { c.Tracker.AutoDiscover = d.Tracker.AutoDiscover }
		if _, ok := trackerFields["autoSamplesPath"]; !ok { c.Tracker.AutoSamplesPath = d.Tracker.AutoSamplesPath }
		if _, ok := trackerFields["discoveryTimeoutSeconds"]; !ok { c.Tracker.DiscoveryTimeoutSeconds = d.Tracker.DiscoveryTimeoutSeconds }
		if _, ok := trackerFields["maxTrackerLookups"]; !ok { c.Tracker.MaxTrackerLookups = d.Tracker.MaxTrackerLookups }
	}
	if err := normalizeConfig(&c); err != nil { return Config{}, err }
	return c, nil
}

func saveConfig(dataDir string, c Config) error {
	if err := normalizeConfig(&c); err != nil { return err }
	return writeJSON(filepath.Join(dataDir, "config.json"), c)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { return err }
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil { return err }
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0644); err != nil { return err }
	return os.Rename(tmp, path)
}

func getenv(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" { return v }
	return fallback
}

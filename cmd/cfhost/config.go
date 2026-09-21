package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	CandidateTTLMinutes        int `json:"candidateTTLMinutes"`
	FailureThreshold           int `json:"failureThreshold"`
	RefreshCooldownMinutes     int `json:"refreshCooldownMinutes"`
	RefreshMaxBackoffMinutes   int `json:"refreshMaxBackoffMinutes"`
}

type TrackerConfig struct {
	RealAnnounce  bool   `json:"realAnnounce"`
	SamplesPath   string `json:"samplesPath"`
	Retries       int    `json:"retries"`
	UserAgent     string `json:"userAgent"`
	PeerIDPrefix  string `json:"peerIdPrefix"`
	AnnouncePort  int    `json:"announcePort"`
}

type Domain struct {
	Host     string `json:"host"`
	Group    string `json:"group"`
	Mode     string `json:"mode"`
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

type Config struct {
	Listen                string        `json:"listen"`
	AutoRepair            bool          `json:"autoRepair"`
	RepairIntervalMinutes int           `json:"repairIntervalMinutes"`
	AutoApply             bool          `json:"autoApply"`
	HostsPath             string        `json:"hostsPath"`
	VerifyTimeoutSeconds  int           `json:"verifyTimeoutSeconds"`
	CFST                  CFSTConfig    `json:"cfst"`
	Repair                RepairConfig  `json:"repair"`
	Tracker               TrackerConfig `json:"tracker"`
	Domains               []Domain      `json:"domains"`
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
		Tracker: TrackerConfig{
			RealAnnounce: true,
			SamplesPath:  "/data/tracker-samples.tsv",
			Retries:      2,
			UserAgent:    "Transmission/4.1.3",
			PeerIDPrefix: "-TR4130-",
			AnnouncePort: 51413,
		},
		Domains: []Domain{
			{Host: "tracker.m-team.cc", Group: "mteam", Mode: "tracker", Endpoint: "/announce", Enabled: true},
			{Host: "tracker.m-team.io", Group: "mteam", Mode: "tracker", Endpoint: "/announce", Enabled: true},
			{Host: "kp.m-team.cc", Group: "mteam", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "zp.m-team.io", Group: "mteam", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "ob.m-team.cc", Group: "mteam", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "h5.m-team.cc", Group: "mteam", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "api.m-team.cc", Group: "mteam", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.ptcafe.club", Group: "ptcafe", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "ptcafe.club", Group: "ptcafe", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.xingyungept.org", Group: "xingyungept", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "pt.xingyungept.org", Group: "xingyungept", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "www.xingyungept.org", Group: "xingyungept", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "tracker.hdtime.org", Group: "hdtime", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "hdtime.org", Group: "hdtime", Mode: "http", Endpoint: "/", Enabled: true},
			{Host: "www.pttime.org", Group: "pttime", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
			{Host: "ptzone.xyz", Group: "ptzone", Mode: "tracker", Endpoint: "/announce.php", Enabled: true},
		},
	}
}

func normalizeConfig(c *Config) error {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.RepairIntervalMinutes < 1 {
		c.RepairIntervalMinutes = 15
	}
	if c.VerifyTimeoutSeconds < 1 {
		c.VerifyTimeoutSeconds = 8
	}
	if c.CFST.Threads < 1 {
		c.CFST.Threads = 200
	}
	if c.CFST.PingTimes < 1 {
		c.CFST.PingTimes = 4
	}
	if c.CFST.DownloadCount < 1 {
		c.CFST.DownloadCount = 20
	}
	if c.CFST.DownloadSeconds < 1 {
		c.CFST.DownloadSeconds = 5
	}
	if c.CFST.MaxDelayMS < 1 {
		c.CFST.MaxDelayMS = 350
	}
	if c.CFST.MaxLossRate < 0 || c.CFST.MaxLossRate > 1 {
		return errors.New("maxLossRate must be between 0 and 1")
	}
	if c.CFST.MinSpeedMB < 0 {
		return errors.New("minSpeedMB cannot be negative")
	}
	if c.CFST.RunTimeoutMinutes < 1 {
		c.CFST.RunTimeoutMinutes = 30
	}

	if c.Repair.CandidateTTLMinutes < 1 {
		c.Repair.CandidateTTLMinutes = 1440
	}
	if c.Repair.FailureThreshold < 1 {
		c.Repair.FailureThreshold = 2
	}
	if c.Repair.RefreshCooldownMinutes < 1 {
		c.Repair.RefreshCooldownMinutes = 60
	}
	if c.Repair.RefreshMaxBackoffMinutes < c.Repair.RefreshCooldownMinutes {
		c.Repair.RefreshMaxBackoffMinutes = 720
	}

	trackerUnset := c.Tracker.SamplesPath == "" && c.Tracker.Retries == 0 &&
		c.Tracker.UserAgent == "" && c.Tracker.PeerIDPrefix == "" && c.Tracker.AnnouncePort == 0
	if trackerUnset {
		c.Tracker.RealAnnounce = true
	}
	if c.Tracker.SamplesPath == "" {
		c.Tracker.SamplesPath = "/data/tracker-samples.tsv"
	}
	if c.Tracker.Retries < 1 {
		c.Tracker.Retries = 2
	}
	if c.Tracker.UserAgent == "" {
		c.Tracker.UserAgent = "Transmission/4.1.3"
	}
	if c.Tracker.PeerIDPrefix == "" {
		c.Tracker.PeerIDPrefix = "-TR4130-"
	}
	if len(c.Tracker.PeerIDPrefix) >= 20 {
		return errors.New("tracker peerIdPrefix must be shorter than 20 bytes")
	}
	if c.Tracker.AnnouncePort < 1 || c.Tracker.AnnouncePort > 65535 {
		c.Tracker.AnnouncePort = 51413
	}

	seen := make(map[string]bool)
	clean := make([]Domain, 0, len(c.Domains))
	for _, d := range c.Domains {
		d.Host = strings.ToLower(strings.TrimSpace(d.Host))
		d.Group = strings.TrimSpace(d.Group)
		d.Mode = strings.ToLower(strings.TrimSpace(d.Mode))
		d.Endpoint = strings.TrimSpace(d.Endpoint)
		if d.Host == "" || seen[d.Host] {
			continue
		}
		if d.Mode != "tracker" {
			d.Mode = "http"
		}
		if d.Endpoint == "" {
			d.Endpoint = "/"
		}
		if !strings.HasPrefix(d.Endpoint, "/") {
			d.Endpoint = "/" + d.Endpoint
		}
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
		if err := saveConfig(dataDir, c); err != nil {
			return Config{}, err
		}
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if err := normalizeConfig(&c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func saveConfig(dataDir string, c Config) error {
	if err := normalizeConfig(&c); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dataDir, "config.json"), c)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func getenv(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

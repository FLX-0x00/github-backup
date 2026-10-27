package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	appVersion = "1.1.0"
	configVer  = 1
)

type Schedule struct {
	Enabled bool `json:"enabled"`
	Weekday int  `json:"weekday"` // 0 = Sunday ... 6 = Saturday
	Hour    int  `json:"hour"`
	Minute  int  `json:"minute"`
}

type Options struct {
	IncludeWikis bool `json:"include_wikis"`
	PruneDeleted bool `json:"prune_deleted"`
	Notify       bool `json:"notify"`
	Concurrency  int  `json:"concurrency"`
	KeepLogDays  int  `json:"keep_log_days"`
	GitRetries   int  `json:"git_retries"`
}

type Connection struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Kind       string     `json:"kind"` // user, org, classic
	Owner      string     `json:"owner"`
	Token      string     `json:"token"`
	Mode       string     `json:"mode"` // all, selected
	Repos      []string   `json:"repos,omitempty"`
	Path       string     `json:"path"`
	Enabled    bool       `json:"enabled"`
	CreatedAt  time.Time  `json:"created_at"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
}

type Config struct {
	Version     int          `json:"version"`
	DataRoot    string       `json:"data_root"`
	APIBase     string       `json:"api_base"`
	Schedule    Schedule     `json:"schedule"`
	Options     Options      `json:"options"`
	Connections []Connection `json:"connections"`
	LastRun     *time.Time   `json:"last_run,omitempty"`
	LastStatus  string       `json:"last_status,omitempty"`
}

func defaultConfig(defaultDataRoot string) *Config {
	if strings.TrimSpace(defaultDataRoot) == "" {
		defaultDataRoot = "/volume1/backup/github"
	}
	return &Config{
		Version:  configVer,
		DataRoot: filepath.Clean(defaultDataRoot),
		APIBase:  "https://api.github.com",
		Schedule: Schedule{
			Enabled: true,
			Weekday: 0,
			Hour:    3,
			Minute:  0,
		},
		Options: Options{
			IncludeWikis: true,
			PruneDeleted: false,
			Notify:       true,
			Concurrency:  1,
			KeepLogDays:  90,
			GitRetries:   2,
		},
		Connections: []Connection{},
	}
}

func loadConfig(path, defaultDataRoot string) (*Config, error) {
	cfg := defaultConfig(defaultDataRoot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config lesen: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = configVer
	}
	if cfg.DataRoot == "" {
		cfg.DataRoot = defaultDataRoot
	}
	if cfg.APIBase == "" {
		cfg.APIBase = "https://api.github.com"
	}
	cfg.DataRoot = filepath.Clean(cfg.DataRoot)
	cfg.Schedule = normalizeSchedule(cfg.Schedule)
	cfg.Options = normalizeOptions(cfg.Options)
	if cfg.Connections == nil {
		cfg.Connections = []Connection{}
	}
	for i := range cfg.Connections {
		if cfg.Connections[i].CreatedAt.IsZero() {
			cfg.Connections[i].CreatedAt = time.Now()
		}
		cfg.Connections[i].normalizeRepos()
	}
	return cfg, nil
}

func normalizeSchedule(s Schedule) Schedule {
	if s.Weekday < 0 || s.Weekday > 6 {
		s.Weekday = 0
	}
	if s.Hour < 0 || s.Hour > 23 {
		s.Hour = 3
	}
	if s.Minute < 0 || s.Minute > 59 {
		s.Minute = 0
	}
	return s
}

func normalizeOptions(o Options) Options {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.Concurrency > 8 {
		o.Concurrency = 8
	}
	if o.KeepLogDays < 1 {
		o.KeepLogDays = 90
	}
	if o.KeepLogDays > 3650 {
		o.KeepLogDays = 3650
	}
	if o.GitRetries < 1 {
		o.GitRetries = 2
	}
	if o.GitRetries > 10 {
		o.GitRetries = 10
	}
	return o
}

func saveConfig(path string, cfg *Config) error {
	cfg.Version = configVer
	cfg.Schedule = normalizeSchedule(cfg.Schedule)
	cfg.Options = normalizeOptions(cfg.Options)
	return writeFileAtomic(path, mustJSON(cfg), 0600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func mustJSON(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._-")
	if out == "" {
		out = "space"
	}
	return out
}

func (c *Connection) defaultPath(dataRoot string) string {
	owner := sanitizeName(c.Owner)
	if c.Kind == "classic" {
		return filepath.Clean(dataRoot)
	}
	return filepath.Join(dataRoot, owner)
}

func (c *Connection) effectivePath(dataRoot string) string {
	if strings.TrimSpace(c.Path) != "" {
		return filepath.Clean(c.Path)
	}
	return c.defaultPath(dataRoot)
}

func (c *Connection) normalizeRepos() {
	if c.Mode != "selected" {
		c.Mode = "all"
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(c.Repos))
	for _, r := range c.Repos {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	c.Repos = out
	if c.Mode == "selected" && len(c.Repos) == 0 {
		c.Mode = "all"
	}
}

func validateAbsolutePath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("Pfad muss absolut sein: %s", p)
	}
	clean := filepath.Clean(p)
	if clean == "/" {
		return errors.New("Pfad darf nicht / sein")
	}
	return nil
}

func (c *Config) connectionByID(id string) *Connection {
	for i := range c.Connections {
		if c.Connections[i].ID == id {
			return &c.Connections[i]
		}
	}
	return nil
}

func (c *Config) connectionByLabel(label string) *Connection {
	for i := range c.Connections {
		if strings.EqualFold(c.Connections[i].Label, label) {
			return &c.Connections[i]
		}
	}
	return nil
}

func (c *Config) removeConnection(id string) bool {
	for i := range c.Connections {
		if c.Connections[i].ID == id {
			c.Connections = append(c.Connections[:i], c.Connections[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Config) clone() *Config {
	b := mustJSON(c)
	var out Config
	_ = json.Unmarshal(b, &out)
	return &out
}

func parseRepos(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	_ = syscall.Umask(0077)

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "add-connection":
		err = cmdAddConnection(args)
	case "list":
		err = cmdList(args)
	case "select":
		err = cmdSelect(args)
	case "remove":
		err = cmdRemove(args)
	case "run":
		err = cmdRun(args)
	case "daemon":
		err = cmdDaemon(args)
	case "serve":
		err = cmdServe(args)
	case "test":
		err = cmdTest(args)
	case "config":
		err = cmdConfig(args)
	case "set-token":
		err = cmdSetToken(args)
	case "schedule":
		err = cmdSchedule(args)
	case "version":
		fmt.Printf("github-backup %s\n", appVersion)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "Unbekannter Befehl: %s\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "FEHLER: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`github-backup - GitHub-Mirror-Backup für Synology DSM

Befehle:
  init             Konfiguration anlegen (Token wird geprüft)
  add-connection   weitere Verbindung (z. B. weitere Org) hinzufügen
  list             Repositories einer Verbindung auflisten
  select           Repo-Auswahl ändern (--all oder --repos "owner/repo,...")
  remove           Verbindung entfernen
  run              Backup jetzt ausführen
  daemon           Zeitplan-Daemon (ohne UI)
  serve            UI + Zeitplan (wird vom Paket gestartet)
  test             Tokens/Verbindungen prüfen
  config           Speicherort/Optionen/Zeitplan ändern
  set-token        Token einer Verbindung ersetzen
  schedule         Wochentag/Uhrzeit ändern
  version          Version anzeigen

Wichtige Flags:
  --config PATH    Konfigurationsdatei (Default: Paket-Var oder ./github-backup.json)
  --data-root PATH Zielordner (Default: /volume1/backup/github)
  --kind           classic | user | org (Default: classic)
  --owner          Org/User bei fine-grained Tokens
  --repos          Kommagetrennte Repos; leer = alle (inkl. neue)
  --weekday        0=So ... 6=Sa (Default: 0)
  --hour           0-23 (Default: 3)
  --minute         0-59 (Default: 0)
  --no-schedule    Zeitplan deaktivieren
  --wikis=1|0      (Default: 1)
  --prune=1|0      (Default: 0)
  --notify=1|0     (Default: 1)
  --token-file     Token-Datei (sonst Umgebungsvariable GITHUB_TOKEN)

Beispiel:
  GITHUB_TOKEN=ghp_xxx github-backup init --kind classic \
    --data-root /volume1/backup/github --weekday 0 --hour 3 --minute 0 --wikis=1
`)
}

func defaultConfigPath() string {
	if p := os.Getenv("GITHUB_BACKUP_CONFIG"); p != "" {
		return p
	}
	if st, err := os.Stat("/var/packages/github-backup"); err == nil && st.IsDir() {
		return "/var/packages/github-backup/var/config.json"
	}
	return "github-backup.json"
}

type commonFlags struct {
	config      string
	tokenFile   string
	kind        string
	owner       string
	dataRoot    string
	apiBase     string
	path        string
	label       string
	repos       string
	weekday     int
	hour        int
	minute      int
	noSchedule  bool
	wikis       bool
	prune       bool
	notify      bool
	concurrency int
	keepLogs    int
	retries     int
	force       bool
}

func bindCommon(fs *flag.FlagSet) *commonFlags {
	c := &commonFlags{}
	fs.StringVar(&c.config, "config", defaultConfigPath(), "Konfigurationsdatei")
	fs.StringVar(&c.tokenFile, "token-file", "", "Token-Datei")
	fs.StringVar(&c.kind, "kind", "classic", "classic, user oder org")
	fs.StringVar(&c.owner, "owner", "", "Owner/Org für fine-grained Token")
	fs.StringVar(&c.dataRoot, "data-root", "/volume1/backup/github", "Zielordner")
	fs.StringVar(&c.apiBase, "api-base", "", "GitHub API Base URL")
	fs.StringVar(&c.path, "path", "", "optionaler Verbindungsordner")
	fs.StringVar(&c.label, "label", "", "Anzeigename der Verbindung")
	fs.StringVar(&c.repos, "repos", "", "kommagetrennte Repo-Auswahl; leer = alle")
	fs.IntVar(&c.weekday, "weekday", 0, "0=So ... 6=Sa")
	fs.IntVar(&c.hour, "hour", 3, "Stunde 0-23")
	fs.IntVar(&c.minute, "minute", 0, "Minute 0-59")
	fs.BoolVar(&c.noSchedule, "no-schedule", false, "Zeitplan deaktivieren")
	fs.BoolVar(&c.wikis, "wikis", true, "Wikis sichern")
	fs.BoolVar(&c.prune, "prune", false, "gelöschte Refs im Backup löschen")
	fs.BoolVar(&c.notify, "notify", true, "DSM-Benachrichtigung senden")
	fs.IntVar(&c.concurrency, "concurrency", 1, "parallele Git-Operationen (1-8)")
	fs.IntVar(&c.keepLogs, "keep-logs", 90, "Log-Aufbewahrung in Tagen")
	fs.IntVar(&c.retries, "retries", 2, "Git-Wiederholungen")
	fs.BoolVar(&c.force, "force", false, "existierende Config überschreiben")
	return c
}

func readToken(path string) (string, error) {
	if p := os.Getenv("GITHUB_TOKEN"); strings.TrimSpace(p) != "" {
		return strings.TrimSpace(p), nil
	}
	if path == "" {
		return "", errors.New("kein Token: GITHUB_TOKEN setzen oder --token-file angeben")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("Token-Datei ist leer")
	}
	return token, nil
}

func validateSchedule(weekday, hour, minute int) error {
	if weekday < 0 || weekday > 6 {
		return errors.New("weekday muss 0-6 sein")
	}
	if hour < 0 || hour > 23 {
		return errors.New("hour muss 0-23 sein")
	}
	if minute < 0 || minute > 59 {
		return errors.New("minute muss 0-59 sein")
	}
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateSchedule(c.weekday, c.hour, c.minute); err != nil {
		return err
	}
	if err := validateAbsolutePath(c.dataRoot); err != nil {
		return err
	}
	if c.path != "" {
		if err := validateAbsolutePath(c.path); err != nil {
			return err
		}
	}
	if st, err := os.Stat(c.config); err == nil && !st.IsDir() && !c.force {
		return fmt.Errorf("Config existiert bereits: %s (--force zum Überschreiben)", c.config)
	}

	token, err := readToken(c.tokenFile)
	if err != nil {
		return err
	}
	kind, owner, err := normalizeKind(c.kind, c.owner)
	if err != nil {
		return err
	}
	apiBase := firstNonEmpty(c.apiBase, "https://api.github.com")
	client := newGitHubClient(apiBase)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	login, repos, err := client.Validate(ctx, kind, owner, token)
	if err != nil {
		return fmt.Errorf("Token/Verbindung prüfen: %w", err)
	}
	owner = login
	if len(repos) == 0 {
		fmt.Println("WARNUNG: Der Token sieht keine Repositories. Bei fine-grained ggf. den Token-Typ/Resource Owner prüfen.")
	}
	selected := parseRepos(c.repos)
	if len(selected) > 0 {
		if err := validateSelected(selected, repos); err != nil {
			return err
		}
	}
	mode := "all"
	if len(selected) > 0 {
		mode = "selected"
	}

	cfg := defaultConfig(c.dataRoot)
	cfg.APIBase = apiBase
	cfg.Schedule = Schedule{Enabled: !c.noSchedule, Weekday: c.weekday, Hour: c.hour, Minute: c.minute}
	cfg.Options = normalizeOptions(Options{
		IncludeWikis: c.wikis,
		PruneDeleted: c.prune,
		Notify:       c.notify,
		Concurrency:  c.concurrency,
		KeepLogDays:  c.keepLogs,
		GitRetries:   c.retries,
	})
	label := c.label
	if label == "" {
		switch kind {
		case "classic":
			label = "Alles (classic)"
		case "org":
			label = "Org: " + owner
		default:
			label = owner
		}
	}
	cfg.Connections = []Connection{{
		ID:        newID(),
		Label:     label,
		Kind:      kind,
		Owner:     owner,
		Token:     token,
		Mode:      mode,
		Repos:     selected,
		Path:      c.path,
		Enabled:   true,
		CreatedAt: time.Now(),
	}}
	if err := ensureDataDirs(cfg.DataRoot); err != nil {
		return err
	}
	if err := saveConfig(c.config, cfg); err != nil {
		return err
	}
	fmt.Printf("Konfiguration angelegt: %s\n", c.config)
	fmt.Printf("Verbindung: %s (%s/%s), Repos sichtbar: %d, Modus: %s\n", label, kind, owner, len(repos), mode)
	fmt.Printf("Ziel: %s\n", cfg.DataRoot)
	return nil
}

func cmdAddConnection(args []string) error {
	fs := flag.NewFlagSet("add-connection", flag.ContinueOnError)
	c := bindCommon(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateAbsolutePath(c.dataRoot); err != nil {
		return err
	}
	if c.path != "" {
		if err := validateAbsolutePath(c.path); err != nil {
			return err
		}
	}
	token, err := readToken(c.tokenFile)
	if err != nil {
		return err
	}
	kind, owner, err := normalizeKind(c.kind, c.owner)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(c.config, c.dataRoot)
	if err != nil {
		return err
	}
	if c.apiBase != "" {
		cfg.APIBase = c.apiBase
	}
	client := newGitHubClient(firstNonEmpty(c.apiBase, cfg.APIBase))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	login, repos, err := client.Validate(ctx, kind, owner, token)
	if err != nil {
		return fmt.Errorf("Token/Verbindung prüfen: %w", err)
	}
	owner = login
	if len(repos) == 0 {
		fmt.Println("WARNUNG: Der Token sieht keine Repositories. Bei fine-grained ggf. den Token-Typ/Resource Owner prüfen.")
	}
	selected := parseRepos(c.repos)
	if len(selected) > 0 {
		if err := validateSelected(selected, repos); err != nil {
			return err
		}
	}
	mode := "all"
	if len(selected) > 0 {
		mode = "selected"
	}
	label := c.label
	if label == "" {
		switch kind {
		case "classic":
			label = "Alles (classic)"
		case "org":
			label = "Org: " + owner
		default:
			label = owner
		}
	}
	cfg.Connections = append(cfg.Connections, Connection{
		ID: newID(), Label: label, Kind: kind, Owner: owner, Token: token,
		Mode: mode, Repos: selected, Path: c.path, Enabled: true, CreatedAt: time.Now(),
	})
	if err := ensureDataDirs(cfg.DataRoot); err != nil {
		return err
	}
	if err := saveConfig(c.config, cfg); err != nil {
		return err
	}
	fmt.Printf("Verbindung hinzugefügt: %s (%s/%s), Repos sichtbar: %d, Modus: %s\n", label, kind, owner, len(repos), mode)
	return nil
}

func normalizeKind(kind, owner string) (string, string, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "classic", "all":
		return "classic", "", nil
	case "user", "personal":
		return "user", strings.TrimSpace(owner), nil
	case "org", "organization":
		owner = strings.TrimSpace(owner)
		if owner == "" {
			return "", "", errors.New("--owner (Organisation) fehlt")
		}
		return "org", owner, nil
	default:
		return "", "", fmt.Errorf("ungültiger --kind %q (classic, user, org)", kind)
	}
}

func validateSelected(selected []string, repos []Repo) error {
	have := map[string]bool{}
	for _, r := range repos {
		have[r.FullName] = true
	}
	var missing []string
	for _, s := range selected {
		if !validRepoFullName(s) {
			return fmt.Errorf("ungültiger Repo-Name: %s", s)
		}
		if !have[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("nicht gefunden oder kein Zugriff: %s", strings.Join(missing, ", "))
	}
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	connID := fs.String("connection", "", "nur diese Verbindung (ID/Label)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	client := newGitHubClient(cfg.APIBase)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for i := range cfg.Connections {
		c := &cfg.Connections[i]
		if *connID != "" && c.ID != *connID && !strings.EqualFold(c.Label, *connID) {
			continue
		}
		fmt.Printf("## %s (%s/%s)\n", c.Label, c.Kind, c.Owner)
		repos, err := client.listRepos(ctx, c.Token, c.Kind, c.Owner)
		if err != nil {
			fmt.Printf("FEHLER: %v\n", err)
			continue
		}
		for _, r := range repos {
			flags := []string{}
			if r.Private {
				flags = append(flags, "private")
			}
			if r.Fork {
				flags = append(flags, "fork")
			}
			if r.Archived {
				flags = append(flags, "archived")
			}
			fmt.Printf("%s\t%s\n", r.FullName, strings.Join(flags, ","))
		}
	}
	return nil
}

func cmdSelect(args []string) error {
	fs := flag.NewFlagSet("select", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	connID := fs.String("connection", "", "Verbindung (ID/Label)")
	all := fs.Bool("all", false, "alle Repos inkl. neue")
	reposFlag := fs.String("repos", "", "kommagetrennte Repos")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	conn, err := findConnection(cfg, *connID)
	if err != nil {
		return err
	}
	if *all {
		conn.Mode = "all"
		conn.Repos = nil
		if err := saveConfig(*config, cfg); err != nil {
			return err
		}
		fmt.Printf("%s: Modus=all\n", conn.Label)
		return nil
	}
	selected := parseRepos(*reposFlag)
	if len(selected) == 0 {
		return errors.New("--repos oder --all angeben")
	}
	client := newGitHubClient(cfg.APIBase)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repos, err := client.listRepos(ctx, conn.Token, conn.Kind, conn.Owner)
	if err != nil {
		return err
	}
	if err := validateSelected(selected, repos); err != nil {
		return err
	}
	conn.Mode = "selected"
	conn.Repos = selected
	if err := saveConfig(*config, cfg); err != nil {
		return err
	}
	fmt.Printf("%s: %d Repo(s) ausgewählt\n", conn.Label, len(selected))
	return nil
}

func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	connID := fs.String("connection", "", "Verbindung (ID/Label)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	conn, err := findConnection(cfg, *connID)
	if err != nil {
		return err
	}
	cfg.removeConnection(conn.ID)
	if err := saveConfig(*config, cfg); err != nil {
		return err
	}
	fmt.Printf("Verbindung entfernt: %s\n", conn.Label)
	return nil
}

func findConnection(cfg *Config, id string) (*Connection, error) {
	if id == "" {
		if len(cfg.Connections) == 1 {
			return &cfg.Connections[0], nil
		}
		return nil, errors.New("--connection angeben")
	}
	for i := range cfg.Connections {
		if cfg.Connections[i].ID == id || strings.EqualFold(cfg.Connections[i].Label, id) {
			return &cfg.Connections[i], nil
		}
	}
	return nil, fmt.Errorf("Verbindung nicht gefunden: %s", id)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	connID := fs.String("connection", "", "nur diese Verbindung (ID/Label, Komma)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	only := parseRepos(*connID)
	r, err := newRunner(*config, cfg, true, nil)
	if err != nil {
		return err
	}
	sum, err := r.Run(ctx, only, false)
	fmt.Printf("Status: %s, Repos: %d, neu: %d, aktualisiert: %d, Fehler: %d\n", sum.Status, sum.Total, sum.Cloned, sum.Updated, sum.Failed)
	fmt.Printf("Log: %s\n", sum.LogPath)
	return err
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	unixPath := fs.String("unix", "", "Unix-Socket für DSM-nginx")
	pidFile := fs.String("pidfile", "", "PID-Datei")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *unixPath == "" {
		return errors.New("--unix fehlt")
	}
	app, err := newServerApp(*config)
	if err != nil {
		return err
	}
	return app.serve(*unixPath, *pidFile)
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	pidFile := fs.String("pidfile", "", "PID-Datei")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pidFile != "" {
		_ = os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)
		defer os.Remove(*pidFile)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Printf("Daemon gestartet, Config: %s\n", *config)
	var retryAt time.Time
	for {
		cfg, err := loadConfig(*config, "/volume1/backup/github")
		if err != nil {
			fmt.Printf("Config-Fehler: %v\n", err)
		} else if cfg.Schedule.Enabled && dueNow(cfg) && (retryAt.IsZero() || !time.Now().Before(retryAt)) {
			fmt.Printf("Backup fällig: %s\n", time.Now().Format(time.RFC3339))
			r, err := newRunner(*config, cfg, false, nil)
			if err != nil {
				fmt.Printf("Runner-Fehler: %v\n", err)
				retryAt = time.Now().Add(30 * time.Minute)
			} else {
				sum, err := r.Run(ctx, nil, true)
				if err != nil {
					fmt.Printf("Backup-Fehler: %v\n", err)
					retryAt = time.Now().Add(30 * time.Minute)
				} else {
					retryAt = time.Time{}
				}
				fmt.Printf("Backup beendet: %s, Log: %s\n", sum.Status, sum.LogPath)
			}
		}
		select {
		case <-ctx.Done():
			fmt.Println("Daemon beendet.")
			return nil
		case <-time.After(30 * time.Second):
		}
	}
}

func dueNow(cfg *Config) bool {
	hasEnabled := false
	for _, c := range cfg.Connections {
		if c.Enabled {
			hasEnabled = true
			break
		}
	}
	if !hasEnabled {
		return false
	}
	if cfg.LastRun == nil {
		return true
	}
	next := nextRunAfter(*cfg.LastRun, cfg.Schedule)
	return !time.Now().Before(next)
}

func nextRunAfter(after time.Time, sch Schedule) time.Time {
	after = after.In(time.Local)
	t := time.Date(after.Year(), after.Month(), after.Day(), sch.Hour, sch.Minute, 0, 0, time.Local)
	for i := 0; i < 8; i++ {
		if t.Weekday() == time.Weekday(sch.Weekday) && t.After(after) {
			return t
		}
		t = t.AddDate(0, 0, 1)
	}
	return t
}

func parseOnOff(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("erwartet 1|0, true|false (bekommen: %q)", value)
	}
}

func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	dataRoot := fs.String("data-root", "", "neuer Zielordner")
	apiBase := fs.String("api-base", "", "neue API Base URL")
	weekday := fs.Int("weekday", -1, "0=So ... 6=Sa")
	hour := fs.Int("hour", -1, "Stunde 0-23")
	minute := fs.Int("minute", -1, "Minute 0-59")
	schedule := fs.String("schedule", "", "Zeitplan 1|0")
	wikis := fs.String("wikis", "", "Wikis 1|0")
	prune := fs.String("prune", "", "Prune 1|0")
	notify := fs.String("notify", "", "Benachrichtigung 1|0")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	if *dataRoot != "" {
		if err := validateAbsolutePath(*dataRoot); err != nil {
			return err
		}
		cfg.DataRoot = filepath.Clean(*dataRoot)
	}
	if *apiBase != "" {
		cfg.APIBase = *apiBase
	}
	if *weekday >= 0 {
		cfg.Schedule.Weekday = *weekday
	}
	if *hour >= 0 {
		cfg.Schedule.Hour = *hour
	}
	if *minute >= 0 {
		cfg.Schedule.Minute = *minute
	}
	if *schedule != "" {
		v, err := parseOnOff(*schedule)
		if err != nil {
			return err
		}
		cfg.Schedule.Enabled = v
	}
	if *wikis != "" {
		v, err := parseOnOff(*wikis)
		if err != nil {
			return err
		}
		cfg.Options.IncludeWikis = v
	}
	if *prune != "" {
		v, err := parseOnOff(*prune)
		if err != nil {
			return err
		}
		cfg.Options.PruneDeleted = v
	}
	if *notify != "" {
		v, err := parseOnOff(*notify)
		if err != nil {
			return err
		}
		cfg.Options.Notify = v
	}
	if err := validateSchedule(cfg.Schedule.Weekday, cfg.Schedule.Hour, cfg.Schedule.Minute); err != nil {
		return err
	}
	if err := ensureDataDirs(cfg.DataRoot); err != nil {
		return fmt.Errorf("Zielordner: %w", err)
	}
	if err := saveConfig(*config, cfg); err != nil {
		return err
	}
	fmt.Printf("Config: %s\nZiel: %s\nZeitplan: enabled=%v weekday=%d %02d:%02d\nOptionen: wikis=%v prune=%v notify=%v\n",
		*config, cfg.DataRoot, cfg.Schedule.Enabled, cfg.Schedule.Weekday, cfg.Schedule.Hour, cfg.Schedule.Minute,
		cfg.Options.IncludeWikis, cfg.Options.PruneDeleted, cfg.Options.Notify)
	if *dataRoot != "" {
		fmt.Println("Hinweis: Bereits vorhandene Mirrors werden nicht verschoben; neue/aktualisierte Repos landen im neuen Ziel.")
	}
	return nil
}

func cmdSetToken(args []string) error {
	fs := flag.NewFlagSet("set-token", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	connID := fs.String("connection", "", "Verbindung (ID/Label)")
	tokenFile := fs.String("token-file", "", "Token-Datei")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	conn, err := findConnection(cfg, *connID)
	if err != nil {
		return err
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		return err
	}
	client := newGitHubClient(cfg.APIBase)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner, repos, err := client.Validate(ctx, conn.Kind, conn.Owner, token)
	if err != nil {
		return fmt.Errorf("Token prüfen: %w", err)
	}
	conn.Token = token
	conn.Owner = owner
	if conn.Mode == "selected" {
		available := map[string]bool{}
		for _, repo := range repos {
			available[repo.FullName] = true
		}
		kept := conn.Repos[:0]
		for _, name := range conn.Repos {
			if available[name] {
				kept = append(kept, name)
			}
		}
		conn.Repos = kept
		if len(conn.Repos) == 0 {
			conn.Mode = "all"
		}
	}
	if err := saveConfig(*config, cfg); err != nil {
		return err
	}
	fmt.Printf("Token für %s aktualisiert (%d Repos sichtbar).\n", conn.Label, len(repos))
	return nil
}

func cmdSchedule(args []string) error {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	weekday := fs.Int("weekday", -1, "0=So ... 6=Sa")
	hour := fs.Int("hour", -1, "Stunde 0-23")
	minute := fs.Int("minute", -1, "Minute 0-59")
	enable := fs.Bool("enable", false, "Zeitplan aktivieren")
	disable := fs.Bool("disable", false, "Zeitplan deaktivieren")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	if *weekday >= 0 {
		cfg.Schedule.Weekday = *weekday
	}
	if *hour >= 0 {
		cfg.Schedule.Hour = *hour
	}
	if *minute >= 0 {
		cfg.Schedule.Minute = *minute
	}
	if *enable && *disable {
		return errors.New("--enable und --disable nicht gleichzeitig")
	}
	if *enable {
		cfg.Schedule.Enabled = true
	}
	if *disable {
		cfg.Schedule.Enabled = false
	}
	if err := validateSchedule(cfg.Schedule.Weekday, cfg.Schedule.Hour, cfg.Schedule.Minute); err != nil {
		return err
	}
	if err := saveConfig(*config, cfg); err != nil {
		return err
	}
	fmt.Printf("Zeitplan: aktiv=%v, Wochentag=%d, %02d:%02d\n", cfg.Schedule.Enabled, cfg.Schedule.Weekday, cfg.Schedule.Hour, cfg.Schedule.Minute)
	return nil
}

func cmdTest(args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	config := fs.String("config", defaultConfigPath(), "Konfigurationsdatei")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*config, "/volume1/backup/github")
	if err != nil {
		return err
	}
	client := newGitHubClient(cfg.APIBase)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	failed := false
	for i := range cfg.Connections {
		c := &cfg.Connections[i]
		repos, err := client.listRepos(ctx, c.Token, c.Kind, c.Owner)
		if err != nil {
			fmt.Printf("%s: FEHLER %v\n", c.Label, err)
			failed = true
			continue
		}
		fmt.Printf("%s: OK, %d Repo(s)\n", c.Label, len(repos))
	}
	if failed {
		return errors.New("mindestens eine Verbindung fehlerhaft")
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/index.html web/app.js web/app.css
var contentFS embed.FS

type serverApp struct {
	cfgPath string
	cfgMu   sync.RWMutex
	cfg     *Config

	cfgOpMu   sync.Mutex
	runMu     sync.Mutex
	runState  *RunState
	runCancel context.CancelFunc
}

type connectionView struct {
	ID            string     `json:"id"`
	Label         string     `json:"label"`
	Kind          string     `json:"kind"`
	Owner         string     `json:"owner"`
	Mode          string     `json:"mode"`
	Path          string     `json:"path"`
	EffectivePath string     `json:"effective_path"`
	Enabled       bool       `json:"enabled"`
	HasToken      bool       `json:"has_token"`
	SelectedCount int        `json:"selected_count"`
	LastRun       *time.Time `json:"last_run,omitempty"`
	LastStatus    string     `json:"last_status,omitempty"`
}

type statsView struct {
	Repos    int   `json:"repos"`
	Wikis    int   `json:"wikis"`
	DiskFree int64 `json:"disk_free"`
	DiskSize int64 `json:"disk_size"`
}

func newServerApp(cfgPath string) (*serverApp, error) {
	cfg, err := loadConfig(cfgPath, "/volume1/backup/github")
	if err != nil {
		return nil, err
	}
	return &serverApp{
		cfgPath: cfgPath,
		cfg:     cfg,
	}, nil
}

func (a *serverApp) snapshot() *Config {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	return a.cfg.clone()
}

func (a *serverApp) save(cfg *Config) error {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if err := saveConfig(a.cfgPath, cfg); err != nil {
		return err
	}
	a.cfg = cfg
	return nil
}

func (a *serverApp) reload() {
	cfg, err := loadConfig(a.cfgPath, "/volume1/backup/github")
	if err == nil {
		a.cfgMu.Lock()
		a.cfg = cfg
		a.cfgMu.Unlock()
	}
}

func (a *serverApp) running() bool {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	return a.runState != nil && a.runState.IsRunning()
}

func (a *serverApp) startRun(ids []string, scheduled bool) (*RunState, error) {
	a.cfgOpMu.Lock()
	defer a.cfgOpMu.Unlock()
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.runState != nil && a.runState.IsRunning() {
		return nil, errors.New("es läuft bereits ein Backup")
	}
	cfg := a.snapshot()
	state := newRunState()
	state.start()
	ctx, cancel := context.WithCancel(context.Background())
	a.runState = state
	a.runCancel = cancel

	go func() {
		r, err := newRunner(a.cfgPath, cfg, false, state)
		if err != nil {
			state.start()
			state.finish(RunSummary{Status: "failed", Finished: time.Now()}, err)
			a.runMu.Lock()
			a.runCancel = nil
			a.runMu.Unlock()
			return
		}
		_, _ = r.Run(ctx, ids, scheduled)
		a.runMu.Lock()
		a.runCancel = nil
		a.runMu.Unlock()
		a.reload()
	}()
	return state, nil
}

func (a *serverApp) cancelRun() {
	a.runMu.Lock()
	cancel := a.runCancel
	a.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *serverApp) schedule(ctx context.Context) {
	var retryAt time.Time
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		cfg := a.snapshot()
		now := time.Now()
		if cfg.Schedule.Enabled && dueNow(cfg) && !a.running() && (retryAt.IsZero() || !now.Before(retryAt)) {
			// Do not hammer a failing schedule every 30 seconds. A successful
			// scheduled run clears dueNow via LastRun; a failed one is retried
			// after half an hour.
			retryAt = now.Add(30 * time.Minute)
			_, _ = a.startRun(nil, true)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *serverApp) state() map[string]any {
	cfg := a.snapshot()
	views := make([]connectionView, 0, len(cfg.Connections))
	for i := range cfg.Connections {
		views = append(views, connectionView{
			ID:            cfg.Connections[i].ID,
			Label:         cfg.Connections[i].Label,
			Kind:          cfg.Connections[i].Kind,
			Owner:         cfg.Connections[i].Owner,
			Mode:          cfg.Connections[i].Mode,
			Path:          cfg.Connections[i].Path,
			EffectivePath: cfg.Connections[i].effectivePath(cfg.DataRoot),
			Enabled:       cfg.Connections[i].Enabled,
			HasToken:      cfg.Connections[i].Token != "",
			SelectedCount: len(cfg.Connections[i].Repos),
			LastRun:       cfg.Connections[i].LastRun,
			LastStatus:    cfg.Connections[i].LastStatus,
		})
	}
	var run RunStateView
	a.runMu.Lock()
	if a.runState != nil {
		run = a.runState.View()
	} else {
		run = RunStateView{Status: "idle"}
	}
	a.runMu.Unlock()

	var next *time.Time
	if cfg.Schedule.Enabled {
		n := nextRunAfter(time.Now(), cfg.Schedule)
		next = &n
	}
	return map[string]any{
		"data_root":   cfg.DataRoot,
		"schedule":    cfg.Schedule,
		"options":     cfg.Options,
		"connections": views,
		"last_run":    cfg.LastRun,
		"last_status": cfg.LastStatus,
		"next_run":    next,
		"run":         run,
		"stats":       a.stats(cfg.DataRoot),
	}
}

func (a *serverApp) stats(root string) statsView {
	var s statsView
	count := func(dir string) int {
		n := 0
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && strings.HasSuffix(path, ".git") {
				n++
				return fs.SkipDir
			}
			return nil
		})
		return n
	}
	s.Repos = count(filepath.Join(root, "repos"))
	s.Wikis = count(filepath.Join(root, "wikis"))
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err == nil {
		s.DiskFree = int64(st.Bavail) * int64(st.Bsize)
		s.DiskSize = int64(st.Blocks) * int64(st.Bsize)
	}
	return s
}

func (a *serverApp) serve(unixPath, pidFile string) error {
	if unixPath == "" {
		return errors.New("kein Unix-Socket-Pfad angegeben")
	}
	if st, err := os.Stat(unixPath); err == nil && !st.IsDir() {
		_ = os.Remove(unixPath)
	}
	_ = os.MkdirAll(filepath.Dir(unixPath), 0700)
	ln, err := net.Listen("unix", unixPath)
	if err != nil {
		return err
	}
	_ = os.Chmod(unixPath, 0666)
	if pidFile != "" {
		_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600)
		defer os.Remove(pidFile)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", a.handleSession)
	mux.HandleFunc("/api/state", a.handleState)
	mux.HandleFunc("/api/connections", a.handleConnections)
	mux.HandleFunc("/api/connections/", a.handleConnection)
	mux.HandleFunc("/api/run", a.handleRun)
	mux.HandleFunc("/api/run/cancel", a.handleRunCancel)
	mux.HandleFunc("/api/settings", a.handleSettings)
	mux.HandleFunc("/api/logs", a.handleLogs)
	mux.HandleFunc("/api/logs/", a.handleLogFile)
	sub, err := fs.Sub(contentFS, "web")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	srv := &http.Server{Handler: a.authMiddleware(mux)}
	ctx, cancel := signalContext()
	defer cancel()
	go a.schedule(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = srv.Shutdown(shutdownCtx)
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func (a *serverApp) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/session" {
			next.ServeHTTP(w, r)
			return
		}
		user, err := dsmUser(r)
		if err != nil || user == "" {
			jsonError(w, http.StatusUnauthorized, "Nicht in DSM angemeldet.")
			return
		}
		if !isAdmin(user) {
			jsonError(w, http.StatusForbidden, "Nur DSM-Administratoren dürfen GitHub Backup verwalten.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-GitHub-Backup") != "1" {
				jsonError(w, http.StatusForbidden, "CSRF-Schutz: Header fehlt.")
				return
			}
		}
		r.Header.Set("X-DSM-User", user)
		next.ServeHTTP(w, r)
	})
}

func dsmUser(r *http.Request) (string, error) {
	if dev := os.Getenv("GITHUB_BACKUP_DEV_USER"); dev != "" {
		return dev, nil
	}
	cgi := "/usr/syno/synoman/webman/modules/authenticate.cgi"
	if _, err := os.Stat(cgi); err != nil {
		return "", errors.New("DSM-Authentifizierung nicht verfügbar")
	}
	cmd := exec.Command(cgi)
	host := firstNonEmpty(r.Header.Get("X-Forwarded-Host"), r.Host)
	serverAddr := firstNonEmpty(r.Header.Get("X-Server-Addr"), host)
	serverPort := firstNonEmpty(r.Header.Get("X-Server-Port"), "5001")
	remoteAddr := firstNonEmpty(r.Header.Get("X-Real-IP"), r.RemoteAddr)
	https := "off"
	if r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil {
		https = "on"
	}
	env := os.Environ()
	env = append(env,
		"REQUEST_METHOD=GET",
		"SCRIPT_NAME="+r.URL.Path,
		"QUERY_STRING="+r.URL.RawQuery,
		"SERVER_PROTOCOL=HTTP/1.1",
		"SERVER_SOFTWARE=nginx",
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_NAME="+host,
		"SERVER_ADDR="+serverAddr,
		"SERVER_PORT="+serverPort,
		"REMOTE_ADDR="+remoteAddr,
		"HTTP_HOST="+host,
		"HTTPS="+https,
		"HTTP_COOKIE="+r.Header.Get("Cookie"),
	)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func isAdmin(user string) bool {
	if dev := os.Getenv("GITHUB_BACKUP_DEV_USER"); dev != "" {
		return true
	}
	cmd := exec.Command("id", "-nG", user)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, g := range strings.Fields(string(out)) {
		if g == "administrators" || g == "admin" {
			return true
		}
	}
	return false
}

func (a *serverApp) handleSession(w http.ResponseWriter, r *http.Request) {
	user, err := dsmUser(r)
	if err != nil || user == "" {
		jsonError(w, http.StatusUnauthorized, "Nicht in DSM angemeldet.")
		return
	}
	if !isAdmin(user) {
		jsonError(w, http.StatusForbidden, "Nur DSM-Administratoren.")
		return
	}
	jsonOK(w, map[string]any{"user": user})
}

func (a *serverApp) handleState(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, a.state())
}

func (a *serverApp) handleConnections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, a.state()["connections"])
	case http.MethodPost:
		a.cfgOpMu.Lock()
		defer a.cfgOpMu.Unlock()
		var req struct {
			Kind  string `json:"kind"`
			Label string `json:"label"`
			Owner string `json:"owner"`
			Token string `json:"token"`
			Mode  string `json:"mode"`
			Path  string `json:"path"`
		}
		if err := decodeJSON(r, &req); err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if a.running() {
			jsonError(w, http.StatusConflict, "Backup läuft; bitte danach ändern.")
			return
		}
		kind, owner, err := normalizeKind(req.Kind, req.Owner)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if req.Token == "" {
			jsonError(w, 400, "Token fehlt.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		login, repos, err := newGitHubClient(a.snapshot().APIBase).Validate(ctx, kind, owner, req.Token)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		mode := req.Mode
		if mode != "selected" {
			mode = "all"
		}
		if strings.TrimSpace(req.Path) != "" {
			if err := validateAbsolutePath(req.Path); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
		}
		label := strings.TrimSpace(req.Label)
		if label == "" {
			switch kind {
			case "classic":
				label = "Alles (classic)"
			case "org":
				label = "Org: " + login
			default:
				label = login
			}
		}
		cfg := a.snapshot()
		conn := Connection{ID: newID(), Label: label, Kind: kind, Owner: login, Token: req.Token, Mode: mode, Path: strings.TrimSpace(req.Path), Enabled: true, CreatedAt: time.Now()}
		if mode == "selected" {
			jsonError(w, 400, "Bitte nach dem Anlegen die Repositories auswählen.")
			return
		}
		cfg.Connections = append(cfg.Connections, conn)
		if err := a.save(cfg); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]any{"id": conn.ID, "owner": login, "repos": len(repos)})
	default:
		jsonError(w, 405, "Methode nicht erlaubt")
	}
}

var connPathRe = regexp.MustCompile(`^/api/connections/([^/]+)(/repos|/test)?$`)

func (a *serverApp) handleConnection(w http.ResponseWriter, r *http.Request) {
	m := connPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		jsonError(w, 404, "nicht gefunden")
		return
	}
	id := m[1]
	suffix := m[2]

	if suffix == "/test" {
		if r.Method != http.MethodPost {
			jsonError(w, 405, "Methode nicht erlaubt")
			return
		}
		cfg := a.snapshot()
		conn := cfg.connectionByID(id)
		if conn == nil {
			jsonError(w, 404, "Verbindung nicht gefunden")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		repos, err := newGitHubClient(cfg.APIBase).listRepos(ctx, conn.Token, conn.Kind, conn.Owner)
		if err != nil {
			jsonOK(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		jsonOK(w, map[string]any{"ok": true, "count": len(repos)})
		return
	}

	if suffix == "/repos" {
		a.handleRepos(w, r, id)
		return
	}

	switch r.Method {
	case http.MethodPut:
		a.cfgOpMu.Lock()
		defer a.cfgOpMu.Unlock()
		var req struct {
			Label   string `json:"label"`
			Mode    string `json:"mode"`
			Path    string `json:"path"`
			Enabled *bool  `json:"enabled"`
			Token   string `json:"token"`
		}
		if err := decodeJSON(r, &req); err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if a.running() {
			jsonError(w, http.StatusConflict, "Backup läuft; bitte danach ändern.")
			return
		}
		cfg := a.snapshot()
		conn := cfg.connectionByID(id)
		if conn == nil {
			jsonError(w, 404, "Verbindung nicht gefunden")
			return
		}
		if req.Label != "" {
			conn.Label = strings.TrimSpace(req.Label)
		}
		if req.Mode == "all" || req.Mode == "selected" {
			conn.Mode = req.Mode
		}
		if req.Path != "" {
			if err := validateAbsolutePath(req.Path); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
			conn.Path = filepath.Clean(req.Path)
		}
		if req.Enabled != nil {
			conn.Enabled = *req.Enabled
		}
		if req.Token != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
			defer cancel()
			owner, _, err := newGitHubClient(cfg.APIBase).Validate(ctx, conn.Kind, conn.Owner, req.Token)
			if err != nil {
				jsonError(w, 400, err.Error())
				return
			}
			conn.Token = req.Token
			conn.Owner = owner
		}
		if err := a.save(cfg); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]any{"ok": true})
	case http.MethodDelete:
		a.cfgOpMu.Lock()
		defer a.cfgOpMu.Unlock()
		if a.running() {
			jsonError(w, http.StatusConflict, "Backup läuft; bitte danach ändern.")
			return
		}
		cfg := a.snapshot()
		if !cfg.removeConnection(id) {
			jsonError(w, 404, "Verbindung nicht gefunden")
			return
		}
		if err := a.save(cfg); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]any{"ok": true})
	default:
		jsonError(w, 405, "Methode nicht erlaubt")
	}
}

func (a *serverApp) handleRepos(w http.ResponseWriter, r *http.Request, id string) {
	cfg := a.snapshot()
	conn := cfg.connectionByID(id)
	if conn == nil {
		jsonError(w, 404, "Verbindung nicht gefunden")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	repos, err := newGitHubClient(cfg.APIBase).listRepos(ctx, conn.Token, conn.Kind, conn.Owner)
	if err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		jsonOK(w, map[string]any{"repos": repos, "selected": conn.Repos, "mode": conn.Mode})
	case http.MethodPut:
		a.cfgOpMu.Lock()
		defer a.cfgOpMu.Unlock()
		if a.running() {
			jsonError(w, http.StatusConflict, "Backup läuft; bitte danach ändern.")
			return
		}
		var req struct {
			Mode  string   `json:"mode"`
			Repos []string `json:"repos"`
		}
		if err := decodeJSON(r, &req); err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		cfg := a.snapshot()
		conn = cfg.connectionByID(id)
		if req.Mode == "all" {
			conn.Mode = "all"
			conn.Repos = nil
		} else {
			available := map[string]bool{}
			for _, repo := range repos {
				available[repo.FullName] = true
			}
			var selected []string
			for _, name := range req.Repos {
				if available[name] {
					selected = append(selected, name)
				}
			}
			if len(selected) == 0 {
				jsonError(w, 400, "Keine gültigen Repositories ausgewählt.")
				return
			}
			conn.Mode = "selected"
			conn.Repos = selected
		}
		conn.normalizeRepos()
		if err := a.save(cfg); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		jsonOK(w, map[string]any{"ok": true})
	default:
		jsonError(w, 405, "Methode nicht erlaubt")
	}
}

func (a *serverApp) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "Methode nicht erlaubt")
		return
	}
	var req struct {
		ConnectionID string `json:"connection_id"`
	}
	_ = decodeJSON(r, &req)
	var ids []string
	if req.ConnectionID != "" {
		ids = []string{req.ConnectionID}
	}
	state, err := a.startRun(ids, false)
	if err != nil {
		jsonError(w, http.StatusConflict, err.Error())
		return
	}
	jsonOK(w, state.View())
}

func (a *serverApp) handleRunCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "Methode nicht erlaubt")
		return
	}
	a.cancelRun()
	jsonOK(w, map[string]any{"ok": true})
}

func (a *serverApp) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		jsonError(w, 405, "Methode nicht erlaubt")
		return
	}
	a.cfgOpMu.Lock()
	defer a.cfgOpMu.Unlock()
	if a.running() {
		jsonError(w, http.StatusConflict, "Backup läuft; bitte danach ändern.")
		return
	}
	var req struct {
		DataRoot string   `json:"data_root"`
		Schedule Schedule `json:"schedule"`
		Options  Options  `json:"options"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if err := validateAbsolutePath(req.DataRoot); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if err := validateSchedule(req.Schedule.Weekday, req.Schedule.Hour, req.Schedule.Minute); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	cfg := a.snapshot()
	cfg.DataRoot = filepath.Clean(req.DataRoot)
	cfg.Schedule = req.Schedule
	cfg.Options = normalizeOptions(req.Options)
	if err := ensureDataDirs(cfg.DataRoot); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if err := a.save(cfg); err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (a *serverApp) handleLogs(w http.ResponseWriter, r *http.Request) {
	cfg := a.snapshot()
	dir := filepath.Join(cfg.DataRoot, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		jsonOK(w, []any{})
		return
	}
	type item struct {
		Name  string    `json:"name"`
		Size  int64     `json:"size"`
		MTime time.Time `json:"mtime"`
	}
	var out []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "run-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, item{Name: e.Name(), Size: info.Size(), MTime: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MTime.After(out[j].MTime) })
	jsonOK(w, out)
}

func (a *serverApp) handleLogFile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		jsonError(w, 400, "ungültiger Logname")
		return
	}
	cfg := a.snapshot()
	path := filepath.Join(cfg.DataRoot, "logs", name)
	f, err := os.Open(path)
	if err != nil {
		jsonError(w, 404, "Log nicht gefunden")
		return
	}
	defer f.Close()
	const max = 512 * 1024
	st, err := f.Stat()
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	if st.Size() > max {
		_, _ = f.Seek(-max, io.SeekEnd)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		jsonError(w, 500, err.Error())
		return
	}
	jsonOK(w, map[string]any{"name": name, "content": string(data)})
}

func decodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("leerer Request")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	return dec.Decode(out)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

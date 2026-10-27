package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type RunSummary struct {
	Status      string
	Started     time.Time
	Finished    time.Time
	Connections int
	ConnFailed  int
	Total       int
	Cloned      int
	Updated     int
	Failed      int
	Wikis       int
	WikiFailed  int
	LFSFailed   int
	LogPath     string
	FailedRepos []string
}

type repoResult struct {
	FullName string
	Status   string
	Wiki     string
	LFS      string
	Error    string
}

type runner struct {
	cfgPath string
	cfg     *Config
	client  *githubClient
	askpass string
	gitPath string
	verbose bool
	logFile *os.File
	logPath string
	logMu   sync.Mutex
}

func newRunner(cfgPath string, cfg *Config, verbose bool) (*runner, error) {
	varlDir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(varlDir, 0700); err != nil {
		return nil, err
	}
	askpass := filepath.Join(varlDir, "askpass.sh")
	script := `#!/bin/sh
case "$1" in
  *[Uu]sername*) printf '%s\n' "${GIT_USERNAME:-x-access-token}" ;;
  *[Pp]assword*) printf '%s\n' "$GITHUB_TOKEN" ;;
  *) printf '\n' ;;
esac
`
	if err := os.WriteFile(askpass, []byte(script), 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(askpass, 0700)
	gitPath := findGit()
	return &runner{
		cfgPath: cfgPath,
		cfg:     cfg,
		client:  newGitHubClient(cfg.APIBase),
		askpass: askpass,
		gitPath: gitPath,
		verbose: verbose,
	}, nil
}

func findGit() string {
	candidates := []string{
		"/var/packages/Git/target/bin/git",
		"/opt/bin/git",
		"/usr/local/bin/git",
		"/usr/bin/git",
		"/bin/git",
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return ""
}

func (r *runner) logf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	line = strings.TrimRight(line, "\n")
	r.logMu.Lock()
	defer r.logMu.Unlock()
	if r.verbose {
		fmt.Println(line)
	}
	if r.logFile != nil {
		fmt.Fprintf(r.logFile, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
	}
}

func (r *runner) openLog() error {
	logDir := filepath.Join(r.cfg.DataRoot, "logs")
	if err := ensureDir(logDir); err != nil {
		return err
	}
	name := "run-" + time.Now().Format("20060102-150405") + ".log"
	path := filepath.Join(logDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	r.logFile = f
	r.logPath = path
	return nil
}

func (r *runner) closeLog() {
	if r.logFile != nil {
		_ = r.logFile.Close()
		r.logFile = nil
	}
}

func (r *runner) Run(ctx context.Context, only []string) (RunSummary, error) {
	sum := RunSummary{Started: time.Now(), Status: "failed"}
	if err := ensureDataDirs(r.cfg.DataRoot); err != nil {
		return sum, err
	}
	lockPath := filepath.Join(r.cfg.DataRoot, ".lock")
	release, err := acquireLock(lockPath)
	if err != nil {
		return sum, err
	}
	defer release()

	if err := r.openLog(); err != nil {
		return sum, err
	}
	defer r.closeLog()
	sum.LogPath = r.logPath

	r.logf("=== GitHub Backup start ===")
	r.logf("Ziel: %s", r.cfg.DataRoot)
	if r.gitPath == "" {
		r.logf("FEHLER: git nicht gefunden. Bitte das Synology-Paket 'Git Server' installieren.")
		sum.Finished = time.Now()
		r.cfg.LastRun = &sum.Finished
		r.cfg.LastStatus = "failed"
		_ = saveConfig(r.cfgPath, r.cfg)
		_ = r.writeLastRun(sum)
		return sum, errors.New("git nicht gefunden")
	}
	r.logf("Git: %s", r.gitPath)

	conns := make([]*Connection, 0, len(r.cfg.Connections))
	onlySet := map[string]bool{}
	for _, id := range only {
		onlySet[id] = true
	}
	for i := range r.cfg.Connections {
		c := &r.cfg.Connections[i]
		if !c.Enabled {
			continue
		}
		if len(onlySet) > 0 && !onlySet[c.ID] && !onlySet[c.Label] {
			continue
		}
		conns = append(conns, c)
	}
	if len(conns) == 0 {
		r.logf("Keine aktiven Verbindungen.")
		sum.Status = "empty"
		sum.Finished = time.Now()
		r.cfg.LastRun = &sum.Finished
		r.cfg.LastStatus = "empty"
		_ = saveConfig(r.cfgPath, r.cfg)
		_ = r.writeLastRun(sum)
		return sum, errors.New("keine aktiven Verbindungen")
	}
	sum.Connections = len(conns)

	var allResults []repoResult
	now := time.Now()
	for _, conn := range conns {
		r.logf("")
		r.logf("--- %s (%s/%s) ---", conn.Label, conn.Kind, conn.Owner)
		if strings.TrimSpace(conn.Token) == "" {
			r.logf("FEHLER: Token fehlt.")
			sum.ConnFailed++
			conn.LastStatus = "error"
			continue
		}
		repos, err := r.client.listRepos(ctx, conn.Token, conn.Kind, conn.Owner)
		if err != nil {
			r.logf("FEHLER beim Repo-Listing: %v", err)
			sum.ConnFailed++
			conn.LastStatus = "error"
			conn.LastRun = &now
			continue
		}

		targets := repos
		if conn.Mode == "selected" {
			byName := map[string]Repo{}
			for _, repo := range repos {
				byName[repo.FullName] = repo
			}
			targets = targets[:0]
			for _, name := range conn.Repos {
				if repo, ok := byName[name]; ok {
					targets = append(targets, repo)
				} else {
					r.logf("WARN: ausgewähltes Repo nicht mehr vorhanden: %s", name)
				}
			}
		}
		if len(targets) == 0 {
			r.logf("Keine Repositories zum Sichern.")
			conn.LastRun = &now
			conn.LastStatus = "empty"
			continue
		}

		r.logf("Repositories: %d (Modus: %s)", len(targets), conn.Mode)
		results := r.backupConnection(ctx, conn, targets)
		allResults = append(allResults, results...)

		for _, res := range results {
			switch res.Status {
			case "cloned":
				sum.Cloned++
			case "updated":
				sum.Updated++
			default:
				sum.Failed++
				sum.FailedRepos = append(sum.FailedRepos, res.FullName)
			}
			if res.Wiki == "ok" {
				sum.Wikis++
			}
			if res.Wiki == "failed" {
				sum.WikiFailed++
			}
			if res.LFS == "failed" {
				sum.LFSFailed++
			}
		}
		sum.Total += len(results)
		conn.LastRun = &now
		if connHasErrors(results) {
			conn.LastStatus = "error"
		} else {
			conn.LastStatus = "success"
		}
		r.cfg.LastRun = &now
		if err := saveConfig(r.cfgPath, r.cfg); err != nil {
			r.logf("WARN: Konfiguration speichern: %v", err)
		}
	}

	sum.Finished = time.Now()
	if sum.Failed == 0 && sum.WikiFailed == 0 && sum.LFSFailed == 0 && sum.ConnFailed == 0 {
		sum.Status = "success"
	} else {
		sum.Status = "completed_with_errors"
	}
	r.cfg.LastRun = &sum.Finished
	r.cfg.LastStatus = sum.Status
	if err := saveConfig(r.cfgPath, r.cfg); err != nil {
		r.logf("WARN: Konfiguration speichern: %v", err)
	}

	_ = r.writeManifest(allResults)
	_ = r.writeLastRun(sum)
	r.cleanupLogs()

	r.logf("")
	r.logf("=== GitHub Backup fertig ===")
	r.logf("Status: %s | Repos: %d | neu: %d | aktualisiert: %d | Fehler: %d | Wikis: %d | Wiki-Fehler: %d | LFS-Fehler: %d",
		sum.Status, sum.Total, sum.Cloned, sum.Updated, sum.Failed, sum.Wikis, sum.WikiFailed, sum.LFSFailed)
	r.logf("Dauer: %s", formatDuration(sum.Finished.Sub(sum.Started)))
	r.logf("Log: %s", sum.LogPath)

	if r.cfg.Options.Notify {
		msg := fmt.Sprintf("Status: %s\nRepos: %d, aktualisiert: %d, neu: %d, Fehler: %d\nLog: %s",
			sum.Status, sum.Total, sum.Updated, sum.Cloned, sum.Failed, sum.LogPath)
		sendNotification("GitHub Backup", msg)
	}

	if sum.Status != "success" {
		return sum, fmt.Errorf("Backup mit Fehlern beendet (%d Repo-Fehler, %d Wiki-Fehler, %d LFS-Fehler, %d Verbindungsfehler)",
			sum.Failed, sum.WikiFailed, sum.LFSFailed, sum.ConnFailed)
	}
	return sum, nil
}

func connHasErrors(results []repoResult) bool {
	for _, r := range results {
		if r.Status == "failed" || r.Wiki == "failed" || r.LFS == "failed" {
			return true
		}
	}
	return false
}

func (r *runner) backupConnection(ctx context.Context, conn *Connection, repos []Repo) []repoResult {
	workers := r.cfg.Options.Concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(repos) {
		workers = len(repos)
	}
	jobs := make(chan Repo)
	out := make(chan repoResult, len(repos))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				out <- r.backupRepo(ctx, conn, repo)
			}
		}()
	}
	go func() {
		for _, repo := range repos {
			jobs <- repo
		}
		close(jobs)
	}()
	wg.Wait()
	close(out)
	results := make([]repoResult, 0, len(repos))
	for res := range out {
		results = append(results, res)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].FullName < results[j].FullName })
	return results
}

func (r *runner) backupRepo(ctx context.Context, conn *Connection, repo Repo) repoResult {
	res := repoResult{FullName: repo.FullName, Status: "failed"}
	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 || !validRepoFullName(repo.FullName) {
		res.Error = "ungültiger Repo-Name"
		r.logf("  SKIP %s: %s", repo.FullName, res.Error)
		return res
	}
	owner, name := parts[0], parts[1]
	root := conn.effectivePath(r.cfg.DataRoot)
	dest := filepath.Join(root, "repos", owner, name+".git")
	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		cloneURL = "https://github.com/" + repo.FullName + ".git"
	}

	r.logf("  [%s] %s", conn.Owner, repo.FullName)
	action, err := r.mirror(ctx, conn.Token, cloneURL, dest, 12*time.Hour)
	if err != nil {
		res.Error = err.Error()
		r.logf("    FEHLER: %v", err)
		return res
	}
	res.Status = action
	r.logf("    %s", action)

	if r.cfg.Options.IncludeWikis {
		wikiURL := strings.TrimSuffix(cloneURL, ".git") + ".wiki.git"
		wikiDest := filepath.Join(root, "wikis", owner, name+".git")
		if r.wikiExists(ctx, conn.Token, wikiURL) {
			if _, err := r.mirror(ctx, conn.Token, wikiURL, wikiDest, 4*time.Hour); err != nil {
				res.Wiki = "failed"
				r.logf("    Wiki-FEHLER: %v", err)
			} else {
				res.Wiki = "ok"
				r.logf("    Wiki gesichert")
			}
		}
	}

	if r.cfg.Options.IncludeLFS {
		if err := r.fetchLFS(ctx, conn.Token, dest); err != nil {
			res.LFS = "failed"
			r.logf("    LFS-FEHLER: %v", err)
		} else {
			res.LFS = "ok"
			r.logf("    LFS gesichert")
		}
	}
	return res
}

func (r *runner) mirror(ctx context.Context, token, url, dest string, timeout time.Duration) (string, error) {
	if err := ensureDir(filepath.Dir(dest)); err != nil {
		return "", err
	}
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		r.enableReflog(ctx, token, dest)
		args := []string{"--git-dir=" + dest, "remote", "update"}
		if r.cfg.Options.PruneDeleted {
			args = append(args, "--prune")
		}
		if err := r.gitRetry(ctx, token, timeout, args...); err != nil {
			return "", err
		}
		return "updated", nil
	}

	tmp := dest + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	attempts := r.cfg.Options.GitRetries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := r.runGit(ctx, token, timeout, "clone", "--mirror", url, tmp); err != nil {
			lastErr = err
			_ = os.RemoveAll(tmp)
			if attempt < attempts {
				if !sleepCtx(ctx, time.Duration(attempt)*5*time.Second) {
					break
				}
				continue
			}
			break
		}
		if err := os.Rename(tmp, dest); err != nil {
			return "", err
		}
		return "cloned", nil
	}
	return "", lastErr
}

func (r *runner) wikiExists(ctx context.Context, token, url string) bool {
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := r.gitCommand(ctx2, token, "ls-remote", url)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	return cmd.Run() == nil
}

func (r *runner) fetchLFS(ctx context.Context, token, dest string) error {
	if findGitLFS() == "" {
		return errors.New("git-lfs ist nicht installiert")
	}
	return r.gitRetry(ctx, token, 4*time.Hour, "--git-dir="+dest, "lfs", "fetch", "--all")
}

func findGitLFS() string {
	for _, p := range []string{
		"/var/packages/Git/target/bin/git-lfs",
		"/opt/bin/git-lfs",
		"/usr/local/bin/git-lfs",
		"/usr/bin/git-lfs",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return p
		}
	}
	if p, err := exec.LookPath("git-lfs"); err == nil {
		return p
	}
	return ""
}

func (r *runner) enableReflog(ctx context.Context, token, dest string) {
	_ = r.runGit(ctx, token, 30*time.Second, "--git-dir="+dest, "config", "core.logAllRefUpdates", "true")
}

func (r *runner) gitRetry(ctx context.Context, token string, timeout time.Duration, args ...string) error {
	attempts := r.cfg.Options.GitRetries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := r.runGit(ctx, token, timeout, args...); err != nil {
			lastErr = err
			if attempt < attempts {
				if !sleepCtx(ctx, time.Duration(attempt)*5*time.Second) {
					break
				}
				continue
			}
			break
		}
		return nil
	}
	return lastErr
}

func (r *runner) runGit(ctx context.Context, token string, timeout time.Duration, args ...string) error {
	if r.gitPath == "" {
		return errors.New("git nicht gefunden")
	}
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := r.gitCommand(ctx2, token, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		out := buf.String()
		out = strings.ReplaceAll(out, token, "***")
		if len(out) > 1600 {
			out = out[len(out)-1600:]
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (r *runner) gitCommand(ctx context.Context, token string, args ...string) *exec.Cmd {
	base := []string{
		"-c", "credential.helper=",
		"-c", "http.lowSpeedLimit=1000",
		"-c", "http.lowSpeedTime=60",
	}
	cmd := exec.CommandContext(ctx, r.gitPath, append(base, args...)...)
	path := "/var/packages/Git/target/bin:/opt/bin:/usr/local/bin:/usr/syno/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if old := os.Getenv("PATH"); old != "" {
		path += ":" + old
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = filepath.Dir(r.cfgPath)
	}
	env := make([]string, 0, len(os.Environ())+6)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") || strings.HasPrefix(e, "GIT_ASKPASS=") ||
			strings.HasPrefix(e, "GIT_TERMINAL_PROMPT=") || strings.HasPrefix(e, "LC_ALL=") ||
			strings.HasPrefix(e, "HOME=") || strings.HasPrefix(e, "LD_LIBRARY_PATH=") {
			continue
		}
		env = append(env, e)
	}
	libPath := "/var/packages/Git/target/lib"
	if old := os.Getenv("LD_LIBRARY_PATH"); old != "" {
		libPath += ":" + old
	}
	env = append(env,
		"GITHUB_TOKEN="+token,
		"GIT_ASKPASS="+r.askpass,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
		"HOME="+home,
		"PATH="+path,
		"LD_LIBRARY_PATH="+libPath,
	)
	cmd.Env = env
	return cmd
}

func (r *runner) writeManifest(results []repoResult) error {
	path := filepath.Join(r.cfg.DataRoot, "meta", "manifest.tsv")
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("repository\tstatus\twiki\tlfs\ttimestamp_utc\n")
	now := time.Now().UTC().Format(time.RFC3339)
	for _, res := range results {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", res.FullName, res.Status, emptyDash(res.Wiki), emptyDash(res.LFS), now)
	}
	return writeFileAtomic(path, []byte(b.String()), 0640)
}

func (r *runner) writeLastRun(sum RunSummary) error {
	path := filepath.Join(r.cfg.DataRoot, "meta", "last-run.txt")
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "status: %s\n", sum.Status)
	fmt.Fprintf(&b, "started_utc: %s\n", sum.Started.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "finished_utc: %s\n", sum.Finished.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "connections: %d\n", sum.Connections)
	fmt.Fprintf(&b, "connection_errors: %d\n", sum.ConnFailed)
	fmt.Fprintf(&b, "repositories: %d\n", sum.Total)
	fmt.Fprintf(&b, "cloned: %d\n", sum.Cloned)
	fmt.Fprintf(&b, "updated: %d\n", sum.Updated)
	fmt.Fprintf(&b, "failed: %d\n", sum.Failed)
	fmt.Fprintf(&b, "wikis: %d\n", sum.Wikis)
	fmt.Fprintf(&b, "wiki_failures: %d\n", sum.WikiFailed)
	fmt.Fprintf(&b, "lfs_failures: %d\n", sum.LFSFailed)
	fmt.Fprintf(&b, "log: %s\n", sum.LogPath)
	fmt.Fprintf(&b, "target: %s\n", r.cfg.DataRoot)
	if len(sum.FailedRepos) > 0 {
		b.WriteString("failed_repositories:\n")
		for _, name := range sum.FailedRepos {
			fmt.Fprintf(&b, "  %s\n", name)
		}
	}
	return writeFileAtomic(path, []byte(b.String()), 0640)
}

func (r *runner) cleanupLogs() {
	dir := filepath.Join(r.cfg.DataRoot, "logs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -r.cfg.Options.KeepLogDays)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "run-") || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func ensureDataDirs(root string) error {
	for _, p := range []string{root, filepath.Join(root, "repos"), filepath.Join(root, "wikis"), filepath.Join(root, "meta"), filepath.Join(root, "logs")} {
		if err := ensureDir(p); err != nil {
			return err
		}
	}
	return nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	_ = os.Chmod(path, 0700)
	return nil
}

func acquireLock(path string) (func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		data, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 1 && syscall.Kill(pid, 0) == nil {
			return nil, fmt.Errorf("Backup läuft bereits (PID %d)", pid)
		}
		_ = os.Remove(path)
	}
	return nil, errors.New("konnte Lock nicht erwerben")
}

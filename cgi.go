package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var dsmLoginCGI = "/usr/syno/synoman/webman/login.cgi"
var dsmAuthenticateCGI = "/usr/syno/synoman/webman/modules/authenticate.cgi"

// cmdCGI is the package CGI bridge below ui/. It runs in the DSM webman CGI
// context, authenticates the DSM session there, and forwards one request to the
// persistent daemon over a Unix socket. No TCP listener is involved.
func cmdCGI(_ []string) error {
	q, err := url.ParseQuery(os.Getenv("QUERY_STRING"))
	if err != nil {
		cgiError(http.StatusBadRequest, "ungültige Anfrage")
		return nil
	}
	path := q.Get("path")
	if !strings.HasPrefix(path, "/api/") {
		cgiError(http.StatusBadRequest, "ungültiger API-Pfad")
		return nil
	}

	method := os.Getenv("REQUEST_METHOD")
	if method == "" {
		method = http.MethodGet
	}

	token := os.Getenv("HTTP_X_SYNO_TOKEN")
	user, err := cgiDSMUser(token)
	if err != nil || user == "" {
		cgiError(http.StatusUnauthorized, "Nicht in DSM angemeldet.")
		return nil
	}

	var body io.Reader
	if n, _ := strconv.ParseInt(os.Getenv("CONTENT_LENGTH"), 10, 64); n > 0 {
		if n > 4<<20 {
			cgiError(http.StatusRequestEntityTooLarge, "Anfrage zu groß")
			return nil
		}
		body = io.LimitReader(os.Stdin, n)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, "http://daemon"+path, body)
	if err != nil {
		cgiError(http.StatusBadRequest, err.Error())
		return nil
	}
	req.Host = firstNonEmpty(os.Getenv("SERVER_NAME"), "localhost")
	req.Header.Set("X-GitHub-Backup", "1")
	req.Header.Set("X-GitHub-Backup-CGI", "1")
	req.Header.Set("X-DSM-User", user)
	if ct := os.Getenv("CONTENT_TYPE"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if cookie := os.Getenv("HTTP_COOKIE"); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if ip := os.Getenv("REMOTE_ADDR"); ip != "" {
		req.Header.Set("X-Real-IP", ip)
	}
	if addr := os.Getenv("SERVER_ADDR"); addr != "" {
		req.Header.Set("X-Server-Addr", addr)
	}
	if port := os.Getenv("SERVER_PORT"); port != "" {
		req.Header.Set("X-Server-Port", port)
	}
	if host := os.Getenv("HTTP_HOST"); host != "" {
		req.Header.Set("X-Forwarded-Host", host)
	}
	if os.Getenv("HTTPS") == "on" {
		req.Header.Set("X-Forwarded-Proto", "https")
	} else {
		req.Header.Set("X-Forwarded-Proto", "http")
	}

	socket := "/tmp/github-backup-ui.sock"
	client := &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		cgiError(http.StatusBadGateway, "GitHub Backup Dienst nicht erreichbar")
		return nil
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	fmt.Fprintf(os.Stdout, "Status: %d %s\r\n", resp.StatusCode, http.StatusText(resp.StatusCode))
	fmt.Fprintf(os.Stdout, "Content-Type: %s\r\n", ct)
	fmt.Fprint(os.Stdout, "Cache-Control: no-store\r\n\r\n")
	_, _ = io.Copy(os.Stdout, resp.Body)
	return nil
}

func cgiDSMUser(token string) (string, error) {
	if dev := os.Getenv("GITHUB_BACKUP_DEV_USER"); dev != "" {
		return dev, nil
	}

	// The app shell passes DSM's SynoToken into the iframe. Prefer it: custom
	// CGI environments do not always receive the Cookie header.
	if token != "" {
		authEnv := cgiAuthEnv(
			"QUERY_STRING=SynoToken="+url.QueryEscape(token),
			"HTTP_X_SYNO_TOKEN="+token,
		)
		authCmd := exec.Command(dsmAuthenticateCGI)
		authCmd.Env = authEnv
		if authOut, err := authCmd.Output(); err == nil {
			if user := strings.TrimSpace(string(authOut)); user != "" {
				return user, nil
			}
		}
	}

	// DSM does not accept authenticate.cgi with only the session cookie here.
	// The supported flow is: login.cgi -> SynoToken -> authenticate.cgi.
	// This is the same flow DSM's own third-party UI examples use.
	loginEnv := cgiAuthEnv()
	loginCmd := exec.Command(dsmLoginCGI)
	loginCmd.Env = loginEnv
	out, err := loginCmd.Output()
	if err != nil {
		return "", err
	}

	var login struct {
		Success   bool   `json:"success"`
		SynoToken string `json:"SynoToken"`
		Username  string `json:"username"`
		Account   string `json:"account"`
	}
	_ = json.Unmarshal(out, &login)

	// login.cgi versions differ in nesting. DSM's own samples grep for the
	// SynoToken field; do the same as a fallback.
	if login.SynoToken == "" {
		if m := regexp.MustCompile(`"SynoToken"\s*:\s*"([^"]+)"`).FindSubmatch(out); len(m) == 2 {
			login.SynoToken = string(m[1])
		}
	}

	if login.SynoToken != "" {
		authEnv := cgiAuthEnv(
			"QUERY_STRING=SynoToken="+url.QueryEscape(login.SynoToken),
			"HTTP_X_SYNO_TOKEN="+login.SynoToken,
		)
		authCmd := exec.Command(dsmAuthenticateCGI)
		authCmd.Env = authEnv
		if authOut, err := authCmd.Output(); err == nil {
			if user := strings.TrimSpace(string(authOut)); user != "" {
				return user, nil
			}
		}
	}

	// Some DSM builds return the user directly from login.cgi.
	if login.Success {
		if user := firstNonEmpty(login.Username, login.Account); user != "" {
			return user, nil
		}
	}

	// Last fallback: authenticate.cgi with the CGI's original environment.
	// Some builds accept the session cookie directly.
	fallback := exec.Command(dsmAuthenticateCGI)
	fallback.Env = cgiAuthEnv()
	if authOut, err := fallback.Output(); err == nil {
		return strings.TrimSpace(string(authOut)), nil
	}
	return "", nil
}

// cgiAuthEnv keeps the nginx/synoscgi identity and cookie variables but
// normalizes the request to a bodyless GET for login.cgi/authenticate.cgi.
func cgiAuthEnv(extra ...string) []string {
	values := make(map[string]string, len(os.Environ())+len(extra)+5)
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			values[e[:i]] = e[i+1:]
		}
	}
	for _, k := range []string{
		"REQUEST_METHOD", "CONTENT_LENGTH", "CONTENT_TYPE",
		"HTTP_CONTENT_LENGTH", "HTTP_CONTENT_TYPE", "QUERY_STRING",
	} {
		values[k] = ""
	}
	values["REQUEST_METHOD"] = "GET"
	values["QUERY_STRING"] = ""
	for _, e := range extra {
		if i := strings.IndexByte(e, '='); i > 0 {
			values[e[:i]] = e[i+1:]
		}
	}
	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env
}

func cgiError(status int, msg string) {
	fmt.Fprintf(os.Stdout, "Status: %d %s\r\n", status, http.StatusText(status))
	fmt.Fprint(os.Stdout, "Content-Type: application/json\r\nCache-Control: no-store\r\n\r\n")
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": msg})
}

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
	"strconv"
	"strings"
	"time"
)

// cmdCGI is a tiny CGI bridge for DSM's webapi-desc worker. The persistent
// scheduler/API daemon still owns the Unix socket; this process only forwards
// one authenticated DSM web request to it. No TCP listener is involved.
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

func cgiError(status int, msg string) {
	fmt.Fprintf(os.Stdout, "Status: %d %s\r\n", status, http.StatusText(status))
	fmt.Fprint(os.Stdout, "Content-Type: application/json\r\nCache-Control: no-store\r\n\r\n")
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"error": msg})
}

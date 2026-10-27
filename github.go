package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type githubClient struct {
	baseURL string
	http    *http.Client
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub API: HTTP %d", e.Status)
	}
	return fmt.Sprintf("GitHub API: HTTP %d: %s", e.Status, e.Message)
}

func newGitHubClient(baseURL string) *githubClient {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	return &githubClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

type githubUser struct {
	Login string `json:"login"`
}

type githubOrg struct {
	Login string `json:"login"`
}

type Repo struct {
	ID            int64      `json:"id"`
	FullName      string     `json:"full_name"`
	Name          string     `json:"name"`
	Private       bool       `json:"private"`
	Fork          bool       `json:"fork"`
	Archived      bool       `json:"archived"`
	Disabled      bool       `json:"disabled"`
	Size          int64      `json:"size"`
	DefaultBranch string     `json:"default_branch"`
	PushedAt      *time.Time `json:"pushed_at"`
	CloneURL      string     `json:"clone_url"`
	HTMLURL       string     `json:"html_url"`
	Description   string     `json:"description"`
}

type repoPage struct {
	Repos []Repo
	Next  bool
}

func (c *githubClient) request(ctx context.Context, token, method, path string, query url.Values, out any) (*http.Response, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "synology-github-backup/"+appVersion)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		msg := strings.TrimSpace(string(body))
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		if len(msg) > 600 {
			msg = msg[:600] + "..."
		}
		return resp, &apiError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil {
		defer resp.Body.Close()
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32*1024*1024)).Decode(out); err != nil {
			return resp, fmt.Errorf("GitHub-Antwort dekodieren: %w", err)
		}
	}
	return resp, nil
}

func (c *githubClient) getUser(ctx context.Context, token string) (string, error) {
	var u githubUser
	if _, err := c.request(ctx, token, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("GitHub-Login konnte nicht ermittelt werden")
	}
	return u.Login, nil
}

func (c *githubClient) getOrg(ctx context.Context, token, owner string) (string, error) {
	var o githubOrg
	if _, err := c.request(ctx, token, http.MethodGet, "/orgs/"+url.PathEscape(owner), nil, &o); err != nil {
		return "", err
	}
	if o.Login == "" {
		return "", fmt.Errorf("Organisation %q nicht gefunden", owner)
	}
	return o.Login, nil
}

func (c *githubClient) listRepos(ctx context.Context, token, kind, owner string) ([]Repo, error) {
	var path string
	q := url.Values{}
	q.Set("per_page", "100")
	q.Set("visibility", "all")
	q.Set("sort", "full_name")
	q.Set("direction", "asc")

	switch kind {
	case "org":
		if !validOwner(owner) {
			return nil, errors.New("ungültiger Organisationsname")
		}
		path = "/orgs/" + url.PathEscape(owner) + "/repos"
		q.Set("type", "all")
	case "user":
		path = "/user/repos"
		q.Set("affiliation", "owner")
	case "classic":
		path = "/user/repos"
		q.Set("affiliation", "owner,organization_member,collaborator")
	default:
		return nil, fmt.Errorf("unbekannter Verbindungstyp: %s", kind)
	}

	var all []Repo
	for page := 1; page <= 1000; page++ {
		q.Set("page", fmt.Sprintf("%d", page))
		var batch []Repo
		resp, err := c.request(ctx, token, http.MethodGet, path, q, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !hasNextLink(resp.Header.Get("Link")) {
			break
		}
	}
	all = dedupeRepos(all)
	sort.Slice(all, func(i, j int) bool {
		return strings.ToLower(all[i].FullName) < strings.ToLower(all[j].FullName)
	})
	return all, nil
}

func (c *githubClient) Validate(ctx context.Context, kind, owner, token string) (string, []Repo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil, errors.New("Token fehlt")
	}
	if strings.HasPrefix(token, "github_pat_") && kind == "classic" {
		return "", nil, errors.New("Ein fine-grained Token kann nicht als 'Alles (classic)' verwendet werden. Lege pro Konto/Org eine eigene Verbindung an.")
	}

	login, err := c.getUser(ctx, token)
	if err != nil {
		return "", nil, err
	}

	switch kind {
	case "user":
		if owner != "" && !strings.EqualFold(owner, login) {
			return "", nil, fmt.Errorf("Token gehört zu %q, nicht zu %q", login, owner)
		}
		owner = login
	case "org":
		if !validOwner(owner) {
			return "", nil, errors.New("Organisationsname fehlt oder ist ungültig")
		}
		orgLogin, err := c.getOrg(ctx, token, owner)
		if err != nil {
			return "", nil, err
		}
		owner = orgLogin
	case "classic":
		owner = login
	default:
		return "", nil, fmt.Errorf("unbekannter Verbindungstyp: %s", kind)
	}

	repos, err := c.listRepos(ctx, token, kind, owner)
	if err != nil {
		return "", nil, err
	}
	return owner, repos, nil
}

func hasNextLink(link string) bool {
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}

func dedupeRepos(in []Repo) []Repo {
	seen := map[string]bool{}
	out := make([]Repo, 0, len(in))
	for _, r := range in {
		if r.FullName == "" || seen[r.FullName] {
			continue
		}
		seen[r.FullName] = true
		out = append(out, r)
	}
	return out
}

var ownerRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validOwner(s string) bool {
	return ownerRe.MatchString(s) && s != "." && s != ".."
}

func validRepoFullName(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	return validOwner(parts[0]) && validOwner(parts[1])
}

func isFineGrained(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), "github_pat_")
}

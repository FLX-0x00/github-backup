package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDueNowRequiresEnabledConnection(t *testing.T) {
	cfg := defaultConfig("/volume1/backup/github")
	cfg.Schedule = Schedule{Enabled: true, Weekday: 0, Hour: 3, Minute: 0}
	if dueNow(cfg) {
		t.Fatal("dueNow must be false without an enabled connection")
	}
	cfg.Connections = []Connection{{ID: "c1", Enabled: true}}
	if !dueNow(cfg) {
		t.Fatal("first run should be due when a connection is enabled")
	}
	now := time.Now()
	cfg.LastRun = &now
	if dueNow(cfg) {
		t.Fatal("dueNow must be false directly after a run")
	}
}

func TestNextRunAfter(t *testing.T) {
	// Monday 2026-10-26 12:00 local; next Sunday 03:00.
	after := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local)
	next := nextRunAfter(after, Schedule{Weekday: 0, Hour: 3, Minute: 0})
	if next.Weekday() != time.Sunday || next.Hour() != 3 || next.Minute() != 0 {
		t.Fatalf("unexpected next run: %v", next)
	}
	if !next.After(after) {
		t.Fatalf("next run must be in the future: %v", next)
	}
}

func TestRunStateLifecycle(t *testing.T) {
	s := newRunState()
	s.start()
	s.addTotal(2)
	s.startRepo("owner/repo")
	s.doneRepo()
	view := s.View()
	if !view.Running || view.Total != 2 || view.Done != 1 || view.Current != "owner/repo" {
		t.Fatalf("unexpected running view: %+v", view)
	}
	finished := time.Now()
	s.finish(RunSummary{Status: "success", Finished: finished, Total: 2, LogPath: "/tmp/run.log"}, nil)
	view = s.View()
	if view.Running || view.Status != "success" || view.Done != 2 || view.LogPath != "/tmp/run.log" {
		t.Fatalf("unexpected finished view: %+v", view)
	}
}

func TestCgiAuthEnvOverridesQueryString(t *testing.T) {
	env := cgiAuthEnv("QUERY_STRING=SynoToken=abc123")
	count := 0
	value := ""
	for _, e := range env {
		if strings.HasPrefix(e, "QUERY_STRING=") {
			count++
			value = strings.TrimPrefix(e, "QUERY_STRING=")
		}
	}
	if count != 1 || value != "SynoToken=abc123" {
		t.Fatalf("QUERY_STRING override failed: count=%d value=%q", count, value)
	}
}

func TestCgiDSMUserLoginTokenFlow(t *testing.T) {
	dir := t.TempDir()
	login := dir + "/login.cgi"
	auth := dir + "/authenticate.cgi"
	if err := os.WriteFile(login, []byte("#!/bin/sh\necho '{\"success\":true,\"SynoToken\":\"tok123\",\"username\":\"admin\"}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auth, []byte("#!/bin/sh\ncase \"$QUERY_STRING\" in *SynoToken=tok123*) echo admin ;; esac\n"), 0755); err != nil {
		t.Fatal(err)
	}
	oldLogin, oldAuth := dsmLoginCGI, dsmAuthenticateCGI
	dsmLoginCGI, dsmAuthenticateCGI = login, auth
	defer func() { dsmLoginCGI, dsmAuthenticateCGI = oldLogin, oldAuth }()
	t.Setenv("GITHUB_BACKUP_DEV_USER", "")
	user, err := cgiDSMUser("")
	if err != nil || user != "admin" {
		t.Fatalf("user=%q err=%v", user, err)
	}
}

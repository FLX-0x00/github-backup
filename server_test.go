package main

import (
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

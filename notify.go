package main

import (
	"os"
	"os/exec"
)

func sendNotification(title, message string) {
	path := "/usr/syno/bin/synodsmnotify"
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return
	}
	cmd := exec.Command(path, "@administrators", title, message)
	_ = cmd.Run()
}

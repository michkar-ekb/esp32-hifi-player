//go:build !windows

package main

import (
	"log"
	"net/http"
)

// runTray: the notification-area icon exists only on Windows; elsewhere -tray just runs the server.
func runTray(listen string, lib *Library, data string, h http.Handler, askFolder bool) {
	log.Fatal(http.ListenAndServe(listen, h))
}

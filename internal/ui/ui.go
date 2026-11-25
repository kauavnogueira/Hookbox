package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assetsFS embed.FS

// Handler returns an http.Handler that serves the embedded web interface.
func Handler() http.Handler {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// GetIndexHTML returns the raw HTML bytes of the embedded UI.
func GetIndexHTML() ([]byte, error) {
	return assetsFS.ReadFile("assets/index.html")
}

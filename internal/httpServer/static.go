package httpServer

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/css/*
var staticFiles embed.FS

//go:embed templates/*
var templateFiles embed.FS

// getFileSystem gets the embedded static files and unwraps the 'static' directory
func getFileSystem() http.FileSystem {
	fsys, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return http.FS(fsys)
}

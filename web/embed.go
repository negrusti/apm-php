package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed public/*
var assets embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(assets, "public")
	return http.FileServer(http.FS(root))
}

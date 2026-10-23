package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var assets embed.FS

func FS() fs.FS {
	f, e := fs.Sub(assets, "assets")
	if e != nil {
		panic(e)
	}
	return f
}

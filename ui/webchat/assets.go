package webchatui

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var embedded embed.FS

func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

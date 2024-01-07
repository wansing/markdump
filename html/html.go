package html

import (
	"embed"
	"html/template"
)

//go:embed *
var files embed.FS

func parse(fn ...string) *template.Template {
	return template.Must(template.New(fn[0]).Funcs(template.FuncMap{}).ParseFS(files, fn...))
}

var (
	Dir    = parse("layout.html", "dir.html")
	File   = parse("layout.html", "file.html")
	Index  = parse("layout.html", "index.html")
	Search = parse("layout.html", "search.html")
)

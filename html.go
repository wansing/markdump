package markdump

import (
	"embed"
	"html/template"
)

//go:embed *.html
var files embed.FS

func parse(fn ...string) *template.Template {
	return template.Must(template.New(fn[0]).ParseFS(files, fn...))
}

var (
	dirTmpl    = parse("layout.html", "dir.html")
	fileTmpl   = parse("layout.html", "file.html")
	searchTmpl = parse("layout.html", "search.html")
)

type dirData struct {
	Title  string // layout.html
	Base   string // layout.html
	Search string // layout.html
	Dir    *Dir
}

type fileData struct {
	Title  string // layout.html
	Base   string // layout.html
	Search string // layout.html
	Dir    *Dir   // breadcrumbs
	File   *File
}

type searchData struct {
	Title   string // layout.html
	Base    string // layout.html
	Search  string // layout.html
	Matches []DocumentMatch
}

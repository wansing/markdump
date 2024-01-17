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

type layoutData struct {
	AuthHref        string
	Base            string
	ContainsAuthKey bool
	Search          string
	Title           string
}

type dirData struct {
	layoutData
	Dir *Dir
}

type fileData struct {
	layoutData
	Dir  *Dir // breadcrumbs
	File *File
}

type searchData struct {
	layoutData
	Matches []DocumentMatch
}

package markdump

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blugelabs/bluge"
	"github.com/blugelabs/bluge/index"
	"github.com/blugelabs/bluge/search/highlight"
	"github.com/julienschmidt/httprouter"
	"github.com/wansing/markdump/html"
	"gitlab.com/golang-commonmark/markdown"
	"golang.org/x/exp/maps"
)

var md = markdown.New(markdown.Linkify(true), markdown.Typographer(true))

type Server struct {
	FsDir  string
	Root   *Dir
	Reader *bluge.Reader
}

type Dir struct {
	Title      string
	LinkURL    string
	Subdirs    map[string]*Dir
	SubdirList []*Dir
	Files      map[string]*File
	FileList   []*File
}

// fspath is case-sensitive for reading from filesystem
func LoadDir(fsys fs.FS, fspath string, batch *index.Batch) (*Dir, error) {
	entries, err := fs.ReadDir(fsys, fspath)
	if err != nil {
		return nil, err
	}

	var files = map[string]*File{}
	var subdirs = map[string]*Dir{}
	for _, entry := range entries {
		entrypath := filepath.Join(fspath, entry.Name())
		name := strings.TrimSpace(strings.ToLower(entry.Name()))
		switch {
		case strings.HasPrefix(entry.Name(), "."):
			continue

		case entry.IsDir():
			subdir, err := LoadDir(fsys, entrypath, batch)
			if err != nil {
				return nil, err
			}
			subdirs[name] = subdir

			doc := bluge.NewDocument(entrypath) // _id
			doc.AddField(bluge.NewTextField("path", fspath).StoreValue())
			doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name"}))
			batch.Update(doc.ID(), doc)

		case strings.HasSuffix(name, ".md"):
			file, err := LoadFile(fsys, entrypath)
			if err != nil {
				return nil, err
			}
			files[name] = file

			doc := bluge.NewDocument(entrypath) // _id
			doc.AddField(bluge.NewTextField("path", fspath).StoreValue())
			doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewTextField("content", file.MdContent).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name", "content"}))
			batch.Update(doc.ID(), doc)
		}
	}

	var title = filepath.Base(fspath)
	if title == "." {
		title = ""
	}

	var subdirList = maps.Values(subdirs)
	sort.Slice(subdirList, func(i, j int) bool {
		return subdirList[i].LinkURL < subdirList[j].LinkURL
	})

	var fileList = maps.Values(files)
	sort.Slice(fileList, func(i, j int) bool {
		return fileList[i].LinkURL < fileList[j].LinkURL
	})

	return &Dir{
		Title:      title,
		LinkURL:    fspath,
		Subdirs:    subdirs,
		SubdirList: subdirList,
		Files:      files,
		FileList:   fileList,
	}, nil
}

type File struct {
	Title       string
	HTMLContent template.HTML
	MdContent   string
	LinkURL     string
}

func LoadFile(fsys fs.FS, fspath string) (*File, error) {
	bs, err := fs.ReadFile(fsys, fspath)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSuffix(filepath.Base(fspath), ".md")
	return &File{
		Title:       title,
		HTMLContent: template.HTML(md.RenderToString(bs)),
		MdContent:   string(bs),
		LinkURL:     fspath,
	}, nil
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if search := r.URL.Query().Get("s"); search != "" {
		srv.HandleSearchHTML(w, r, search)
		return
	}

	// request path
	reqpath := strings.FieldsFunc(r.URL.Path, func(r rune) bool { return r == '/' })
	if len(reqpath) > 16 {
		http.Error(w, "path too long", http.StatusUnprocessableEntity)
		return
	}
	for i := range reqpath {
		reqpath[i] = strings.ToLower(strings.TrimSpace(reqpath[i]))
	}

	// follow dirs
	var dir = srv.Root
	for len(reqpath) > 0 {
		var slug string
		slug, reqpath = reqpath[0], reqpath[1:]
		newdir, ok := dir.Subdirs[slug]
		if !ok {
			// restore
			reqpath = append(reqpath, slug)
			break
		}
		dir = newdir
	}

	switch len(reqpath) {
	case 0:
		html.Dir.Execute(w, dir)
	case 1:
		slug := reqpath[0]
		file, ok := dir.Files[slug]
		if !ok {
			http.NotFound(w, r)
			return
		}
		html.File.Execute(w, file)
	default:
		http.NotFound(w, r)
	}
}

func (srv *Server) HandleSearchHTML(w http.ResponseWriter, r *http.Request, search string) {
	search = strings.TrimSpace(search)
	result, err := srv.search(search)
	if err != nil {
		return
	}
	html.Search.Execute(w, result)
}

func (srv *Server) HandleSearchAPI(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	input := ps.ByName("search")
	input = strings.TrimPrefix(input, "/") // catch-all parameter value has leading slash
	result, err := srv.search(input)
	if err != nil {
		return
	}
	json.NewEncoder(w).Encode(result)
}

type DocumentMatch struct {
	Href    template.URL  `json:"href"`
	Path    string        `json:"path"` // without name
	Name    template.HTML `json:"name"`
	Content template.HTML `json:"content"` // empty for dirs
}

func (srv *Server) search(input string) ([]DocumentMatch, error) {
	// crop input, lowercase (required for bluge.PrefixQuery and bluge.WildcardQuery, which don't have an analyzer), limit to four words, remove too long words and duplicates
	if len(input) > 128 {
		input = input[:128]
	}
	input = strings.ToLower(input)
	words := strings.Fields(input)
	if len(words) > 4 {
		words = words[:4]
	}
	var wordMap = make(map[string]any)
	for _, word := range words {
		if len(word) <= 32 {
			wordMap[word] = struct{}{}
		}
	}

	query := bluge.NewBooleanQuery()
	for word := range wordMap {
		wordQuery := bluge.NewBooleanQuery()
		wordQuery.AddShould(bluge.NewFuzzyQuery(word).SetField("_all").SetFuzziness(1))
		wordQuery.AddShould(bluge.NewPrefixQuery(word).SetField("_all"))
		wordQuery.AddShould(bluge.NewWildcardQuery("*" + word + "*").SetField("_all"))
		query.AddMust(wordQuery)
	}
	request := bluge.NewTopNSearch(10, query).IncludeLocations()

	highlighter := highlight.NewHTMLHighlighter()

	dmi, err := srv.Reader.Search(context.Background(), request)
	if err != nil {
		return nil, err
	}
	var matches []DocumentMatch
	for next, err := dmi.Next(); err == nil && next != nil; next, err = dmi.Next() {
		var match DocumentMatch
		err = next.VisitStoredFields(func(field string, value []byte) bool {
			switch field {
			case "_id":
				match.Href = template.URL(value)
			case "path":
				var sb strings.Builder
				for _, item := range strings.FieldsFunc(string(value), func(r rune) bool { return r == '/' }) {
					if item == "." {
						continue
					}
					sb.WriteString(item)
					sb.WriteString(" / ")
				}
				match.Path = sb.String()
			case "name":
				match.Name = template.HTML(value)
				if locations, ok := next.Locations[field]; ok {
					if fragment := highlighter.BestFragment(locations, value); len(fragment) > 0 {
						match.Name = template.HTML(fragment)
					}
				}
			case "content":
				match.Content = template.HTML(value)
				if locations, ok := next.Locations[field]; ok {
					if fragment := highlighter.BestFragment(locations, value); len(fragment) > 0 {
						match.Content = template.HTML(fragment)
					}
				}
			}
			return true
		})
		if err != nil {
			return nil, err
		}

		matches = append(matches, match)
	}
	if err != nil {
		return nil, err
	}

	return matches, nil
}

func (srv *Server) Reload() error {
	indexWriter, err := bluge.OpenWriter(bluge.InMemoryOnlyConfig())
	if err != nil {
		return err
	}
	batch := bluge.NewBatch()
	root, err := LoadDir(os.DirFS(srv.FsDir), ".", batch)
	if err != nil {
		panic(err)
	}
	if err := indexWriter.Batch(batch); err != nil {
		return err
	}

	srv.Root = root
	srv.Reader, _ = indexWriter.Reader() // reader is a snapshot
	return nil
}

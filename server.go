package markdump

import (
	"context"
	"encoding/json"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/blugelabs/bluge"
	"github.com/blugelabs/bluge/index"
	"github.com/blugelabs/bluge/search/highlight"
	"github.com/julienschmidt/httprouter"
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
	Path       []*Dir
	Title      string
	URL        string
	Subdirs    map[string]*Dir
	SubdirList []*Dir
	Files      map[string]*File
	FileList   []*File
}

// Load loads subdirs and files of dir.
func (dir *Dir) Load(fsys fs.FS, batch *index.Batch) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return err
	}

	var files = map[string]*File{}
	var subdirs = map[string]*Dir{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // skip hidden files
		}
		name := strings.TrimSpace(entry.Name())
		slug := Slugify(name)
		if entry.IsDir() {
			subdir := &Dir{
				Path:  append(dir.Path, dir),
				Title: name,
				URL:   path.Join(dir.URL, slug),
			}
			subfs, err := fs.Sub(fsys, name)
			if err != nil {
				return err
			}
			if err := subdir.Load(subfs, batch); err != nil {
				return err
			}
			subdirs[slug] = subdir

			doc := bluge.NewDocument(subdir.URL) // _id
			doc.AddField(bluge.NewTextField("path", subdir.PathString()).StoreValue())
			doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name"}))
			batch.Update(doc.ID(), doc)
			continue
		}
		if strings.HasSuffix(name, ".md") {
			mdContent, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			title := strings.TrimSuffix(name, ".md")
			slug := Slugify(title)
			file := &File{
				Title:       title,
				HTMLContent: template.HTML(md.RenderToString(mdContent)),
				URL:         path.Join(dir.URL, slug),
			}
			files[slug] = file

			doc := bluge.NewDocument(file.URL) // _id
			doc.AddField(bluge.NewTextField("path", dir.PathString()).StoreValue())
			doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewTextField("content", string(mdContent)).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name", "content"}))
			batch.Update(doc.ID(), doc)
		}
	}

	var subdirList = maps.Values(subdirs)
	sort.Slice(subdirList, func(i, j int) bool {
		return subdirList[i].URL < subdirList[j].URL
	})

	var fileList = maps.Values(files)
	sort.Slice(fileList, func(i, j int) bool {
		return fileList[i].URL < fileList[j].URL
	})

	dir.Subdirs = subdirs
	dir.SubdirList = subdirList
	dir.Files = files
	dir.FileList = fileList
	return nil
}

func (dir *Dir) PathString() string {
	var sb strings.Builder
	for _, dir := range dir.Path {
		sb.WriteString(dir.Title)
		sb.WriteString(" / ")
	}
	return sb.String()
}

type File struct {
	Title       string
	HTMLContent template.HTML
	URL         string
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
		if err := dirTmpl.Execute(w, dirData{
			Title: dir.Title,
			Dir:   dir,
		}); err != nil {
			log.Println(err)
		}
	case 1:
		slug := reqpath[0]
		file, ok := dir.Files[slug]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if err := fileTmpl.Execute(w, fileData{
			Title: file.Title,
			Dir:   dir,
			File:  file,
		}); err != nil {
			log.Println(err)
		}
	default:
		http.NotFound(w, r)
	}
}

func (srv *Server) HandleSearchHTML(w http.ResponseWriter, r *http.Request, search string) {
	search = strings.TrimSpace(search)
	matches, err := srv.search(search)
	if err != nil {
		return
	}
	err = searchTmpl.Execute(w, searchData{
		Title:   "Search: " + search,
		Search:  search,
		Matches: matches,
	})
	if err != nil {
		log.Println(err)
	}
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
				match.Path = string(value)
			case "name":
				match.Name = template.HTML(value)
				if locations, ok := next.Locations[field]; ok {
					if fragment := highlighter.BestFragment(locations, value); len(fragment) > 0 {
						match.Name = template.HTML(fragment)
					}
				}
			case "content":
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
	// update root and search index
	indexWriter, err := bluge.OpenWriter(bluge.InMemoryOnlyConfig())
	if err != nil {
		return err
	}
	batch := bluge.NewBatch()

	root := &Dir{
		Title: "Home",
		URL:   "/",
	}
	err = root.Load(os.DirFS(srv.FsDir), batch)
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

// Slugify returns a modified version of the given string in lower case, with [a-z0-9] retained and a dash in each gap.
func Slugify(s string) string {
	s = strings.ToLower(s)
	strs := strings.FieldsFunc(s, func(r rune) bool {
		if 'a' <= r && r <= 'z' {
			return false
		}
		if '0' <= r && r <= '9' {
			return false
		}
		return true
	})
	return strings.Join(strs, "-")
}

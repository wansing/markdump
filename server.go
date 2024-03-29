package markdump

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blugelabs/bluge"
	"github.com/blugelabs/bluge/index"
	"github.com/blugelabs/bluge/search/highlight"
	"gitlab.com/golang-commonmark/markdown"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var md = markdown.New(markdown.HTML(true), markdown.Linkify(true), markdown.Typographer(true))

type Server struct {
	AuthTokens []string
	FsDir      string
	Root       *Dir
	Reader     *bluge.Reader
	RootTitle  string
}

type Entry interface {
	IsDir() bool
	Title() string
	URL() string
}

type Dir struct {
	FsPath    string // required for serving files by slug
	Path      []*Dir // including root
	title     string
	url       string
	Subdirs   map[string]*Dir
	Files     map[string]*File
	EntryList []Entry
}

func (dir *Dir) IsDir() bool {
	return true
}

// Load loads subdirs and files of dir.
func (dir *Dir) Load(batch *index.Batch) error {
	entries, err := os.ReadDir(dir.FsPath)
	if err != nil {
		return err
	}

	var files = map[string]*File{}
	var subdirs = map[string]*Dir{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue // skip hidden files
		}
		name := entry.Name()
		slug := Slugify(name)
		if entry.IsDir() {
			subdir := &Dir{
				FsPath: filepath.Join(dir.FsPath, name),
				Path:   append(dir.Path, dir),
				title:  name,
				url:    path.Join(dir.url, slug),
			}
			if err := subdir.Load(batch); err != nil {
				return err
			}
			if len(subdir.Subdirs) > 0 || len(subdir.Files) > 0 {
				subdirs[slug] = subdir

				doc := bluge.NewDocument(subdir.url) // _id
				doc.AddField(bluge.NewTextField("path", subdir.PathString()).StoreValue())
				doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
				doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name"}))
				batch.Update(doc.ID(), doc)
			}
			continue
		}
		if strings.HasSuffix(name, ".md") {
			mdContent, err := os.ReadFile(filepath.Join(dir.FsPath, name))
			if err != nil {
				return err
			}
			title := strings.TrimSuffix(name, ".md")
			slug := Slugify(title)
			file := &File{
				title:       title,
				HTMLContent: template.HTML(md.RenderToString(mdContent)),
				url:         path.Join(dir.url, slug),
			}
			files[slug] = file

			doc := bluge.NewDocument(file.url) // _id
			doc.AddField(bluge.NewTextField("path", dir.PathString()).StoreValue())
			doc.AddField(bluge.NewTextField("name", entry.Name()).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewTextField("content", string(mdContent)).SearchTermPositions().StoreValue())
			doc.AddField(bluge.NewCompositeFieldIncluding("_all", []string{"name", "content"}))
			batch.Update(doc.ID(), doc)
		}
	}

	var entryList = make([]Entry, 0, len(subdirs)+len(files))
	for _, subdir := range subdirs {
		entryList = append(entryList, subdir)
	}
	for _, file := range files {
		entryList = append(entryList, file)
	}
	sort.Slice(entryList, func(i, j int) bool {
		return entryList[i].URL() < entryList[j].URL()
	})

	dir.Subdirs = subdirs
	dir.Files = files
	dir.EntryList = entryList
	return nil
}

// without root, but with dir
func (dir *Dir) PathString() string {
	path := append(dir.Path, dir) // with dir
	path = path[1:]               // without root
	var sb strings.Builder
	for _, d := range path {
		sb.WriteString(d.title)
		sb.WriteString(" / ")
	}
	return sb.String()
}

func (dir *Dir) Readme() *File {
	return dir.Files["readme"]
}

func (dir *Dir) Title() string {
	return dir.title
}

func (dir *Dir) URL() string {
	return dir.url
}

type File struct {
	title       string
	HTMLContent template.HTML
	url         string
}

func (file *File) IsDir() bool {
	return false
}

func (file *File) Title() string {
	return file.title
}

func (file *File) URL() string {
	return file.url
}

// returns auth token for AuthHref and whether request is authenticated
func (srv *Server) authenticated(w http.ResponseWriter, r *http.Request) (string, bool) {
	if slices.Contains(srv.AuthTokens, "public") {
		return "", true
	}

	var token string
	if cookie, err := r.Cookie("auth"); err == nil {
		token = cookie.Value
	}

	if queryToken := r.URL.Query().Get("auth"); queryToken != "" && queryToken != token {
		// update cookie and var token
		http.SetCookie(w, &http.Cookie{
			Name:     "auth",
			Value:    queryToken,
			Expires:  time.Now().AddDate(0, 0, 30),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		token = queryToken
	}

	if slices.Contains(srv.AuthTokens, token) {
		return token, true
	} else {
		return "", false
	}
}

func (srv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, authenticated := srv.authenticated(w, r)
	if !authenticated {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var authHref string
	if token != "" {
		var u = *r.URL
		var query = u.Query()
		query.Set("auth", token)
		u.RawQuery = query.Encode()
		authHref = u.String()
	}

	if search := r.URL.Query().Get("s"); search != "" {
		srv.handleSearchHTML(w, r, authHref, search)
		return
	}

	// request path
	reqpath := strings.FieldsFunc(r.URL.Path, func(r rune) bool { return r == '/' })
	if len(reqpath) > 16 {
		http.Error(w, "path too long", http.StatusUnprocessableEntity)
		return
	}

	// follow dirs
	var dir = srv.Root
	for len(reqpath) > 0 {
		newdir, ok := dir.Subdirs[reqpath[0]]
		if !ok {
			break
		}
		dir = newdir
		reqpath = reqpath[1:]
	}

	var base string
	if dir.url != "" && !strings.HasSuffix(dir.url, "/") {
		base = dir.url + "/"
	}

	// serve dir
	if len(reqpath) == 0 {
		if err := dirTmpl.Execute(w, dirData{
			layoutData: layoutData{
				AuthHref:        authHref,
				Base:            base,
				ContainsAuthKey: r.URL.Query().Has("auth"),
				Title:           dir.title,
			},
			Dir: dir,
		}); err != nil {
			log.Println(err)
		}
		return
	}

	// serve markdown file
	if file, ok := dir.Files[reqpath[0]]; ok {
		if err := fileTmpl.Execute(w, fileData{
			layoutData: layoutData{
				AuthHref:        authHref,
				Base:            base,
				ContainsAuthKey: r.URL.Query().Has("auth"),
				Title:           file.title,
			},
			Dir:  dir,
			File: file,
		}); err != nil {
			log.Println(err)
		}
		return
	}

	// serve other file
	http.ServeFile(w, r, filepath.Join(dir.FsPath, filepath.Join(reqpath...)))
}

func (srv *Server) handleSearchHTML(w http.ResponseWriter, r *http.Request, authHref, search string) {
	search = strings.TrimSpace(search)
	matches, err := srv.search(search)
	if err != nil {
		return
	}
	err = searchTmpl.Execute(w, searchData{
		layoutData: layoutData{
			AuthHref:        authHref,
			ContainsAuthKey: r.URL.Query().Has("auth"),
			Search:          search,
			Title:           "Search: " + search,
		},
		Matches:   matches,
		RootTitle: srv.RootTitle,
	})
	if err != nil {
		log.Println(err)
	}
}

func (srv *Server) HandleSearchAPI(w http.ResponseWriter, r *http.Request) {
	_, authenticated := srv.authenticated(w, r)
	if !authenticated {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	input := r.URL.Query().Get("s")
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
		FsPath: srv.FsDir,
		title:  srv.RootTitle,
		url:    "/",
	}
	err = root.Load(batch)
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

// replaces diacritic and accent characters with the underlying character
var transformer = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// Slugify returns a modified version of the given string in lower case, with [a-z0-9] retained and a dash in each gap.
func Slugify(s string) string {
	s = strings.TrimSpace(s)
	s, _, _ = transform.String(transformer, s)
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

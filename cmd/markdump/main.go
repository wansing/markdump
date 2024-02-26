package main

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/wansing/markdump"
	"github.com/wansing/markdump/static"
	"github.com/wansing/seal"
)

func main() {
	authTokens := strings.Fields(os.Getenv("AUTH"))
	if len(authTokens) == 0 {
		log.Fatalln("AUTH missing")
	}
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8134"
	}
	reloadSecret := os.Getenv("RELOAD_SECRET")
	if reloadSecret == "" {
		var bs = make([]byte, 16)
		if _, err := rand.Read(bs); err != nil {
			log.Fatalf("error making random secret: %v", err)
		}
		reloadSecret = base64.RawURLEncoding.EncodeToString(bs)
		log.Printf("generated temporary reload secret: %s", reloadSecret)
	}
	repoDir := os.Getenv("REPO")
	if repoDir == "" {
		repoDir = "."
	}
	rootTitle := os.Getenv("TITLE")
	if rootTitle == "" {
		rootTitle = "Home"
	}

	srv := &markdump.Server{
		AuthTokens: authTokens,
		FsDir:      repoDir,
		RootTitle:  rootTitle,
	}
	if err := srv.Reload(); err != nil {
		log.Fatalf("error loading: %v", err)
	}

	reloadHandler := seal.GitReloadHandler(reloadSecret, repoDir, srv.Reload)

	log.Printf("listening to %s", listen)
	http.Handle("GET /", srv)
	http.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files))))
	http.HandleFunc("GET /reload", reloadHandler)
	http.HandleFunc("POST /reload", reloadHandler)
	http.HandleFunc("GET /search", srv.HandleSearchAPI)
	http.ListenAndServe(listen, nil)
}

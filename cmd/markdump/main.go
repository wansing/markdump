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
	repoDir := os.Getenv("REPO")
	if repoDir == "" {
		repoDir = "."
	}
	secret := os.Getenv("SECRET")
	if secret == "" {
		var bs = make([]byte, 16)
		if _, err := rand.Read(bs); err != nil {
			log.Fatalf("error making random secret: %v", err)
		}
		secret = base64.RawURLEncoding.EncodeToString(bs)
		log.Printf("generated temporary reload secret: %s", secret)
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

	log.Printf("listening to %s", listen)
	http.Handle("/", srv)                                                                         // TODO "GET /"
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static.Files)))) // TODO "GET /static/"
	http.HandleFunc("/reload", seal.GitReloadHandler(secret, repoDir, srv.Reload))                // TODO "GET /reload"
	http.HandleFunc("/search", srv.HandleSearchAPI)                                               // TODO "GET /search"
	http.ListenAndServe(listen, nil)
}

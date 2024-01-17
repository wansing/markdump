package main

import (
	"crypto/rand"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/julienschmidt/httprouter"
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

	srv := &markdump.Server{
		AuthTokens: authTokens,
		FsDir:      repoDir,
	}
	if err := srv.Reload(); err != nil {
		log.Fatalf("error loading: %v", err)
	}

	log.Printf("listening to %s", listen)
	router := httprouter.New()
	router.HandlerFunc(http.MethodGet, "/reload", seal.GitReloadHandler(secret, repoDir, srv.Reload))
	router.HandlerFunc(http.MethodGet, "/search", srv.HandleSearchAPI)
	router.ServeFiles("/static/*filepath", http.FS(static.Files))
	router.NotFound = srv // chain handlers
	http.ListenAndServe(listen, router)
}

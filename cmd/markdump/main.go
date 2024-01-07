package main

import (
	"log"
	"net/http"
	"os"

	"github.com/julienschmidt/httprouter"
	"github.com/wansing/markdump"
	"github.com/wansing/markdump/html/static"
)

func main() {
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8134"
	}

	srv := &markdump.Server{
		FsDir: "./example",
	}
	if err := srv.Reload(); err != nil {
		log.Fatalf("error loading: %v", err)
	}

	log.Printf("listening to %s", listen)
	router := httprouter.New()
	router.Handle(http.MethodGet, "/search/:search", srv.HandleSearchAPI)
	router.ServeFiles("/static/*filepath", http.FS(static.Files))
	router.NotFound = srv
	http.ListenAndServe(listen, router)
}

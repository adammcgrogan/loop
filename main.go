package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/adammcgrogan/loop/internal/handler"
	"github.com/adammcgrogan/loop/internal/ors"
	"github.com/adammcgrogan/loop/internal/store"
)

//go:embed templates static
var content embed.FS

func main() {
	loadDotEnv()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	st, err := store.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	tmpl := template.Must(template.ParseFS(content, "templates/*.html"))
	h := handler.New(tmpl, ors.NewClient(os.Getenv("ORS_API_KEY")), st)

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServer(http.FS(content)))
	mux.HandleFunc("GET /", h.Index)
	mux.HandleFunc("GET /share/{id}", h.SharePage)
	mux.HandleFunc("POST /api/route", h.Route)
	mux.HandleFunc("POST /api/share", h.Share)
	mux.HandleFunc("GET /api/share/{id}", h.ShareData)
	mux.HandleFunc("POST /api/export/gpx", h.ExportGPX)

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(key), strings.TrimSpace(val))
	}
}

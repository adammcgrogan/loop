package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/adammcgrogan/loop/internal/gpx"
	"github.com/adammcgrogan/loop/internal/ors"
	"github.com/adammcgrogan/loop/internal/store"
)

type Handler struct {
	tmpl  *template.Template
	ors   *ors.Client
	store *store.Store
}

func New(tmpl *template.Template, orsClient *ors.Client, st *store.Store) *Handler {
	return &Handler{tmpl: tmpl, ors: orsClient, store: st}
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	if err := h.tmpl.ExecuteTemplate(w, "index.html", nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("template: %v", err)
	}
}

func (h *Handler) Route(w http.ResponseWriter, r *http.Request) {
	var req ors.RouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	route, err := h.ors.GenerateRoute(req)
	if err != nil {
		log.Printf("route generation: %v", err)
		http.Error(w, "failed to generate route", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(route)
}

func (h *Handler) ExportGPX(w http.ResponseWriter, r *http.Request) {
	var req gpx.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/gpx+xml")
	w.Header().Set("Content-Disposition", `attachment; filename="loop-route.gpx"`)
	w.Write([]byte(gpx.Build(req)))
}

// Share saves a generated route and returns a short share ID.
func (h *Handler) Share(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Route json.RawMessage `json:"route"`
		Meta  json.RawMessage `json:"meta"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	id, err := h.store.Save(r.Context(), string(body.Route), string(body.Meta))
	if err != nil {
		log.Printf("share save: %v", err)
		http.Error(w, "failed to save share", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q}`, id)
}

// ShareData returns the stored route and meta for a given share ID.
func (h *Handler) ShareData(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	route, meta, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		log.Printf("share get: %v", err)
		http.Error(w, "failed to load share", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"route":%s,"meta":%s}`, route, meta)
}

// SharePage serves the app shell for shared route URLs; JS handles the rest.
func (h *Handler) SharePage(w http.ResponseWriter, r *http.Request) {
	if err := h.tmpl.ExecuteTemplate(w, "index.html", nil); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("template: %v", err)
	}
}

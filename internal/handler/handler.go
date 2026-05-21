package handler

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"

	"github.com/adammcgrogan/loop/internal/gpx"
	"github.com/adammcgrogan/loop/internal/ors"
)

type Handler struct {
	tmpl *template.Template
	ors  *ors.Client
}

func New(tmpl *template.Template, orsClient *ors.Client) *Handler {
	return &Handler{tmpl: tmpl, ors: orsClient}
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
	w.Header().Set("Content-Disposition", `attachment; filename="circuit-route.gpx"`)
	w.Write([]byte(gpx.Build(req)))
}

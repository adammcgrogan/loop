package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/adammcgrogan/loop/internal/gpx"
	"github.com/adammcgrogan/loop/internal/ors"
	"github.com/adammcgrogan/loop/internal/store"
	"github.com/adammcgrogan/loop/internal/sysmetrics"
)

type Handler struct {
	tmpl          *template.Template
	ors           *ors.Client
	store         *store.Store
	adminUsername string
	adminPassword string
	startTime     time.Time
}

type adminTemplateData struct {
	store.Metrics
	Sys sysmetrics.Metrics
}

func New(tmpl *template.Template, orsClient *ors.Client, st *store.Store, adminUsername, adminPassword string) *Handler {
	return &Handler{
		tmpl:          tmpl,
		ors:           orsClient,
		store:         st,
		adminUsername: adminUsername,
		adminPassword: adminPassword,
		startTime:     time.Now(),
	}
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	h.store.LogEvent(r.Context(), store.EventPageView, clientIP(r), r.UserAgent(), r.Referer())

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

	h.store.LogEvent(r.Context(), store.EventRouteGenerated, clientIP(r), r.UserAgent(), "")

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

	h.store.LogEvent(r.Context(), store.EventShareCreated, clientIP(r), r.UserAgent(), "")

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%q}`, id)
}

type shareMeta struct {
	Distance int    `json:"distance"`
	Surface  string `json:"surface"`
	Hills    string `json:"hills"`
}

type sharePageData struct {
	DistanceKm string
	TimeMin    int
	Surface    string
	Hills      string
	RouteJSON  template.JS
}

func (h *Handler) SharePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	route, metaStr, err := h.store.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "Route not found", http.StatusNotFound)
			return
		}
		log.Printf("share get: %v", err)
		http.Error(w, "failed to load share", http.StatusInternalServerError)
		return
	}

	h.store.LogEvent(r.Context(), store.EventShareViewed, clientIP(r), r.UserAgent(), r.Referer())

	var meta shareMeta
	json.Unmarshal([]byte(metaStr), &meta)

	surface := "Roads"
	if meta.Surface == "trail" {
		surface = "Trails"
	}
	hills := "Any"
	if meta.Hills == "flat" {
		hills = "Prefer flat"
	}

	data := sharePageData{
		DistanceKm: fmt.Sprintf("%.1f km", float64(meta.Distance)/1000),
		TimeMin:    meta.Distance * 6 / 1000,
		Surface:    surface,
		Hills:      hills,
		RouteJSON:  template.JS(route),
	}

	if err := h.tmpl.ExecuteTemplate(w, "share.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("share template: %v", err)
	}
}

func (h *Handler) Admin(w http.ResponseWriter, r *http.Request) {
	if h.adminUsername == "" || h.adminPassword == "" {
		http.Error(w, "Admin not configured", http.StatusForbidden)
		return
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != h.adminUsername || pass != h.adminPassword {
		w.Header().Set("WWW-Authenticate", `Basic realm="Loop Admin"`)
		http.Error(w, "Unauthorised", http.StatusUnauthorized)
		return
	}

	metrics, err := h.store.Metrics(r.Context())
	if err != nil {
		log.Printf("admin metrics: %v", err)
		http.Error(w, "failed to load metrics", http.StatusInternalServerError)
		return
	}

	data := adminTemplateData{
		Metrics: metrics,
		Sys:     sysmetrics.Gather(h.startTime),
	}

	if err := h.tmpl.ExecuteTemplate(w, "admin.html", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		log.Printf("admin template: %v", err)
	}
}

// clientIP extracts the real client IP, respecting Railway's proxy headers.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return strings.Split(fwd, ",")[0]
	}
	return r.RemoteAddr
}

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
go run .          # run locally (reads .env for ORS_API_KEY)
go build ./...    # compile check
```

No test suite yet. No linter configured.

## Architecture

Single-binary Go web app. The binary embeds `templates/` and `static/` via `//go:embed` in `main.go`, so no files need to be deployed alongside it — relevant for the Dockerfile and Railway hosting.

**Request flow:**
1. Browser → `GET /` → `handler.Index` renders `templates/index.html`
2. User clicks Generate → `POST /api/route` → `handler.Route` → `ors.Client.GenerateRoute` → proxies to OpenRouteService, returns GeoJSON
3. User clicks Export → `POST /api/export/gpx` → `handler.ExportGPX` → `gpx.Build` → returns GPX file download

**ORS distance correction (`internal/ors/client.go`):**
ORS round-trip routing overshoots the requested distance by a variable amount. The client keeps a learned `factor` (starts at 0.65) and updates it as an exponential moving average after each generation. Max 2 ORS API calls per user request — first with the learned factor, one correction if the result is outside ±15%.

**Frontend (`static/app.js`):**
Vanilla JS + Leaflet. No build step. Route is drawn as a cased polyline (white outline + red line) with directional arrows via `leaflet-polylinedecorator`. The start marker is a custom `L.divIcon`. All map state (`lat`, `lng`, `seed`, `routeCoords`) lives in a plain `state` object.

## Environment

- `ORS_API_KEY` — required, from account.heigit.org
- `PORT` — defaults to 8080; Railway sets this automatically
- `.env` file is loaded by `loadDotEnv()` in `main.go` for local dev only

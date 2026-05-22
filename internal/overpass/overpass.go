// Package overpass queries the Overpass API for OpenStreetMap features
// we can build loops around (parks, commons, recreation grounds).
package overpass

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Public Overpass mirrors. We try them in order until one responds.
var endpoints = []string{
	"https://overpass-api.de/api/interpreter",
	"https://overpass.kumi.systems/api/interpreter",
	"https://overpass.private.coffee/api/interpreter",
}

// Loop is a closed feature suitable for lapping.
type Loop struct {
	Coords    [][2]float64 // ordered [lat, lng] pairs of the boundary
	Perimeter float64      // metres
	CentroidLat, CentroidLng float64
}

type response struct {
	Elements []struct {
		Type     string             `json:"type"`
		Geometry []map[string]float64 `json:"geometry"`
	} `json:"elements"`
}

// FindLoops returns nearby park-like features whose perimeter is within
// [minPerimeter, maxPerimeter] metres. Results are ordered by suitability
// for the given target distance (closer to start and perimeter that divides
// distance cleanly ranks higher).
func FindLoops(lat, lng float64, radiusM int, minPerimeter, maxPerimeter, targetDistance float64) ([]Loop, error) {
	if cached, ok := cacheLookup(lat, lng, radiusM); ok {
		return filterAndScore(cached, lat, lng, minPerimeter, maxPerimeter, targetDistance), nil
	}

	query := fmt.Sprintf(`[out:json][timeout:20];
(
  way["leisure"~"^(park|common|recreation_ground|garden|nature_reserve)$"](around:%d,%f,%f);
  way["landuse"~"^(recreation_ground|village_green)$"](around:%d,%f,%f);
);
out geom;`, radiusM, lat, lng, radiusM, lat, lng)

	data, err := fetchOverpass(query)
	if err != nil {
		return nil, err
	}

	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}

	// Decode every closed way into a Loop with its perimeter; cache the lot,
	// then apply per-request filtering on top.
	all := make([]Loop, 0, len(r.Elements))
	for _, el := range r.Elements {
		if len(el.Geometry) < 4 {
			continue
		}
		coords := make([][2]float64, 0, len(el.Geometry))
		for _, g := range el.Geometry {
			coords = append(coords, [2]float64{g["lat"], g["lon"]})
		}
		first, last := coords[0], coords[len(coords)-1]
		if first[0] != last[0] || first[1] != last[1] {
			continue
		}
		cLat, cLng := centroid(coords)
		all = append(all, Loop{
			Coords:      coords,
			Perimeter:   perimeter(coords),
			CentroidLat: cLat,
			CentroidLng: cLng,
		})
	}

	cacheStore(lat, lng, radiusM, all)
	return filterAndScore(all, lat, lng, minPerimeter, maxPerimeter, targetDistance), nil
}

func filterAndScore(all []Loop, lat, lng, minPerimeter, maxPerimeter, targetDistance float64) []Loop {
	var out []Loop
	for _, l := range all {
		if l.Perimeter < minPerimeter || l.Perimeter > maxPerimeter {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		return score(out[i], lat, lng, targetDistance) < score(out[j], lat, lng, targetDistance)
	})
	return out
}

// ── cache ─────────────────────────────────────────────

type cacheEntry struct {
	lat, lng float64
	radius   int
	loops    []Loop
	at       time.Time
}

var (
	cacheMu      sync.Mutex
	cacheEntries []cacheEntry
)

const cacheTTL = 6 * time.Hour

// cacheLookup returns a previously fetched set if any cached query covers the
// requested point with at least the requested radius and is still fresh.
func cacheLookup(lat, lng float64, radius int) ([]Loop, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	now := time.Now()
	kept := cacheEntries[:0]
	var hit []Loop
	for _, e := range cacheEntries {
		if now.Sub(e.at) > cacheTTL {
			continue
		}
		kept = append(kept, e)
		if hit != nil {
			continue
		}
		d := haversine(lat, lng, e.lat, e.lng)
		// Only reuse if the cached search radius fully covers what we want now.
		if d+float64(radius) <= float64(e.radius) {
			hit = e.loops
		}
	}
	cacheEntries = kept
	return hit, hit != nil
}

func cacheStore(lat, lng float64, radius int, loops []Loop) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cacheEntries = append(cacheEntries, cacheEntry{lat: lat, lng: lng, radius: radius, loops: loops, at: time.Now()})
	// Bound cache size.
	if len(cacheEntries) > 100 {
		cacheEntries = cacheEntries[len(cacheEntries)-100:]
	}
}

func fetchOverpass(query string) ([]byte, error) {
	form := url.Values{"data": {query}}.Encode()
	httpClient := &http.Client{Timeout: 25 * time.Second}

	var lastErr error
	for _, endpoint := range endpoints {
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "loop-route-builder/1.0 (https://github.com/adammcgrogan/loop)")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("overpass %s: %d", endpoint, resp.StatusCode)
			continue
		}
		return data, nil
	}
	return nil, lastErr
}

// score is lower-is-better. Combines distance to start with how cleanly
// the perimeter divides the target distance.
func score(l Loop, lat, lng, target float64) float64 {
	d := haversine(lat, lng, l.CentroidLat, l.CentroidLng)
	laps := target / l.Perimeter
	// Penalise non-integer lap counts and very short approaches.
	lapPenalty := math.Abs(laps-math.Round(laps)) * l.Perimeter
	return d + lapPenalty
}

func perimeter(coords [][2]float64) float64 {
	var total float64
	for i := 0; i < len(coords)-1; i++ {
		total += haversine(coords[i][0], coords[i][1], coords[i+1][0], coords[i+1][1])
	}
	return total
}

func centroid(coords [][2]float64) (float64, float64) {
	var lat, lng float64
	n := float64(len(coords) - 1) // closed ring, last == first
	for i := 0; i < len(coords)-1; i++ {
		lat += coords[i][0]
		lng += coords[i][1]
	}
	return lat / n, lng / n
}

func haversine(lat1, lng1, lat2, lng2 float64) float64 {
	const R = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLng := (lng2 - lng1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

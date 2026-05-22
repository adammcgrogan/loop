package ors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"

	"github.com/adammcgrogan/loop/internal/overpass"
)

const (
	baseURL = "https://api.openrouteservice.org/v2/directions"
	// Overlap threshold above which we consider the route degenerate
	// (out-and-back instead of a loop).
	maxOverlapRatio = 0.08
)

type Client struct {
	apiKey string
	mu     sync.Mutex
	factor float64 // learned scale from straight-line polygon perimeter → road distance
}

func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, factor: 1.35}
}

type RouteRequest struct {
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Distance  int     `json:"distance"` // metres
	Surface   string  `json:"surface"`  // "road" or "trail"
	Hills     string  `json:"hills"`    // "any" or "flat"
	Seed      int     `json:"seed"`
	AllowLaps bool    `json:"allowLaps"`
}

type body struct {
	Coordinates  [][]float64 `json:"coordinates"`
	Elevation    bool        `json:"elevation"`
	Options      *options    `json:"options,omitempty"`
	Instructions bool        `json:"instructions"`
}

type options struct {
	ProfileParams *profileParams `json:"profile_params,omitempty"`
	AvoidFeatures []string       `json:"avoid_features,omitempty"`
}

type profileParams struct {
	Weightings *weightings `json:"weightings,omitempty"`
}

// ORS foot profiles only accept `green` and `quiet` as weightings. Both run 0–1.
// `steepness_difficulty` is a cycling-only param and is silently ignored here.
type weightings struct {
	Green float64 `json:"green,omitempty"`
	Quiet float64 `json:"quiet,omitempty"`
}

type featureCollection struct {
	Type     string `json:"type"`
	Features []struct {
		Type     string `json:"type"`
		Geometry struct {
			Type        string      `json:"type"`
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Summary struct {
				Distance float64 `json:"distance"`
				Duration float64 `json:"duration"`
			} `json:"summary"`
		} `json:"properties"`
	} `json:"features"`
}

func (c *Client) GenerateRoute(req RouteRequest) (json.RawMessage, error) {
	// foot-walking biases toward pavements/sidewalks; foot-hiking uses paths,
	// tracks and trails more readily. Surface alone drives this.
	profile := "foot-walking"
	if req.Surface == "trail" {
		profile = "foot-hiking"
	}
	endpoint := fmt.Sprintf("%s/%s/geojson", baseURL, profile)

	if req.AllowLaps {
		if data, ok := c.tryLapRoute(endpoint, req); ok {
			return data, nil
		}
		// fall through if no suitable feature or Overpass fails
	}

	return c.polygonRoute(endpoint, req)
}

// polygonRoute picks N waypoints on a polygon around the start, routes through
// them, and detects out-and-back overlap. On poor results it rotates / rescales
// and retries up to 2 more times.
func (c *Client) polygonRoute(endpoint string, req RouteRequest) (json.RawMessage, error) {
	c.mu.Lock()
	factor := c.factor
	c.mu.Unlock()

	target := float64(req.Distance)
	rng := rand.New(rand.NewPCG(uint64(req.Seed), uint64(req.Seed+1)))

	// More waypoints = shorter legs = less room for ORS to detour down dead-ends.
	n := 8
	if target > 8000 {
		n = 10
	}
	if target > 15000 {
		n = 12
	}

	var best json.RawMessage
	var bestScore = math.Inf(1)

	radius := target / (2 * math.Pi) / factor // straight-line radius estimate
	baseAngle := rng.Float64() * 2 * math.Pi

	const attempts = 4
	for attempt := range attempts {
		angleJitter := rng.Float64()*0.3 + 0.1 // 0.1–0.4 rad per point
		coords := polygonWaypoints(req.Lat, req.Lng, radius, n, baseAngle, angleJitter, rng)

		b := body{Coordinates: coords, Elevation: true, Instructions: false, Options: buildOptions(req)}

		data, err := c.fetch(endpoint, b)
		if err != nil {
			if attempt == 0 {
				return nil, err
			}
			break
		}

		// Clip obvious dead-end spurs from the geometry before scoring.
		cleaned, _ := clipSpurs(data)
		got, overlap, ascent := analyse(cleaned)
		if got == 0 {
			break
		}

		// Update learned factor (perimeter→road ratio).
		ratio := got / target
		c.mu.Lock()
		c.factor = c.factor*0.7 + (c.factor*ratio)*0.3
		c.mu.Unlock()

		distErr := math.Abs(got-target) / target
		score := distErr + overlap*2.0
		if req.Hills == "flat" {
			// Normalise by distance: ~10m ascent per km is flat, ~30m/km is hilly.
			// Score penalty saturates around 0.5 for very hilly routes.
			score += math.Min(0.6, (ascent/(got/1000))/40)
		}
		if score < bestScore {
			bestScore = score
			best = cleaned
		}

		// "Good enough" early-exit. Flatter mode keeps trying so we get a true best.
		earlyExit := distErr <= 0.12 && overlap <= maxOverlapRatio
		if req.Hills == "flat" {
			earlyExit = false
		}
		if earlyExit {
			return cleaned, nil
		}

		// Adjust for next attempt: rescale and meaningfully rotate.
		if got > 0 {
			radius *= target / got
		}
		// Rotate the polygon by an irrational fraction of a wedge so we don't
		// revisit the same waypoint positions across attempts.
		baseAngle += 2 * math.Pi / float64(n) * 0.37
	}

	if best == nil {
		return nil, fmt.Errorf("no route produced")
	}
	return best, nil
}

// polygonWaypoints builds a closed polygon ring around (lat,lng).
// The first and last waypoint is the start; intermediate points are evenly
// spaced around a circle with small angular jitter.
func polygonWaypoints(lat, lng, radius float64, n int, baseAngle, jitter float64, rng *rand.Rand) [][]float64 {
	coords := make([][]float64, 0, n+2)
	coords = append(coords, []float64{lng, lat})

	mPerDegLat := 111320.0
	mPerDegLng := 111320.0 * math.Cos(lat*math.Pi/180)

	for i := range n {
		a := baseAngle + float64(i)*2*math.Pi/float64(n) + (rng.Float64()*2-1)*jitter
		r := radius * (0.85 + rng.Float64()*0.3) // ±15% radius wobble
		dLat := r * math.Sin(a) / mPerDegLat
		dLng := r * math.Cos(a) / mPerDegLng
		coords = append(coords, []float64{lng + dLng, lat + dLat})
	}

	coords = append(coords, []float64{lng, lat})
	return coords
}

// tryLapRoute finds a nearby park-like loop and routes around it N times.
// Returns (data, true) on success or (nil, false) if no suitable feature was
// found or any step failed.
func (c *Client) tryLapRoute(endpoint string, req RouteRequest) (json.RawMessage, bool) {
	target := float64(req.Distance)
	// Look within half the target distance — beyond that the approach swamps the lap.
	searchRadius := int(target / 2)
	if searchRadius < 800 {
		searchRadius = 800
	}

	loops, err := overpass.FindLoops(req.Lat, req.Lng, searchRadius, 350, target*1.2, target)
	if err != nil {
		log.Printf("laps: overpass error: %v", err)
		return nil, false
	}
	if len(loops) == 0 {
		log.Printf("laps: no suitable park within %dm of (%f,%f)", searchRadius, req.Lat, req.Lng)
		return nil, false
	}

	loop := loops[0]
	laps := int(math.Round(target / loop.Perimeter))
	if laps < 1 {
		laps = 1
	}
	if laps > 8 {
		laps = 8
	}
	log.Printf("laps: park centroid=(%f,%f) perim=%.0fm laps=%d", loop.CentroidLat, loop.CentroidLng, loop.Perimeter, laps)

	// Sample perimeter so each waypoint is roughly 200m apart, capped.
	sampled := samplePerimeter(loop.Coords, 200)
	if len(sampled) < 3 {
		return nil, false
	}

	// Pull each sample point ~40m inward toward the park centroid so ORS snaps
	// them to interior park paths rather than the streets bordering the park.
	sampled = nudgeInward(sampled, loop.CentroidLat, loop.CentroidLng, 40)

	// Cap waypoints — ORS free tier allows up to 50 coordinates per request.
	maxPerLap := (48 - 2) / laps
	if maxPerLap < 3 {
		maxPerLap = 3
	}
	if len(sampled) > maxPerLap {
		sampled = downsample(sampled, maxPerLap)
	}

	coords := [][]float64{{req.Lng, req.Lat}}
	for lap := range laps {
		// Rotate starting index per lap so consecutive identical points
		// don't collapse, and the route flows continuously.
		offset := (lap * len(sampled) / max(1, laps)) % len(sampled)
		for i := range sampled {
			p := sampled[(offset+i)%len(sampled)]
			coords = append(coords, []float64{p[1], p[0]}) // [lng,lat]
		}
	}
	coords = append(coords, []float64{req.Lng, req.Lat})

	b := body{Coordinates: coords, Elevation: true, Instructions: false, Options: buildOptions(req)}

	data, err := c.fetch(endpoint, b)
	if err != nil {
		log.Printf("laps: ORS error with %d waypoints: %v", len(coords), err)
		return nil, false
	}
	cleaned, dist := clipSpurs(data)
	log.Printf("laps: success, distance=%.0fm", dist)
	return cleaned, true
}

// samplePerimeter walks the closed ring and emits points at ~stepM intervals.
func samplePerimeter(ring [][2]float64, stepM float64) [][2]float64 {
	if len(ring) < 2 {
		return ring
	}
	var out [][2]float64
	out = append(out, ring[0])
	carry := 0.0
	for i := 0; i < len(ring)-1; i++ {
		a, b := ring[i], ring[i+1]
		segLen := haversine(a[0], a[1], b[0], b[1])
		if segLen == 0 {
			continue
		}
		dist := stepM - carry
		for dist < segLen {
			t := dist / segLen
			out = append(out, [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t})
			dist += stepM
		}
		carry = segLen - (dist - stepM)
	}
	return out
}

// nudgeInward moves each point ~offsetM metres along the vector from the
// point toward (cLat, cLng). Used so park-perimeter samples snap to interior
// paths rather than the streets bordering the park.
func nudgeInward(pts [][2]float64, cLat, cLng, offsetM float64) [][2]float64 {
	out := make([][2]float64, len(pts))
	const mPerDegLat = 111320.0
	for i, p := range pts {
		dLatM := (cLat - p[0]) * mPerDegLat
		dLngM := (cLng - p[1]) * 111320.0 * math.Cos(p[0]*math.Pi/180)
		d := math.Hypot(dLatM, dLngM)
		if d < offsetM*1.5 {
			// Point is already close to the centroid; leave as-is.
			out[i] = p
			continue
		}
		out[i] = [2]float64{
			p[0] + (dLatM/d)*offsetM/mPerDegLat,
			p[1] + (dLngM/d)*offsetM/(111320.0*math.Cos(p[0]*math.Pi/180)),
		}
	}
	return out
}

func downsample(pts [][2]float64, target int) [][2]float64 {
	if len(pts) <= target {
		return pts
	}
	out := make([][2]float64, 0, target)
	step := float64(len(pts)) / float64(target)
	for i := range target {
		out = append(out, pts[int(float64(i)*step)])
	}
	return out
}

func (c *Client) fetch(url string, b body) (json.RawMessage, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ORS %d: %s", resp.StatusCode, data)
	}
	return json.RawMessage(data), nil
}

// buildOptions translates user preferences into ORS request options.
// Surface drives the profile *and* a green-weighting (trails prefer parks/woods).
// avoid_features keeps the route off staircases — runners don't want stairs.
func buildOptions(req RouteRequest) *options {
	opts := &options{AvoidFeatures: []string{"steps"}}
	if req.Surface == "trail" {
		opts.ProfileParams = &profileParams{Weightings: &weightings{Green: 1.0}}
	}
	return opts
}

// analyse returns the route's total distance, the fraction of its length that
// retraces edges already travelled (out-and-back ratio), and total positive
// elevation gain in metres (computed from the geometry's z-coordinates).
func analyse(data json.RawMessage) (distance, overlap, ascent float64) {
	var fc featureCollection
	if err := json.Unmarshal(data, &fc); err != nil || len(fc.Features) == 0 {
		return 0, 0, 0
	}
	f := fc.Features[0]
	distance = f.Properties.Summary.Distance

	coords := f.Geometry.Coordinates
	if len(coords) < 2 {
		return distance, 0, 0
	}

	// Snap segments to a ~12m grid, keyed undirected so out-and-back
	// over the same edge collides.
	const cell = 1e-4 // ~11m at the equator
	edges := map[[4]int64]bool{}
	var total, dup float64
	for i := 0; i < len(coords)-1; i++ {
		a, b := coords[i], coords[i+1]
		ax, ay := int64(a[0]/cell), int64(a[1]/cell)
		bx, by := int64(b[0]/cell), int64(b[1]/cell)
		if ax == bx && ay == by {
			continue
		}
		var key [4]int64
		if ax < bx || (ax == bx && ay < by) {
			key = [4]int64{ax, ay, bx, by}
		} else {
			key = [4]int64{bx, by, ax, ay}
		}
		l := haversine(a[1], a[0], b[1], b[0])
		total += l
		if edges[key] {
			dup += l
		}
		edges[key] = true

		if len(a) >= 3 && len(b) >= 3 {
			// 1m per-step threshold filters DEM noise; matches the client display.
			if d := b[2] - a[2]; d > 1 {
				ascent += d
			}
		}
	}
	if total == 0 {
		return distance, 0, ascent
	}
	return distance, dup / total, ascent
}

// clipSpurs removes out-and-back dead-end spurs from the returned geometry.
// It iteratively finds the shortest A → … → A palindrome (the path retraces
// itself exactly) in the grid-snapped coordinate sequence and removes it,
// then rebuilds the GeoJSON with a corrected summary distance.
func clipSpurs(data json.RawMessage) (json.RawMessage, float64) {
	var fc featureCollection
	if err := json.Unmarshal(data, &fc); err != nil || len(fc.Features) == 0 {
		return data, 0
	}
	coords := fc.Features[0].Geometry.Coordinates
	if len(coords) < 3 {
		return data, fc.Features[0].Properties.Summary.Distance
	}

	const cell = 1e-4 // ~11m
	snap := func(p []float64) [2]int64 { return [2]int64{int64(p[0] / cell), int64(p[1] / cell)} }

	for {
		n := len(coords)
		keys := make([][2]int64, n)
		for i, p := range coords {
			keys[i] = snap(p)
		}

		bestI, bestJ, bestLen := -1, -1, math.MaxInt
		for i := 0; i < n-2; i++ {
			for j := i + 2; j < n; j++ {
				if keys[i] != keys[j] {
					continue
				}
				if !isPalindrome(keys[i : j+1]) {
					continue
				}
				if j-i < bestLen {
					bestI, bestJ, bestLen = i, j, j-i
				}
				break
			}
		}
		if bestI < 0 {
			break
		}
		// Keep coords[bestI], drop coords[bestI+1 .. bestJ].
		coords = append(coords[:bestI+1], coords[bestJ+1:]...)
	}

	// Recompute total distance.
	var total float64
	for i := 0; i < len(coords)-1; i++ {
		total += haversine(coords[i][1], coords[i][0], coords[i+1][1], coords[i+1][0])
	}

	// Patch the GeoJSON back in place.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return data, total
	}
	features, _ := raw["features"].([]any)
	if len(features) == 0 {
		return data, total
	}
	feat, _ := features[0].(map[string]any)
	geom, _ := feat["geometry"].(map[string]any)
	geom["coordinates"] = coords
	if props, ok := feat["properties"].(map[string]any); ok {
		if summary, ok := props["summary"].(map[string]any); ok {
			summary["distance"] = total
		}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return data, total
	}
	return out, total
}

func isPalindrome(k [][2]int64) bool {
	for i, j := 0, len(k)-1; i < j; i, j = i+1, j-1 {
		if k[i] != k[j] {
			return false
		}
	}
	return true
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

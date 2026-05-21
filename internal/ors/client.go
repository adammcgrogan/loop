package ors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

const baseURL = "https://api.openrouteservice.org/v2/directions"

type Client struct {
	apiKey string
	mu     sync.Mutex
	factor float64 // learned correction for ORS overshoot
}

func NewClient(apiKey string) *Client {
	return &Client{apiKey: apiKey, factor: 0.65}
}

type RouteRequest struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Distance int     `json:"distance"` // metres
	Surface  string  `json:"surface"`  // "road" or "trail"
	Hills    string  `json:"hills"`    // "any" or "flat"
	Seed     int     `json:"seed"`
}

type body struct {
	Coordinates [][]float64 `json:"coordinates"`
	Options     options     `json:"options"`
	Elevation   bool        `json:"elevation"`
}

type options struct {
	RoundTrip     roundTrip      `json:"round_trip"`
	ProfileParams *profileParams `json:"profile_params,omitempty"`
}

type roundTrip struct {
	Length int `json:"length"`
	Points int `json:"points"`
	Seed   int `json:"seed"`
}

type profileParams struct {
	Weightings map[string]int `json:"weightings"`
}

type summary struct {
	Features []struct {
		Properties struct {
			Summary struct {
				Distance float64 `json:"distance"`
			} `json:"summary"`
		} `json:"properties"`
	} `json:"features"`
}

func (c *Client) GenerateRoute(req RouteRequest) (json.RawMessage, error) {
	profile := "foot-walking"
	if req.Surface == "trail" || req.Hills == "flat" {
		profile = "foot-hiking"
	}

	b := body{
		Coordinates: [][]float64{{req.Lng, req.Lat}},
		Elevation:   true,
		Options: options{
			RoundTrip: roundTrip{
				Points: 3,
				Seed:   req.Seed,
			},
		},
	}

	if req.Hills == "flat" {
		b.Options.ProfileParams = &profileParams{
			Weightings: map[string]int{"steepness_difficulty": 3},
		}
	}

	c.mu.Lock()
	factor := c.factor
	c.mu.Unlock()

	target := float64(req.Distance)
	b.Options.RoundTrip.Length = int(target * factor)

	endpoint := fmt.Sprintf("%s/%s/geojson", baseURL, profile)

	// One attempt with the learned factor, one correction if needed.
	var last json.RawMessage
	for range 2 {
		data, err := c.fetch(endpoint, b)
		if err != nil {
			return nil, err
		}
		last = data

		returned, err := parseDistance(data)
		if err != nil || returned == 0 {
			break
		}

		ratio := target / returned

		c.mu.Lock()
		c.factor = c.factor*0.7 + ratio*0.3
		c.mu.Unlock()

		if ratio >= 0.85 && ratio <= 1.15 {
			break
		}

		b.Options.RoundTrip.Length = int(float64(b.Options.RoundTrip.Length) * ratio)
	}

	return last, nil
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

func parseDistance(data json.RawMessage) (float64, error) {
	var s summary
	if err := json.Unmarshal(data, &s); err != nil {
		return 0, err
	}
	if len(s.Features) == 0 {
		return 0, nil
	}
	return s.Features[0].Properties.Summary.Distance, nil
}

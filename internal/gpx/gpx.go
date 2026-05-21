package gpx

import (
	"fmt"
	"strings"
)

type Request struct {
	Coordinates [][]float64 `json:"coordinates"` // [lng, lat, ele] from ORS GeoJSON
	Name        string      `json:"name"`
}

func Build(req Request) string {
	if req.Name == "" {
		req.Name = "Circuit Route"
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<gpx xmlns="http://www.topografix.com/GPX/1/1" version="1.1" creator="loop">` + "\n")
	b.WriteString("  <trk>\n")
	fmt.Fprintf(&b, "    <name>%s</name>\n", req.Name)
	b.WriteString("    <trkseg>\n")

	for _, coord := range req.Coordinates {
		if len(coord) < 2 {
			continue
		}
		lng, lat := coord[0], coord[1]
		if len(coord) >= 3 {
			fmt.Fprintf(&b, "      <trkpt lat=\"%f\" lon=\"%f\"><ele>%f</ele></trkpt>\n", lat, lng, coord[2])
		} else {
			fmt.Fprintf(&b, "      <trkpt lat=\"%f\" lon=\"%f\"></trkpt>\n", lat, lng)
		}
	}

	b.WriteString("    </trkseg>\n")
	b.WriteString("  </trk>\n")
	b.WriteString("</gpx>\n")

	return b.String()
}

// Package geocode is Beamfall's fully-offline reverse geocoder: GPS
// latitude/longitude → a coarse place name, with zero network calls. An
// external geocoding API is deliberately not used — it would send a user's
// photo locations off the server.
//
// DEPENDENCY-LIGHT STAND-IN. The bundled dataset is a small table of major
// world cities (see cities.go) resolved by great-circle nearest-neighbor. It
// returns city/country-granularity names — enough to label a photo "near
// Paris, France" or "near Tokyo, Japan", not street-level precision. It is a
// coarse, zero-configuration default behind a stable seam (the Geocoder
// interface), swappable for a finer dataset (Natural Earth / GeoNames cities-1000) without touching
// any caller. The Reverse signature and the "near <place>" contract are the
// seam; the table is the swappable part.
package geocode

import (
	"errors"
	"math"
)

// The Geocoder seam is declared at the consumer (see local.go),
// not here. Reverse's contract: returns ErrNoPlace when no reference point
// is close enough to name (e.g. mid-ocean), and ErrOutOfRange for
// coordinates outside the valid lat/lon domain.

// ErrNoPlace is returned when no bundled reference point is near enough to the
// query coordinate to produce a meaningful name. Callers treat it like an
// unresolved scraper field: the place is simply omitted, not a failure.
var ErrNoPlace = errors.New("geocode: no nearby place")

// ErrOutOfRange is returned for coordinates outside [-90,90] lat / [-180,180]
// lon — a malformed or absent GPS value the caller should not have passed.
var ErrOutOfRange = errors.New("geocode: coordinate out of range")

// maxMatchKm caps how far a query may sit from its nearest reference point and
// still be named. Beyond it (open ocean, polar regions with no bundled city)
// Reverse returns ErrNoPlace rather than a misleadingly distant city. The
// coarse table is sparse, so this is generous — it labels "the nearest major
// city" within a continental scale, matching the "near <place>" wording.
const maxMatchKm = 500.0

// reference is one bundled (lat, lon, place) anchor.
type reference struct {
	lat, lon float64
	place    string // "City, Country"
}

// Table is the bundled-dataset Geocoder: nearest-neighbor over a fixed slice of
// reference points. It holds no state and is safe for concurrent use.
type Table struct {
	points []reference
}

// NewTable builds the default bundled coarse geocoder.
func NewTable() *Table {
	return &Table{points: bundledCities}
}

// Reverse implements Geocoder by linear nearest-neighbor scan over the bundled
// points. The table is small (tens of points), so a linear scan is simpler and
// faster than any spatial index and avoids a dependency — when the table is
// swapped for a large dataset, the index moves behind this same method.
func (t *Table) Reverse(lat, lon float64) (string, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return "", ErrOutOfRange
	}
	if len(t.points) == 0 {
		return "", ErrNoPlace
	}
	best := math.MaxFloat64
	bestPlace := ""
	for _, p := range t.points {
		d := haversineKm(lat, lon, p.lat, p.lon)
		if d < best {
			best = d
			bestPlace = p.place
		}
	}
	if best > maxMatchKm {
		return "", ErrNoPlace
	}
	return "near " + bestPlace, nil
}

// haversineKm is the great-circle distance between two coordinates in
// kilometers — the correct nearest-neighbor metric on a sphere (a naive
// lat/lon Euclidean distance mis-ranks points away from the equator).
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

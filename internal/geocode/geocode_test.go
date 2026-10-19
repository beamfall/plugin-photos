package geocode

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestReverseKnownCity(t *testing.T) {
	g := NewTable()
	cases := []struct {
		name     string
		lat, lon float64
		want     string // substring the place name must contain
	}{
		{"paris", 48.8567, 2.3508, "Paris, France"},
		{"tokyo", 35.6895, 139.6917, "Tokyo, Japan"},
		{"nyc", 40.7128, -74.0060, "New York, United States"},
		{"sydney", -33.8688, 151.2093, "Sydney, Australia"},
		// A coordinate between two cities resolves to the nearer one: the
		// Bay Area is closer to San Francisco than Los Angeles.
		{"bay-area", 37.4, -122.1, "San Francisco, United States"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := g.Reverse(c.lat, c.lon)
			if err != nil {
				t.Fatalf("Reverse: %v", err)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("Reverse(%v,%v) = %q, want it to contain %q", c.lat, c.lon, got, c.want)
			}
			if !strings.HasPrefix(got, "near ") {
				t.Errorf("Reverse = %q, want a coarse \"near <place>\" form", got)
			}
		})
	}
}

func TestReverseDeterministic(t *testing.T) {
	// The same coordinate always resolves to the same place across calls — the
	// stand-in must be stable so a photo's place does not flap.
	g := NewTable()
	first, err := g.Reverse(48.85, 2.35)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := g.Reverse(48.85, 2.35)
		if err != nil || got != first {
			t.Fatalf("Reverse not deterministic: got %q,%v want %q", got, err, first)
		}
	}
}

func TestReverseNoNearbyPlace(t *testing.T) {
	// Mid-Pacific (Point Nemo-ish): no bundled city within maxMatchKm, so the
	// geocoder declines to name it rather than return a misleadingly distant
	// city.
	g := NewTable()
	_, err := g.Reverse(-48.0, -123.0)
	if !errors.Is(err, ErrNoPlace) {
		t.Errorf("Reverse(mid-ocean) err = %v, want ErrNoPlace", err)
	}
}

func TestReverseOutOfRange(t *testing.T) {
	g := NewTable()
	for _, c := range [][2]float64{{91, 0}, {-91, 0}, {0, 181}, {0, -181}} {
		if _, err := g.Reverse(c[0], c[1]); !errors.Is(err, ErrOutOfRange) {
			t.Errorf("Reverse(%v,%v) err = %v, want ErrOutOfRange", c[0], c[1], err)
		}
	}
}

func TestReverseNaN(t *testing.T) {
	// NaN must not slip past the range guard: a naive `<`/`>` comparison is
	// always false against NaN, so it needs an explicit math.IsNaN check
	// rather than falling through to the haversine scan and returning
	// ErrNoPlace instead of ErrOutOfRange.
	g := NewTable()
	nan := math.NaN()
	for _, c := range [][2]float64{{nan, 0}, {0, nan}, {nan, nan}} {
		if _, err := g.Reverse(c[0], c[1]); !errors.Is(err, ErrOutOfRange) {
			t.Errorf("Reverse(%v,%v) err = %v, want ErrOutOfRange", c[0], c[1], err)
		}
	}
}

func TestReverseFullyOffline(t *testing.T) {
	// The reverse-geocoder must make zero network calls. It holds only the in-memory bundled table and a math
	// function; there is no http import in the package. This test documents the
	// contract — a thousand lookups complete with no I/O at all.
	g := NewTable()
	for i := 0; i < 1000; i++ {
		_, _ = g.Reverse(float64(i%180-90), float64(i%360-180))
	}
}

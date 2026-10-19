package photos

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/beamfall/plugin-photos/internal/exif"
	"github.com/beamfall/plugin-photos/internal/geocode"
)

const imageMediaClass = "image"

var ErrNotImageClass = errors.New("photos: exif hook is gated to image-class Media")

type LocalMetadata struct {
	Fields             map[string]any
	ImpliedMediaFields map[string]string
	Entities           []LocalEntity
	// CaptureTimeFromMtime flags that capture_date was filled from the file's
	// modification time because EXIF DateTimeOriginal was missing or unparseable
	// The photo is still browsable and chronologically sortable,
	// never silently dropped; the flag lets the client mark the time as inferred
	// rather than camera-recorded. Not emitted as a Fields key — core's local
	// EXIF field contract is fixed (conformance), so the signal rides the struct.
	CaptureTimeFromMtime bool
}

type LocalEntity struct {
	Kind        string
	Title       string
	Relation    string
	Cardinality string
	SortOrder   *int
}

type Geocoder interface {
	Reverse(lat, lon float64) (string, error)
}

type LocalExtractor struct {
	geocoder Geocoder
}

func New() *LocalExtractor {
	return &LocalExtractor{geocoder: geocode.NewTable()}
}

func newWithGeocoder(g Geocoder) *LocalExtractor {
	return &LocalExtractor{geocoder: g}
}

func (e *LocalExtractor) ExtractLocal(ctx context.Context, class, root, path string) (LocalMetadata, error) {
	if class != imageMediaClass {
		return LocalMetadata{}, fmt.Errorf("%w: got class %q", ErrNotImageClass, class)
	}
	if err := ctx.Err(); err != nil {
		return LocalMetadata{}, err
	}
	md, err := exif.ReadFileContext(ctx, path)
	if err != nil {
		return LocalMetadata{}, fmt.Errorf("photos: read exif: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return LocalMetadata{}, err
	}
	place := e.resolvePlace(md)
	captureTime, fromMtime := resolveCaptureTime(md.CaptureTime, path)
	implied := map[string]string{"media.class": "photo"}
	if !captureTime.IsZero() {
		implied["media.capture-time"] = captureTime.UTC().Format(time.RFC3339)
	}
	if album := albumTitleForPath(root, path); album != "" {
		// Mirror the folder-derived Album entity into Core's Media
		// search document; switch to relation-backed indexing if manual Album
		// renames must propagate without a rescan.
		implied["media.album"] = album
	}
	return LocalMetadata{
		Fields:               e.fieldsFromEXIF(md, place, captureTime),
		ImpliedMediaFields:   implied,
		Entities:             e.entitiesFromEXIF(root, path, place, captureTime),
		CaptureTimeFromMtime: fromMtime,
	}, nil
}

// resolveCaptureTime returns the timestamp used for chronological sorting: the
// EXIF capture time when present, otherwise the file's modification time as a
// flagged fallback so a photo with missing/unparseable EXIF is
// still browsable rather than dropped. A stat failure leaves the time zero — the
// caller simply omits capture_date, exactly the pre-fallback behavior.
func resolveCaptureTime(exifCapture time.Time, path string) (time.Time, bool) {
	if !exifCapture.IsZero() {
		return exifCapture, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// resolvePlace reverse-geocodes the image's GPS coordinates once, so callers
// building fields and entities from the same EXIF metadata don't each pay
// for (and risk divergent results from) their own geocoder.Reverse lookup.
func (e *LocalExtractor) resolvePlace(md exif.Metadata) string {
	if !md.HasGPS || e.geocoder == nil {
		return ""
	}
	place, err := e.geocoder.Reverse(md.Latitude, md.Longitude)
	if err != nil {
		return ""
	}
	return place
}

func (e *LocalExtractor) fieldsFromEXIF(md exif.Metadata, place string, captureTime time.Time) map[string]any {
	fields := map[string]any{}
	if !captureTime.IsZero() {
		// RFC3339 keeps the capture-site zone offset in the emitted value
		// (normalized instant, offset retained for display) —
		// the instant is UTC-correct via the offset, unlike a forced .UTC().
		fields["capture_date"] = captureTime.Format(time.RFC3339)
	}
	if md.CameraMake != "" {
		fields["camera_make"] = md.CameraMake
	}
	if md.CameraModel != "" {
		fields["camera_model"] = md.CameraModel
	}
	if md.Width > 0 {
		fields["width"] = md.Width
	}
	if md.Height > 0 {
		fields["height"] = md.Height
	}
	if md.Orientation > 0 {
		fields["orientation"] = md.Orientation
	}
	if place != "" {
		fields["place"] = place
	}
	return fields
}

func (e *LocalExtractor) entitiesFromEXIF(root, path, place string, captureTime time.Time) []LocalEntity {
	var entities []LocalEntity
	if album := albumTitleForPath(root, path); album != "" {
		var sortOrder *int
		if !captureTime.IsZero() {
			// Unix() is offset-independent (absolute instant), so cross-timezone
			// albums sort by true capture moment.
			order := int(captureTime.Unix())
			sortOrder = &order
		}
		entities = append(entities, LocalEntity{Kind: "album", Title: album, Relation: "contains", SortOrder: sortOrder})
	}
	if place != "" {
		entities = append(entities, LocalEntity{Kind: "location", Title: place, Relation: "located-in", Cardinality: "one"})
	}
	return entities
}

func albumTitleForPath(root, path string) string {
	cleanPath := filepath.Clean(path)
	parentDir := filepath.Dir(cleanPath)
	parent := filepath.Base(parentDir)
	if parent == "." || parent == string(filepath.Separator) || parent == "" {
		return ""
	}
	if root != "" && parentDir == filepath.Clean(root) {
		return ""
	}
	return parent
}

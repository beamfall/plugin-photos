package photos

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeJPEGFixture(t *testing.T) string {
	t.Helper()
	tiff := parisTIFF()
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	length := len(app1) + 2
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte(length >> 8), byte(length)}
	jpeg = append(jpeg, app1...)
	jpeg = append(jpeg, 0xFF, 0xD9)

	path := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(path, jpeg, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func parisTIFF() []byte {
	le := binary.LittleEndian
	u16 := func(v uint16) []byte { b := make([]byte, 2); le.PutUint16(b, v); return b }
	u32 := func(v uint32) []byte { b := make([]byte, 4); le.PutUint32(b, v); return b }
	u16padded := func(v uint16) []byte { return append(u16(v), 0, 0) }
	rat := func(n uint32) []byte { return append(u32(n), u32(1)...) }

	const gpsIFDOff = 98
	var b []byte
	b = append(b, 'I', 'I')
	b = append(b, u16(0x002A)...)
	b = append(b, u32(8)...)

	b = append(b, u16(7)...)
	b = append(b, u16(0x010F)...)
	b = append(b, u16(2)...)
	b = append(b, u32(6)...)
	b = append(b, u32(0)...)
	b = append(b, u16(0x0110)...)
	b = append(b, u16(2)...)
	b = append(b, u32(7)...)
	b = append(b, u32(0)...)
	b = append(b, u16(0x0112)...)
	b = append(b, u16(3)...)
	b = append(b, u32(1)...)
	b = append(b, u16padded(6)...)
	b = append(b, u16(0x9003)...)
	b = append(b, u16(2)...)
	b = append(b, u32(20)...)
	b = append(b, u32(0)...)
	b = append(b, u16(0xA002)...)
	b = append(b, u16(4)...)
	b = append(b, u32(1)...)
	b = append(b, u32(4032)...)
	b = append(b, u16(0xA003)...)
	b = append(b, u16(4)...)
	b = append(b, u32(1)...)
	b = append(b, u32(3024)...)
	b = append(b, u16(0x8825)...)
	b = append(b, u16(4)...)
	b = append(b, u32(1)...)
	b = append(b, u32(gpsIFDOff)...)
	b = append(b, u32(0)...)

	gps := []byte{}
	gps = append(gps, u16(4)...)
	gps = append(gps, u16(0x0001)...)
	gps = append(gps, u16(2)...)
	gps = append(gps, u32(2)...)
	gps = append(gps, 'N', 0, 0, 0)
	gps = append(gps, u16(0x0002)...)
	gps = append(gps, u16(5)...)
	gps = append(gps, u32(3)...)
	gps = append(gps, u32(0)...)
	gps = append(gps, u16(0x0003)...)
	gps = append(gps, u16(2)...)
	gps = append(gps, u32(2)...)
	gps = append(gps, 'E', 0, 0, 0)
	gps = append(gps, u16(0x0004)...)
	gps = append(gps, u16(5)...)
	gps = append(gps, u32(3)...)
	gps = append(gps, u32(0)...)
	gps = append(gps, u32(0)...)

	dataStart := gpsIFDOff + len(gps)
	makeAt := dataStart
	modelAt := makeAt + 6
	dtAt := modelAt + 7
	latAt := dtAt + 20
	lonAt := latAt + 24

	le.PutUint32(b[18:22], uint32(makeAt))
	le.PutUint32(b[30:34], uint32(modelAt))
	le.PutUint32(b[54:58], uint32(dtAt))
	le.PutUint32(gps[22:26], uint32(latAt))
	le.PutUint32(gps[46:50], uint32(lonAt))

	out := append(b, gps...)
	out = append(out, []byte("Canon\x00")...)
	out = append(out, []byte("EOS R5\x00")...)
	out = append(out, []byte("2021:07:14 09:30:15\x00")...)
	out = append(out, rat(48)...)
	out = append(out, rat(51)...)
	out = append(out, rat(24)...)
	out = append(out, rat(2)...)
	out = append(out, rat(21)...)
	out = append(out, rat(3)...)
	return out
}

func TestExtractLocalEXIF(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Paris Trip", "photo.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := writeJPEGFixture(t)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().ExtractLocal(context.Background(), imageMediaClass, root, path)
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}
	if got := result.ImpliedMediaFields["media.class"]; got != "photo" {
		t.Errorf("class = %v, want photo", got)
	}
	if got := result.ImpliedMediaFields["media.album"]; got != "Paris Trip" {
		t.Errorf("album = %v, want Paris Trip", got)
	}
	if got := result.ImpliedMediaFields["media.capture-time"]; got != "2021-07-14T09:30:15Z" {
		t.Errorf("capture-time = %v, want 2021-07-14T09:30:15Z", got)
	}
	if got := result.Fields["capture_date"]; got != "2021-07-14T09:30:15Z" {
		t.Errorf("capture_date = %v, want 2021-07-14T09:30:15Z", got)
	}
	if got := result.Fields["camera_make"]; got != "Canon" {
		t.Errorf("camera_make = %v, want Canon", got)
	}
	if got := result.Fields["camera_model"]; got != "EOS R5" {
		t.Errorf("camera_model = %v, want EOS R5", got)
	}
	if got := result.Fields["width"]; got != 4032 {
		t.Errorf("width = %v, want 4032", got)
	}
	if got := result.Fields["height"]; got != 3024 {
		t.Errorf("height = %v, want 3024", got)
	}
	if got := result.Fields["orientation"]; got != 6 {
		t.Errorf("orientation = %v, want 6", got)
	}
	if got := result.Fields["place"]; got != "near Paris, France" {
		t.Errorf("place = %v, want near Paris, France", got)
	}
	if len(result.Entities) != 2 {
		t.Fatalf("entities = %#v, want album and location", result.Entities)
	}
	if result.Entities[0].Kind != "album" || result.Entities[0].Title != "Paris Trip" || result.Entities[0].Relation != "contains" || result.Entities[0].SortOrder == nil {
		t.Errorf("album entity = %#v", result.Entities[0])
	}
	if result.Entities[1].Kind != "location" || result.Entities[1].Title != "near Paris, France" || result.Entities[1].Relation != "located-in" || result.Entities[1].Cardinality != "one" {
		t.Errorf("location entity = %#v", result.Entities[1])
	}
}

// TestExtractLocalRAWCarriesOrientation drives a TIFF-container RAW (DNG)
// through the extraction hook: its orientation and camera capture time must
// reach core's fixed field contract, since core's derivative pipeline rotates
// the RAW's embedded preview by the stored orientation field.
func TestExtractLocalRAWCarriesOrientation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Paris Trip", "photo.dng")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, parisTIFF(), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().ExtractLocal(context.Background(), imageMediaClass, root, path)
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}
	if got := result.Fields["orientation"]; got != 6 {
		t.Errorf("orientation = %v, want 6", got)
	}
	if got := result.Fields["capture_date"]; got != "2021-07-14T09:30:15Z" {
		t.Errorf("capture_date = %v, want 2021-07-14T09:30:15Z", got)
	}
	if result.CaptureTimeFromMtime {
		t.Error("CaptureTimeFromMtime = true, want the RAW's EXIF capture time")
	}
	if got := result.Fields["camera_make"]; got != "Canon" {
		t.Errorf("camera_make = %v, want Canon", got)
	}
	if got := result.Fields["camera_model"]; got != "EOS R5" {
		t.Errorf("camera_model = %v, want EOS R5", got)
	}
}

func TestExtractLocalNoGeocoder(t *testing.T) {
	result, err := newWithGeocoder(nil).ExtractLocal(context.Background(), imageMediaClass, "", writeJPEGFixture(t))
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}
	if _, ok := result.Fields["place"]; ok {
		t.Error("place present without a geocoder, want omitted")
	}
}

func TestExtractLocalNoEXIFDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	bare := filepath.Join(dir, "bare.jpg")
	if err := os.WriteFile(bare, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o600); err != nil {
		t.Fatal(err)
	}
	notImage := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notImage, []byte("hello, not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No EXIF no longer means "no fields": capture_date is filled from mtime and
	// flagged so the item stays browsable. Every
	// EXIF-derived field beyond capture_date is still absent.
	for _, path := range []string{bare, notImage} {
		result, err := New().ExtractLocal(context.Background(), imageMediaClass, dir, path)
		if err != nil {
			t.Errorf("%s: err = %v, want nil", path, err)
		}
		if !result.CaptureTimeFromMtime {
			t.Errorf("%s: CaptureTimeFromMtime = false, want true", path)
		}
		for key := range result.Fields {
			if key != "capture_date" {
				t.Errorf("%s: unexpected EXIF-derived field %q, want only capture_date (from mtime)", path, key)
			}
		}
	}
}

func TestExtractLocalMissingEXIFFallsBackToMtimeFlagged(t *testing.T) {
	// A JPEG with no EXIF capture time: capture_date is filled from the file's
	// mtime (RFC3339, so the photo is still chronologically browsable) and the
	// CaptureTimeFromMtime flag is set so the time reads as inferred.
	dir := t.TempDir()
	bare := filepath.Join(dir, "screenshot.jpg")
	if err := os.WriteFile(bare, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(bare, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	result, err := New().ExtractLocal(context.Background(), imageMediaClass, dir, bare)
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}
	if !result.CaptureTimeFromMtime {
		t.Error("CaptureTimeFromMtime = false, want true")
	}
	got, ok := result.Fields["capture_date"].(string)
	if !ok {
		t.Fatalf("capture_date missing/non-string: %v", result.Fields["capture_date"])
	}
	if parsed, err := time.Parse(time.RFC3339, got); err != nil || !parsed.Equal(mtime) {
		t.Errorf("capture_date = %q, want mtime %v", got, mtime.Format(time.RFC3339))
	}
}

func TestExtractLocalWithEXIFDoesNotFlagMtime(t *testing.T) {
	// The Paris fixture has a real EXIF capture time: mtime fallback must not fire.
	result, err := New().ExtractLocal(context.Background(), imageMediaClass, "", writeJPEGFixture(t))
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}
	if result.CaptureTimeFromMtime {
		t.Error("CaptureTimeFromMtime = true, want false when EXIF capture time present")
	}
}

func TestExtractLocalCorruptEXIFDegradesToMtime(t *testing.T) {
	// Corrupt metadata is "no EXIF", never an error: a JPEG whose APP1 declares
	// more body bytes than the file holds, and one whose well-formed Exif
	// segment wraps a garbage TIFF payload, both degrade to the flagged mtime
	// fallback instead of failing the extraction.
	truncated := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x70}, []byte("Exif\x00\x00")...)
	garbageTIFF := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00, 0x10}, []byte("Exif\x00\x00")...)
	garbageTIFF = append(garbageTIFF, 'X', 'X', 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	garbageTIFF = append(garbageTIFF, 0xFF, 0xD9)
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"truncated-app1.jpg": truncated,
		"garbage-tiff.jpg":   garbageTIFF,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := New().ExtractLocal(context.Background(), imageMediaClass, dir, path)
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
			continue
		}
		if !result.CaptureTimeFromMtime {
			t.Errorf("%s: CaptureTimeFromMtime = false, want true (corrupt EXIF degrades to mtime)", name)
		}
		for key := range result.Fields {
			if key != "capture_date" {
				t.Errorf("%s: unexpected EXIF-derived field %q from corrupt metadata", name, key)
			}
		}
	}
}

func TestExtractLocalGatedToImageClass(t *testing.T) {
	fixture := writeJPEGFixture(t)
	for _, class := range []string{"video", "audio", ""} {
		_, err := New().ExtractLocal(context.Background(), class, "", fixture)
		if !errors.Is(err, ErrNotImageClass) {
			t.Errorf("class %q: err = %v, want ErrNotImageClass", class, err)
		}
	}
}

func TestExtractLocalMissingFileErrors(t *testing.T) {
	_, err := New().ExtractLocal(context.Background(), imageMediaClass, "", "/no/such/file.jpg")
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ExtractLocal on missing file err = %v, want os.ErrNotExist", err)
	}
}

func TestAlbumTitleForPathCleansTrailingSlash(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Paris Trip", "photo.jpg") + string(filepath.Separator)
	if got := albumTitleForPath(root, path); got != "Paris Trip" {
		t.Fatalf("albumTitleForPath = %q, want Paris Trip", got)
	}
}

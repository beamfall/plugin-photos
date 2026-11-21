package photos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sdk "github.com/beamfall/plugin-sdk"
)

// wantBundleVersion is the release semver this head carries: the release step
// tags the head `v`+wantBundleVersion, so a bundle Version left behind by a
// tag bump fails here instead of shipping a stale identity.
const wantBundleVersion = "0.1.4"

// TestBundleVersionMatchesReleaseTag pins Bundle().Version to the semver of
// the release tag applied to this head.
func TestBundleVersionMatchesReleaseTag(t *testing.T) {
	if got := Bundle().Version; got != wantBundleVersion {
		t.Fatalf("Bundle().Version = %q, want %q", got, wantBundleVersion)
	}
}

// TestBundleCompatibleWithPinnedSDK pins the declared [SDKFloor, SDKCeiling]
// range against the SDK identity this module pins, so a bundle whose range
// excludes its own pinned SDK fails here rather than at core load time
// (sdk.CompatibleWithSDK).
func TestBundleCompatibleWithPinnedSDK(t *testing.T) {
	b := Bundle()
	if !sdk.CompatibleWithSDK(b, sdk.Version) {
		t.Fatalf("Bundle() compat range [%s, %s] excludes pinned SDK %s", b.SDKFloor, b.SDKCeiling, sdk.Version)
	}
}

// fixtureClassFor mirrors Core's scan classification of a file in a library:
// image extensions are image, any other media file is video.
func fixtureClassFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic", ".webp":
		return "image"
	default:
		return "video"
	}
}

// CORE-2977: camera-roll clips beside photos must be admitted as video. The
// scanner treats a class outside the declaration as undeclared media and
// suspends retirement for the library (CORE-2361), so both classes are needed.
func TestBundleAdmitsCameraRollVideoBesidePhotos(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"IMG_0001.jpg": "image",
		"IMG_0002.mov": "video",
		"IMG_0003.mp4": "video",
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != len(files) {
		t.Fatalf("fixture library: %v entries=%d", err, len(entries))
	}
	classes := Bundle().MediaClasses
	for _, entry := range entries {
		got := fixtureClassFor(entry.Name())
		if want := files[entry.Name()]; got != want {
			t.Fatalf("%s classified %q, want %q", entry.Name(), got, want)
		}
		if !slices.Contains(classes, got) {
			t.Errorf("%s is %s but Bundle().MediaClasses = %v excludes it", entry.Name(), got, classes)
		}
	}
}

// Video is admitted but carries no EXIF: the hook still refuses other classes
// and reads a clip's capture date from its mtime.
func TestExifHookRefusesOtherClassesAndDatesVideo(t *testing.T) {
	_, err := New().ExtractLocal(context.Background(), "audio", t.TempDir(), "clip.m4a")
	if !errors.Is(err, ErrNotImageClass) {
		t.Fatalf("ExtractLocal(audio) error = %v, want ErrNotImageClass", err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "Trip", "clip.mov")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not exif"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New().ExtractLocal(context.Background(), "video", root, path)
	if err != nil {
		t.Fatalf("ExtractLocal(video) error = %v", err)
	}
	if result.Fields["capture_date"] == nil || !result.CaptureTimeFromMtime {
		t.Fatalf("video result = %+v, want mtime capture_date", result)
	}
}

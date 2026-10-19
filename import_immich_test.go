package photos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/beamfall/plugin-sdk"
	"github.com/beamfall/plugin-sdk/sdktest"
)

var errImmichFixtureRead = errors.New("fixture read failed")

type failingImmichReader struct{}

func (failingImmichReader) Read([]byte) (int, error) { return 0, errImmichFixtureRead }

const immichFixture = `[
  {
    "albumName": "Family Weekend",
    "order": "ASC",
    "shared": true,
    "albumUsers": [{"userId": "user-1"}],
    "assets": [
      {"id": "asset-b", "originalPath": "/archive/b.jpg", "people": [{"id": "face-1"}], "isFavorite": true},
      {"id": "asset-a", "originalPath": "/archive/a.jpg", "smartInfo": {"objects": ["beach"]}}
    ]
  },
  {
    "albumName": "Best Of",
    "assets": [
      {"id": "asset-a", "externalPath": "/external/a.jpg"}
    ]
  }
]`

func TestImmichImporterPreservesAlbumMembershipAndOrderOnly(t *testing.T) {
	path := t.TempDir() + "/albums.json"
	if err := os.WriteFile(path, []byte(immichFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewImmichSource(path)
	if got := source.Categories(); !reflect.DeepEqual(got, []string{sdk.CategoryCollections}) {
		t.Fatalf("Categories() = %v", got)
	}

	got, err := source.Items(t.Context()) //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
	if err != nil {
		t.Fatal(err)
	}
	want := []sdk.ForeignItem{
		{
			Path:        "/archive/b.jpg",
			ExternalIDs: []sdk.ExternalID{{Scheme: "immich-asset", Value: "asset-b"}},
			CollectionMember: sdk.CollectionMember{
				Present: true, Name: "Family Weekend", Position: 0,
			},
		},
		{
			Path:        "/archive/a.jpg",
			ExternalIDs: []sdk.ExternalID{{Scheme: "immich-asset", Value: "asset-a"}},
			CollectionMember: sdk.CollectionMember{
				Present: true, Name: "Family Weekend", Position: 1,
			},
		},
		{
			Path:        "/external/a.jpg",
			ExternalIDs: []sdk.ExternalID{{Scheme: "immich-asset", Value: "asset-a"}},
			CollectionMember: sdk.CollectionMember{
				Present: true, Name: "Best Of", Position: 0,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %#v, want %#v", got, want)
	}

	again, err := source.Items(t.Context()) //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("additive re-read changed items: %#v", again)
	}

	bundle := Bundle()
	var spec *sdk.ImporterSpec
	for i := range bundle.Importers {
		if bundle.Importers[i].ID == ImmichImporterID {
			spec = &bundle.Importers[i]
			break
		}
	}
	if spec == nil || !reflect.DeepEqual(spec.Categories, []string{sdk.CategoryCollections}) || spec.Input != "export-file" || spec.Conflict != "additive" {
		t.Fatalf("Immich ImporterSpec = %#v", spec)
	}
}

func TestImmichImporterHandlesInputFailuresAndUnusableRecords(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := NewImmichSource(t.TempDir() + "/missing.json").Items(t.Context()) //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
		if err == nil {
			t.Fatal("Items() error = nil, want missing-file error")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := NewImmichSource(t.TempDir() + "/missing.json").Items(ctx); !errors.Is(err, context.Canceled) { //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
			t.Fatalf("Items() error = %v, want context.Canceled", err)
		}
	})

	t.Run("read failure", func(t *testing.T) {
		_, err := ParseImmichJSON(failingImmichReader{})
		if !errors.Is(err, errImmichFixtureRead) {
			t.Fatalf("error = %v, want %v", err, errImmichFixtureRead)
		}
	})

	t.Run("oversized export", func(t *testing.T) {
		_, err := ParseImmichJSON(strings.NewReader(strings.Repeat(" ", maxImmichImportBytes+1)))
		if !errors.Is(err, errImmichImportTooLarge) {
			t.Fatalf("error = %v, want %v", err, errImmichImportTooLarge)
		}
	})

	t.Run("malformed export", func(t *testing.T) {
		if _, err := ParseImmichJSON(strings.NewReader("[")); err == nil {
			t.Fatal("ParseImmichJSON() error = nil, want malformed JSON error")
		}
	})

	t.Run("empty export", func(t *testing.T) {
		items, err := ParseImmichJSON(strings.NewReader("  "))
		if err != nil || len(items) != 0 {
			t.Fatalf("items = %#v, error = %v; want empty success", items, err)
		}
	})

	t.Run("unusable records skipped without renumbering", func(t *testing.T) {
		items, err := ParseImmichJSON(strings.NewReader(`[
          {"albumName":"", "assets":[{"id":"asset-without-album"}]},
          {"albumName":"Valid", "assets":[{}, {"id":"id-only"}]}
        ]`))
		if err != nil {
			t.Fatal(err)
		}
		want := []sdk.ForeignItem{{
			ExternalIDs: []sdk.ExternalID{{Scheme: "immich-asset", Value: "id-only"}},
			CollectionMember: sdk.CollectionMember{
				Present: true, Name: "Valid", Position: 1,
			},
		}}
		if !reflect.DeepEqual(items, want) {
			t.Fatalf("items = %#v, want %#v", items, want)
		}
	})
}

func TestImmichImporterStreamingCancellationAndCheckpoint(t *testing.T) {
	path := t.TempDir() + "/albums.json"
	assets := make([]immichAsset, 513)
	for i := range assets {
		assets[i] = immichAsset{ID: fmt.Sprintf("asset-%03d", i), OriginalPath: fmt.Sprintf("/archive/%03d.jpg", i)}
	}
	fixture, err := json.Marshal([]immichAlbum{{AlbumName: "Paged", Assets: assets}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	source := NewImmichSource(path)
	items, err := source.Items(context.Background()) //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
	if err != nil {
		t.Fatal(err)
	}
	streamed, err := collectImmichStream(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(streamed, items) {
		t.Fatalf("streamed items differ from compatibility Items:\nstream: %#v\nitems:  %#v", streamed, items)
	}
	streaming := source.(interface {
		Stream(context.Context, string, func(context.Context, []sdk.ForeignItem, string) error) error
	})
	ctx, cancel := context.WithCancel(context.Background())
	resumed := []sdk.ForeignItem{}
	err = streaming.Stream(ctx, "256", func(_ context.Context, page []sdk.ForeignItem, _ string) error {
		resumed = append(resumed, page...)
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resume error = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(resumed, items[256:512]) {
		t.Fatalf("resumed page differs: got %d items, want 256", len(resumed))
	}
	callbackErr := errors.New("stop after durable page")
	err = streaming.Stream(context.Background(), "512", func(context.Context, []sdk.ForeignItem, string) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v, want %v", err, callbackErr)
	}
	for _, checkpoint := range []string{"bad", "514"} {
		if err := streaming.Stream(context.Background(), checkpoint, func(context.Context, []sdk.ForeignItem, string) error {
			return nil
		}); err == nil {
			t.Errorf("checkpoint %q returned nil error", checkpoint)
		}
	}
	sdktest.RunImporterConformance(t, []sdktest.ImporterVector{{
		Name:           ImmichImporterID,
		Source:         source,
		WantCategories: []string{sdk.CategoryCollections},
		WantItems:      items,
	}})
}

func TestImmichStreamHandlesEmptyUnknownAndMalformedShapes(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		wantErr bool
	}{
		{name: "empty"},
		{name: "scalar", fixture: `1`, wantErr: true},
		{name: "non-object album", fixture: `[1]`, wantErr: true},
		{name: "invalid album name", fixture: `[{"albumName":[]}]`, wantErr: true},
		{name: "non-array assets", fixture: `[{"albumName":"A","assets":{}}]`, wantErr: true},
		{name: "invalid asset", fixture: `[{"albumName":"A","assets":[1]}]`, wantErr: true},
		{name: "trailing value", fixture: `[] []`, wantErr: true},
		{name: "trailing value after null", fixture: `null []`, wantErr: true},
		{name: "truncated array", fixture: `[`, wantErr: true},
		{name: "unknown nested values", fixture: `[{"ignored":{"nested":[1]}}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir() + "/albums.json"
			if err := os.WriteFile(path, []byte(test.fixture), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := collectImmichStream(NewImmichSource(path))
			if (err != nil) != test.wantErr {
				t.Fatalf("Stream error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestImmichStreamRejectsUnavailableOversizedAndCanceledSources(t *testing.T) {
	if _, err := collectImmichStream(NewImmichSource(t.TempDir() + "/missing.json")); err == nil {
		t.Fatal("missing source returned nil error")
	}

	oversized := t.TempDir() + "/oversized.json"
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImmichImportBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := collectImmichStream(NewImmichSource(oversized)); !errors.Is(err, errImmichImportTooLarge) {
		t.Fatalf("oversized source error = %v, want %v", err, errImmichImportTooLarge)
	}

	path := t.TempDir() + "/albums.json"
	if err := os.WriteFile(path, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	streaming := NewImmichSource(path).(interface {
		Stream(context.Context, string, func(context.Context, []sdk.ForeignItem, string) error) error
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := streaming.Stream(ctx, "", func(context.Context, []sdk.ForeignItem, string) error {
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Stream error = %v, want context.Canceled", err)
	}
}

func TestSkipImmichJSONValueRejectsInvalidTokens(t *testing.T) {
	for _, fixture := range []string{"", "}"} {
		decoder := json.NewDecoder(strings.NewReader(fixture))
		if err := skipImmichJSONValue(decoder); err == nil {
			t.Errorf("skipImmichJSONValue(%q) returned nil error", fixture)
		}
	}
}

func TestImmichStreamDetectsAlbumCountChanges(t *testing.T) {
	path := t.TempDir() + "/albums.json"
	if err := os.WriteFile(path, []byte(`[{"albumName":"A"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	pager, err := newImmichPageEmitter(context.Background(), "", func(context.Context, []sdk.ForeignItem, string) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{nil, {"A", "B"}} {
		if err := streamImmichAlbums(context.Background(), path, names, pager); err == nil {
			t.Fatalf("streamImmichAlbums names %v returned nil error", names)
		}
	}
}

func TestImmichStreamMatchesCompatibilityForNullShapes(t *testing.T) {
	for _, fixture := range []string{
		`null`,
		`[null,{"albumName":"Empty","assets":null}]`,
	} {
		path := t.TempDir() + "/albums.json"
		if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
			t.Fatal(err)
		}
		source := NewImmichSource(path)
		items, err := source.Items(context.Background()) //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
		if err != nil {
			t.Fatal(err)
		}
		streamed, err := collectImmichStream(source)
		if err != nil {
			t.Fatal(err)
		}
		if len(streamed) != 0 || len(items) != 0 {
			if reflect.DeepEqual(streamed, items) {
				continue
			}
			t.Fatalf("streamed items differ from compatibility Items: stream=%#v items=%#v", streamed, items)
		}
	}
}

func collectImmichStream(source sdk.Source) ([]sdk.ForeignItem, error) { //nolint:staticcheck // deliberate SDK 0.x compatibility assertion
	streaming := source.(interface {
		Stream(context.Context, string, func(context.Context, []sdk.ForeignItem, string) error) error
	})
	items := []sdk.ForeignItem{}
	err := streaming.Stream(context.Background(), "", func(
		_ context.Context,
		page []sdk.ForeignItem,
		_ string,
	) error {
		items = append(items, page...)
		return nil
	})
	return items, err
}

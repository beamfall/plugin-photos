// Package photos pins the Photos bundle against the SDK contract.
// RunBundleConformance checks the
// declarative invariants every plugin must satisfy: lowercase-slug ID, semver
// Version, "en" anchor on every LocaleString, and MediaClasses from the fixed
// vocabulary.
package photos

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/beamfall/plugin-sdk"
	sdktest "github.com/beamfall/plugin-sdk/sdktest"
)

func TestBundleConformance(t *testing.T) {
	sdktest.RunBundleConformance(t, Bundle())
	sdktest.RequireEntityKindVocab(t, Bundle(), []sdktest.KindVocab{
		{Name: "album", Archetype: "container"},
		{Name: "location", Archetype: "category"},
	})
}

func TestPhotoSearchDeclaration(t *testing.T) {
	bundle := Bundle()
	searchable := map[string]bool{}
	for _, field := range bundle.MediaFields {
		searchable[field.Name] = field.Searchable
	}
	for _, name := range []string{"capture-time", "camera-make", "camera-model", "album", "place"} {
		if !searchable[name] {
			t.Errorf("MediaField %q is not searchable", name)
		}
	}
	layout, ok := bundle.BrowseLayout()
	if !ok {
		t.Fatal("browse Layout missing")
	}
	for _, section := range layout.Sections {
		if section.ID == "all-photos" && section.Source.Kind == "all-items" {
			return
		}
	}
	t.Fatal("all-photos Core search Source missing")
}

func TestExtractLocalConformance(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Paris Trip", "photo.jpg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(writeJPEGFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := New().ExtractLocal(context.Background(), imageMediaClass, root, path)
	if err != nil {
		t.Fatalf("ExtractLocal: %v", err)
	}

	assertLocalFieldKeysConform(t, Bundle(), got)
	assertLocalEntityCardinalitiesConform(t, Bundle(), got.Entities)
}

func assertLocalFieldKeysConform(t *testing.T, b sdk.Bundle, got LocalMetadata) {
	t.Helper()
	// Mirrors core scan.mediaFieldsFromLocalMetadata's local-file key contract.
	coreLocalFields := map[string]string{
		"capture_date": "capture-date",
		"camera_make":  "camera-make",
		"camera_model": "camera-model",
		"width":        "width",
		"height":       "height",
		"orientation":  "orientation",
		"place":        "place",
	}
	mediaFieldTypes := b.MediaFieldTypes()
	for key, field := range coreLocalFields {
		if _, ok := got.Fields[key]; !ok {
			t.Errorf("ExtractLocal Fields missing core-read key %q", key)
		}
		if _, ok := mediaFieldTypes[field]; !ok {
			t.Errorf("core-read local key %q maps to undeclared media field %q", key, field)
		}
	}
	for key := range got.Fields {
		if _, ok := coreLocalFields[key]; !ok {
			t.Errorf("ExtractLocal emitted field key %q that core does not read", key)
		}
	}
	for key, value := range got.ImpliedMediaFields {
		if value == "" {
			t.Errorf("ExtractLocal implied media field %q has empty value", key)
		}
		if _, err := sdk.ClassifyMediaFieldKey(key, mediaFieldTypes); err != nil {
			t.Errorf("ExtractLocal implied media field %q is not declared by Bundle: %v", key, err)
		}
	}
}

func assertLocalEntityCardinalitiesConform(t *testing.T, b sdk.Bundle, entities []LocalEntity) {
	t.Helper()
	cardinalities := map[string]string{}
	for _, kind := range b.EntityKinds {
		for _, relation := range kind.Relations {
			cardinalities[kind.Name+"\x00"+relation.Name] = relation.Cardinality
		}
	}
	for _, entity := range entities {
		want, ok := cardinalities[entity.Kind+"\x00"+entity.Relation]
		if !ok {
			t.Errorf("ExtractLocal emitted undeclared relation %s.%s", entity.Kind, entity.Relation)
			continue
		}
		if entity.Cardinality != want {
			t.Errorf("%s.%s cardinality = %q, want %q", entity.Kind, entity.Relation, entity.Cardinality, want)
		}
	}
}

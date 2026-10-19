// Package photos is the first-party Photos Domain Plugin bundle: a purely
// declarative contribution against Core's fixed plugin vocabularies — Entity
// Kinds, a home Layout, an Immich album importer, and an EXIF scraper. It adds
// no schema and no query strings; the image MediaClass it leans on
// (direct-bytes playback, no transcode) already lives in Core, and its
// local-file EXIF reader + offline geocoder live in this plugin's compiled hook
// layer.
//
// Shape: a Photo is Media — "Photo" is a display label, never a schema
// identifier. Album is an ordered container (a Photo
// belongs to zero or more Albums; default order is chronological by capture
// time, which OrderedChildren carries). Location is a flat category derived from
// EXIF GPS, geocoded to a coarse place name (a Photo belongs to zero or one
// Location). People/Faces belong to an Enrichment Plugin, not this bundle.
//
// Playback is a client-side slideshow with no resume position: Engagement pins
// ResumeMedia false even though image is not an audio class — resume
// eligibility is declarative, not a class convention.
package photos

import sdk "github.com/beamfall/plugin-sdk"

// PluginID must match Bundle().ID and the core composition root hook key.
const PluginID = "photos"

// Bundle returns the Photos plugin's declarative contribution.
func Bundle() sdk.Bundle {
	return sdk.Bundle{
		ID:          PluginID,
		Version:     "0.1.4",
		SDKFloor:    "0.2.0",
		SDKCeiling:  "0.3.2",
		DisplayName: sdk.LocaleString{"en": "Photos", "fr": "Photos"},
		// A Photo is Media — display label only, never a schema identifier.
		MediaLabel:   sdk.LocaleString{"en": "Photo", "fr": "Photo"},
		MediaClasses: []string{"image"},
		// Photos do not resume — playback is a client-side slideshow with no
		// canonical timeline and no resume position. Engagement events are not
		// recorded for photo viewing; the no-resume posture here is the
		// declarative half (resume is a property, not a class convention).
		Engagement: sdk.Engagement{ResumeMedia: boolPtr(false)},
		// The generic image facts the "exif" hook produces (capture_date, camera_make/model, width, height, orientation, place)
		// are the Fields-payload keys the hook writes for core's scan layer to
		// consume — snake_case, distinct from the kebab-case FieldSpec.Name
		// values declared below. They land on these Media fields and
		// are media-shape names, never domain vocabulary. "class" is the
		// conventional subdivision field the exif scraper implies ("photo") so
		// the no-resume image posture is selected per-Media.
		MediaFields: []sdk.FieldSpec{
			{Name: "class", Type: "text",
				Label: sdk.LocaleString{"en": "Class", "fr": "Classe"}},
			{Name: "capture-date", Type: "date", Sortable: true,
				Label: sdk.LocaleString{"en": "Captured", "fr": "Pris le"}},
			{Name: "capture-time", Type: "text", Searchable: true,
				Label: sdk.LocaleString{"en": "Capture Time", "fr": "Heure de prise"}},
			{Name: "camera-make", Type: "text", Searchable: true,
				Label: sdk.LocaleString{"en": "Camera Make", "fr": "Marque de l'appareil"}},
			{Name: "camera-model", Type: "text", Searchable: true,
				Label: sdk.LocaleString{"en": "Camera Model", "fr": "Modèle de l'appareil"}},
			{Name: "album", Type: "text", Searchable: true,
				Label: sdk.LocaleString{"en": "Album", "fr": "Album"}},
			{Name: "width", Type: "number",
				Label: sdk.LocaleString{"en": "Width", "fr": "Largeur"}},
			{Name: "height", Type: "number",
				Label: sdk.LocaleString{"en": "Height", "fr": "Hauteur"}},
			{Name: "orientation", Type: "number",
				Label: sdk.LocaleString{"en": "Orientation", "fr": "Orientation"}},
			// The coarse offline-geocoded place name. It also
			// names the by-location grid's Location entities; the scan pass
			// hydrates them when it runs the local EXIF scraper.
			{Name: "place", Type: "text", Filterable: true, Searchable: true,
				Label: sdk.LocaleString{"en": "Location", "fr": "Lieu"}},
		},
		EntityKinds: []sdk.EntityKind{
			{
				// The organizational container. Children are Photos (Media) in
				// chronological capture order — OrderedChildren carries the
				// default chronological sort. Album membership is derived from
				// directory structure by default (directory = Album);
				// reorganization is a Manager concern.
				Name:            "album",
				DisplayName:     sdk.LocaleString{"en": "Album", "fr": "Album"},
				Archetype:       "container",
				OrderedChildren: true,
				Relations:       []sdk.RelationSpec{{Name: "contains", ToKind: sdk.MediaKindName}},
			},
			{
				// Derived from EXIF GPS, geocoded to a coarse place name. Flat — a Photo belongs to zero or one
				// Location; no place hierarchy (the geocoder yields one
				// "near City, Country" string, not a tree).
				Name:        "location",
				DisplayName: sdk.LocaleString{"en": "Place", "fr": "Lieu"},
				Archetype:   "category",
				Relations:   []sdk.RelationSpec{{Name: "located-in", ToKind: sdk.MediaKindName, Cardinality: "one"}},
			},
		},
		// Home Layout. Each section maps to the fixed core Source vocabulary:
		// all-photos → all-items (chronological is a Source-ordering concern),
		// albums → an entity-items grid of Albums, by-location → an
		// entity-items grid of Locations, on-this-day → a date-field projection
		// over capture-date, and recently-added → Core's recency shelf.
		//
		// Section titles ship as full locale maps: en + fr today.
		Layouts: []sdk.LayoutSpec{{ID: "home", Role: "browse", Sections: []sdk.SectionSpec{
			{
				ID:          "all-photos",
				Kind:        "grid",
				Title:       sdk.LocaleString{"en": "All Photos", "fr": "Toutes les photos"},
				ArtworkSlot: "thumb",
				// A runtime query turns the generic all-items feed into Core's
				// reveal-filtered keyword search; the plugin contributes no query logic.
				Source: sdk.SourceSpec{Kind: "all-items"},
			},
			{
				ID:          "albums",
				Kind:        "grid",
				Title:       sdk.LocaleString{"en": "Albums", "fr": "Albums"},
				ArtworkSlot: "poster",
				Source:      sdk.SourceSpec{Kind: "entity-items", EntityKind: "album"},
			},
			{
				// Suppressed client-side when the Library has no GPS data: an empty Location set yields an empty grid,
				// which clients render as no shelf.
				ID:          "by-location",
				Kind:        "grid",
				Title:       sdk.LocaleString{"en": "Places", "fr": "Lieux"},
				ArtworkSlot: "poster",
				Source:      sdk.SourceSpec{Kind: "entity-items", EntityKind: "location"},
			},
			{
				ID:          "on-this-day",
				Kind:        "shelf",
				Title:       sdk.LocaleString{"en": "On This Day", "fr": "Ce jour-là"},
				ArtworkSlot: "thumb",
				Source:      sdk.SourceSpec{Kind: "on-this-day"},
			},
			{
				ID:          "recently-added",
				Kind:        "shelf",
				Title:       sdk.LocaleString{"en": "Recently Added", "fr": "Ajouts récents"},
				ArtworkSlot: "thumb",
				Source:      sdk.SourceSpec{Kind: "recently-added"},
			},
		}}},
		Importers: []sdk.ImporterSpec{
			{
				ID:          ImmichImporterID,
				DisplayName: sdk.LocaleString{"en": "Immich albums", "fr": "Albums Immich"},
				Categories:  []string{sdk.CategoryCollections},
				Input:       "export-file",
				Conflict:    "additive",
			},
		},
		Scrapers: []sdk.ScraperSpec{
			{
				// The zero-configuration EXIF reader: no
				// endpoint, no API key, no network — it reads the Photo's own
				// file. The "exif" local-file kind emits a fixed generic field
				// set (the scraper framework forbids FieldMaps on it), so this
				// declaration carries no field/artwork maps. ImpliedMediaFields
				// stamps the conventional "class" field so the no-resume image
				// posture is selected at identification (the same mechanism the
				// Film & TV plugin uses for movie/episode subdivision).
				ID:                 "exif",
				DisplayName:        sdk.L("EXIF"),
				Kind:               "exif",
				ImpliedMediaFields: map[string]string{"media.class": "photo"},
			},
		},
	}
}

func boolPtr(value bool) *bool {
	return &value
}

<p align="center">
  <img src=".github/brand/beamfall-logo.png" alt="Beamfall" width="96" height="96">
</p>

# Beamfall Photos

**Your photo folders, turned into a browsable library, without anything leaving your server.**
Beamfall Photos is the plugin that gives [Beamfall Core](https://github.com/beamfall/core) its
photo experience. Point a Beamfall library at the folders you already keep, and Photos reads each
picture's own metadata to sort it by when it was taken, group it into albums, and label where it
was taken. It works offline, needs no configuration, and calls no outside service.

It is free and open source under [AGPL-3.0](LICENSE).

## What you get

- **Albums from the folders you already have.** Each folder becomes an Album, and the photos in it
  sort by the moment they were taken.
- **Capture times you can trust.** Photos reads the EXIF capture time and keeps the time zone it
  was taken in, from the camera's offset tag or from the GPS clock. Albums shot across time zones
  still sort in true order. When a photo has no usable EXIF date, it falls back to the file's
  modification time and flags the time as inferred, so the photo stays in your library instead of
  disappearing.
- **Places, worked out offline.** GPS coordinates become a coarse place name such as
  "Paris, France", using a city table bundled with the plugin. Your locations are never sent to a
  geocoding service. Photos with no GPS data simply have no place.
- **RAW files as well as JPEG.** The EXIF reader handles JPEG and TIFF-based RAW formats (CR2, NEF,
  NRW, ARW, DNG, ORF, RW2, PEF, SRW, 3FR), Fujifilm RAF, and Canon CR3. It picks up capture time,
  camera make and model, pixel dimensions, orientation, and GPS.
- **A home screen built for photos.** All Photos, Albums, Places, On This Day, and Recently Added.
  The Places grid stays hidden when a library has no GPS data.
- **Search that knows photos.** Capture time, camera make, camera model, album, and place are all
  searchable, and you can filter by place.
- **Bring your Immich albums.** Import album membership and order from a saved response of
  Immich's `GET /albums` API. Only album names and their photos are read; everything else in the
  file is ignored.
- **Made for viewing, not resuming.** Photos play as a slideshow, so Beamfall does not track a
  resume position for them.
- **Careful with your files.** The EXIF reader is small and has no dependencies. It reads only a
  bounded slice of each file, never decodes pixels, and is fuzz tested. A damaged or unusual file
  produces empty metadata, not an error.
- **English and French** labels out of the box.

## How it fits into Beamfall

Photos ships with [Beamfall Core](https://github.com/beamfall/core), the self-hosted media server
for the library you already own. You do not install it separately: Core loads it, and your photo
libraries use it.

The plugin is built on the [Beamfall plugin SDK](https://github.com/beamfall/plugin-sdk). Most of
it is declarative. `Bundle()` in [`bundle.go`](bundle.go) describes the Album and Place kinds, the
photo fields, the home layout, the Immich importer, and the EXIF scraper, and Core does the rest.
The only compiled code is the metadata extraction in [`local.go`](local.go), the EXIF reader in
[`internal/exif`](internal/exif), the offline geocoder in [`internal/geocode`](internal/geocode),
and the Immich importer in [`import_immich.go`](import_immich.go).

## Develop

You need Go (the version in [`go.mod`](go.mod)), Python 3, and
[golangci-lint](https://golangci-lint.run/).

```sh
go test ./...          # unit tests and the SDK conformance harness
script/gate.sh         # gofmt, go vet, tests with per-package coverage floors, golangci-lint
script/gate.sh race    # the test suite under the race detector
script/gate.sh fuzz    # a bounded fuzz run of the EXIF reader
```

[`conformance_test.go`](conformance_test.go) runs the SDK's `sdktest` conformance harness against
`Bundle()` and the extraction hook. CI runs the same checks on every push and pull request; see
[`.github/workflows/conformance.yml`](.github/workflows/conformance.yml).

## Contributing

Contributions need a signed Contributor License Agreement. See [CONTRIBUTING.md](CONTRIBUTING.md)
and [CLA.md](CLA.md).

## Security

Please report vulnerabilities privately. See [SECURITY.md](SECURITY.md).

## License

Beamfall Photos is licensed under the GNU Affero General Public License v3. See [LICENSE](LICENSE).

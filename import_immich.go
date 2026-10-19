package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	sdk "github.com/beamfall/plugin-sdk"
)

const (
	ImmichImporterID      = "immich"
	maxImmichImportBytes  = 64 << 20
	immichImportPageItems = 256
)

var errImmichImportTooLarge = errors.New("immich import: source exceeds 64 MB")

type immichSource struct{ jsonPath string }

func (immichSource) Categories() []string { return []string{sdk.CategoryCollections} }

func (s immichSource) Items(ctx context.Context) ([]sdk.ForeignItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.jsonPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseImmichJSON(f)
}

func (s immichSource) Stream(
	ctx context.Context,
	checkpoint string,
	emit func(context.Context, []sdk.ForeignItem, string) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := inspectImmichAlbumNames(ctx, s.jsonPath)
	if err != nil {
		return err
	}
	pager, err := newImmichPageEmitter(ctx, checkpoint, emit)
	if err != nil {
		return err
	}
	if err := streamImmichAlbums(ctx, s.jsonPath, names, pager); err != nil {
		return err
	}
	return pager.finish()
}

// NewImmichSource reads a saved response from Immich's GET /albums API.
func NewImmichSource(jsonPath string) sdk.Source { return immichSource{jsonPath: jsonPath} } //nolint:staticcheck // deliberate SDK 0.x compatibility path for pre-streaming consumers

type immichAlbum struct {
	AlbumName string        `json:"albumName"`
	Assets    []immichAsset `json:"assets"`
}

type immichAsset struct {
	ID           string `json:"id"`
	OriginalPath string `json:"originalPath"`
	ExternalPath string `json:"externalPath"`
}

func inspectImmichAlbumNames(ctx context.Context, path string) ([]string, error) {
	f, err := openImmichImport(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, maxImmichImportBytes+1))
	start, err := decoder.Token()
	if err == io.EOF {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if start == nil {
		if _, err := decoder.Token(); err != io.EOF {
			if err == nil {
				return nil, errors.New("immich import: trailing JSON value")
			}
			return nil, err
		}
		return []string{}, nil
	}
	if start != json.Delim('[') {
		return nil, errors.New("immich import: expected album array")
	}
	names := []string{}
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := inspectImmichAlbum(decoder)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if _, err = decoder.Token(); err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("immich import: trailing JSON value")
		}
		return nil, err
	}
	return names, nil
}

func inspectImmichAlbum(decoder *json.Decoder) (string, error) {
	start, err := decoder.Token()
	if err != nil {
		return "", err
	}
	if start == nil {
		return "", nil
	}
	if start != json.Delim('{') {
		return "", errors.New("immich import: album is not an object")
	}
	name := ""
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return "", err
		}
		if key == "albumName" {
			err = decoder.Decode(&name)
		} else {
			err = skipImmichJSONValue(decoder)
		}
		if err != nil {
			return "", err
		}
	}
	_, err = decoder.Token()
	return strings.TrimSpace(name), err
}

func streamImmichAlbums(
	ctx context.Context,
	path string,
	names []string,
	pager *immichPageEmitter,
) error {
	f, err := openImmichImport(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	decoder := json.NewDecoder(io.LimitReader(f, maxImmichImportBytes+1))
	start, err := decoder.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	if start != json.Delim('[') {
		return errors.New("immich import: expected album array")
	}
	album := 0
	for decoder.More() {
		if album >= len(names) {
			return errors.New("immich import: album count changed during import")
		}
		if err := streamImmichAlbum(ctx, decoder, names[album], pager); err != nil {
			return err
		}
		album++
	}
	_, err = decoder.Token()
	if err != nil {
		return err
	}
	if album != len(names) {
		return errors.New("immich import: album count changed during import")
	}
	return nil
}

func streamImmichAlbum(
	ctx context.Context,
	decoder *json.Decoder,
	name string,
	pager *immichPageEmitter,
) error {
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	if start != json.Delim('{') {
		return errors.New("immich import: album is not an object")
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		if key == "assets" {
			err = streamImmichAssets(ctx, decoder, name, pager)
		} else {
			err = skipImmichJSONValue(decoder)
		}
		if err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func streamImmichAssets(
	ctx context.Context,
	decoder *json.Decoder,
	name string,
	pager *immichPageEmitter,
) error {
	start, err := decoder.Token()
	if err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	if start != json.Delim('[') {
		return errors.New("immich import: assets is not an array")
	}
	position := 0
	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var asset immichAsset
		if err := decoder.Decode(&asset); err != nil {
			return err
		}
		item, ok := asset.foreignItem(name, position)
		position++
		if !ok {
			continue
		}
		if err := pager.add(item); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func openImmichImport(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() > maxImmichImportBytes {
		_ = f.Close()
		return nil, errImmichImportTooLarge
	}
	return f, nil
}

type immichPageEmitter struct {
	ctx    context.Context
	emit   func(context.Context, []sdk.ForeignItem, string) error
	resume int
	seen   int
	page   []sdk.ForeignItem
}

func newImmichPageEmitter(
	ctx context.Context,
	checkpoint string,
	emit func(context.Context, []sdk.ForeignItem, string) error,
) (*immichPageEmitter, error) {
	resume := 0
	var err error
	if checkpoint != "" {
		resume, err = strconv.Atoi(checkpoint)
		if err != nil || resume < 0 {
			return nil, fmt.Errorf("invalid Immich import checkpoint %q", checkpoint)
		}
	}
	return &immichPageEmitter{
		ctx:    ctx,
		emit:   emit,
		resume: resume,
		page:   make([]sdk.ForeignItem, 0, immichImportPageItems),
	}, nil
}

func (p *immichPageEmitter) add(item sdk.ForeignItem) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.seen++
	if p.seen <= p.resume {
		return nil
	}
	p.page = append(p.page, item)
	if len(p.page) < immichImportPageItems {
		return nil
	}
	return p.flush()
}

func (p *immichPageEmitter) finish() error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if p.seen < p.resume {
		return fmt.Errorf("invalid Immich import checkpoint %d: beyond source", p.resume)
	}
	return p.flush()
}

func (p *immichPageEmitter) flush() error {
	if len(p.page) == 0 {
		return nil
	}
	if err := p.emit(p.ctx, p.page, strconv.Itoa(p.seen)); err != nil {
		return err
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.page = make([]sdk.ForeignItem, 0, immichImportPageItems)
	return nil
}

func skipImmichJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := skipImmichJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := skipImmichJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("immich import: unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func (a immichAsset) foreignItem(name string, position int) (sdk.ForeignItem, bool) {
	path := strings.TrimSpace(a.OriginalPath)
	if path == "" {
		path = strings.TrimSpace(a.ExternalPath)
	}
	var externalIDs []sdk.ExternalID
	if id := strings.TrimSpace(a.ID); id != "" {
		externalIDs = []sdk.ExternalID{{Scheme: "immich-asset", Value: id}}
	}
	if name == "" || (path == "" && len(externalIDs) == 0) {
		return sdk.ForeignItem{}, false
	}
	return sdk.ForeignItem{
		Path:        path,
		ExternalIDs: externalIDs,
		CollectionMember: sdk.CollectionMember{
			Present:  true,
			Name:     name,
			Position: position,
		},
	}, true
}

// ParseImmichJSON maps each asset in each album to an ordered static
// collection membership. Unrelated Immich state is intentionally ignored.
func ParseImmichJSON(r io.Reader) ([]sdk.ForeignItem, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxImmichImportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxImmichImportBytes {
		return nil, errImmichImportTooLarge
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return []sdk.ForeignItem{}, nil
	}

	var albums []immichAlbum
	if err := json.Unmarshal(raw, &albums); err != nil {
		return nil, err
	}

	var items []sdk.ForeignItem
	for _, album := range albums {
		name := strings.TrimSpace(album.AlbumName)
		for position, asset := range album.Assets {
			item, ok := asset.foreignItem(name, position)
			if !ok {
				continue
			}
			items = append(items, item)
		}
	}
	return items, nil
}

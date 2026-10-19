package exif

import (
	"bytes"
	"errors"
	"testing"
)

// FuzzReadEXIF drives arbitrary image bytes through the bounded JPEG/TIFF/RAW
// parser with determinism, error atomicity, and valid-seed oracles.
func FuzzReadEXIF(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("not a jpeg"))
	valid := buildJPEG(buildTIFFWithSubIFDs([]tiffEntry{
		{tag: tagOrientation, typ: typeShort, count: 1, value: u16padded(6)},
	}, nil, nil))
	f.Add(valid)
	f.Add(rawBigEndianTIFF())
	f.Add(buildRAF(valid))
	f.Add(buildCR3())

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxJPEGMetadataBytes+1 {
			return
		}
		got, err := Read(bytes.NewReader(data))
		again, againErr := Read(bytes.NewReader(data))
		if got != again || !errors.Is(err, againErr) || !errors.Is(againErr, err) {
			t.Fatalf("Read is not deterministic: (%+v, %v) != (%+v, %v)", got, err, again, againErr)
		}
		if err != nil && !errors.Is(err, errMarkerScanLimit) {
			t.Fatalf("unexpected parser error: %v", err)
		}
		if err != nil && got != (Metadata{}) {
			t.Fatalf("parser error returned partial metadata: %+v, %v", got, err)
		}
		if bytes.Equal(data, valid) && got.Orientation != 6 {
			t.Fatalf("valid EXIF seed lost orientation: %+v", got)
		}
	})
}

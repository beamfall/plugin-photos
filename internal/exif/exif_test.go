package exif

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// buildJPEG wraps a TIFF Exif payload in the minimal JPEG envelope the reader
// scans: SOI, an APP1 segment carrying "Exif\x00\x00"+tiff, then EOI. No pixel
// data — the reader stops at metadata, so a decodable image is unnecessary.
func buildJPEG(tiff []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	length := len(app1) + 2
	b.Write([]byte{0xFF, 0xE1, byte(length >> 8), byte(length)})
	b.Write(app1)
	b.Write([]byte{0xFF, 0xD9}) // EOI
	return b.Bytes()
}

// tiffEntry is one IFD entry. Exactly one of value (a 4-byte inline field) or
// extEnd (out-of-line data the builder places and points at) is set, except for
// sub-IFD pointer entries whose offset the builder fills via subIFD.
type tiffEntry struct {
	tag    uint16
	typ    uint16
	count  uint32
	value  []byte // 4-byte inline field
	extEnd []byte // out-of-line data; builder rewrites value to its offset
	subIFD []tiffEntry
}

// buildTIFFWithSubIFDs lays out a complete little-endian TIFF: the 8-byte
// header, IFD0, then for each sub-IFD an IFD0 pointer entry, then all the IFD
// blocks and out-of-line data contiguously with correct absolute offsets. exif
// and gps may be nil (no such sub-IFD). Offsets are computed in a first pass so
// the reader sees a real, self-consistent structure.
func buildTIFFWithSubIFDs(ifd0 []tiffEntry, exif, gps []tiffEntry) []byte {
	if exif != nil {
		ifd0 = append(ifd0, tiffEntry{tag: tagExifIFDPointer, typ: typeLong, count: 1, subIFD: exif})
	}
	if gps != nil {
		ifd0 = append(ifd0, tiffEntry{tag: tagGPSIFDPointer, typ: typeLong, count: 1, subIFD: gps})
	}

	const headerLen = 8
	// Lay out: header | IFD0 | (each sub-IFD block) | all out-of-line data.
	cursor := headerLen + ifdLen(ifd0)
	// Reserve space for each sub-IFD block and record its start offset.
	subOffsets := map[int]int{}
	for i := range ifd0 {
		if ifd0[i].subIFD != nil {
			subOffsets[i] = cursor
			cursor += ifdLen(ifd0[i].subIFD)
		}
	}
	// Out-of-line data region begins after all IFD blocks.
	var ext bytes.Buffer
	place := func(entries []tiffEntry) {
		for i := range entries {
			if entries[i].extEnd != nil {
				off := cursor + ext.Len()
				entries[i].value = u32(uint32(off))
				ext.Write(entries[i].extEnd)
			}
		}
	}
	place(ifd0)
	for i := range ifd0 {
		if ifd0[i].subIFD != nil {
			place(ifd0[i].subIFD)
			ifd0[i].value = u32(uint32(subOffsets[i]))
		}
	}

	var b bytes.Buffer
	b.Write([]byte{'I', 'I'})
	b.Write(u16(0x002A))
	b.Write(u32(headerLen))
	writeIFD(&b, ifd0)
	for i := range ifd0 {
		if ifd0[i].subIFD != nil {
			writeIFD(&b, ifd0[i].subIFD)
		}
	}
	b.Write(ext.Bytes())
	return b.Bytes()
}

// buildSubIFD is the standalone form used for readability in tests: it returns
// the entries unchanged for buildTIFFWithSubIFDs to place. (A pass-through so
// call sites read as "build a GPS sub-IFD from these entries".)
func buildSubIFD(entries []tiffEntry) []tiffEntry { return entries }

func ifdLen(entries []tiffEntry) int { return 2 + len(entries)*12 + 4 }

func writeIFD(b *bytes.Buffer, entries []tiffEntry) {
	b.Write(u16(uint16(len(entries))))
	for _, e := range entries {
		b.Write(u16(e.tag))
		b.Write(u16(e.typ))
		b.Write(u32(e.count))
		if len(e.value) != 4 {
			panic("tiff entry inline value must be exactly 4 bytes")
		}
		b.Write(e.value)
	}
	b.Write(u32(0)) // no next IFD
}

// pad4 right-pads an inline value field to exactly 4 bytes with NULs.
func pad4(v []byte) []byte {
	out := make([]byte, 4)
	copy(out, v)
	return out
}

func u16(v uint16) []byte {
	out := make([]byte, 2)
	binary.LittleEndian.PutUint16(out, v)
	return out
}

func u32(v uint32) []byte {
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, v)
	return out
}

func u32inline(v uint32) []byte { return u32(v) }

// rational3 packs three (num, den) rationals — the GPS DMS triple.
func rational3(d, m, s uint32) []byte {
	var b bytes.Buffer
	b.Write(u32(d))
	b.Write(u32(1))
	b.Write(u32(m))
	b.Write(u32(1))
	b.Write(u32(s))
	b.Write(u32(1))
	return b.Bytes()
}

// asciiPadded right-pads an ASCII value with a NUL and to a 4-byte minimum so a
// short string can sit inline; longer strings go out-of-line via extEnd.
func ascii(s string) []byte {
	return append([]byte(s), 0)
}

func TestReadFullEXIF(t *testing.T) {
	// Compose an IFD0 with Make/Model/Orientation/DateTimeOriginal direct (and
	// out-of-line where needed), plus an Exif sub-IFD with dimensions and a GPS
	// sub-IFD with a known coordinate. To keep offset bookkeeping simple the
	// test puts dimensions and GPS into IFD0-reachable sub-IFDs built inline.

	// GPS sub-IFD: lat 48°51'24"N (Paris ≈ 48.8567), lon 2°21'3"E.
	gpsEntries := []tiffEntry{
		{tag: gpsTagLatitudeRef, typ: typeASCII, count: 2, value: pad4(ascii("N"))},
		{tag: gpsTagLatitude, typ: typeRational, count: 3, extEnd: rational3(48, 51, 24)},
		{tag: gpsTagLongitudeRef, typ: typeASCII, count: 2, value: pad4(ascii("E"))},
		{tag: gpsTagLongitude, typ: typeRational, count: 3, extEnd: rational3(2, 21, 3)},
	}
	gpsTIFF := buildSubIFD(gpsEntries)

	exifEntries := []tiffEntry{
		{tag: tagPixelXDimension, typ: typeLong, count: 1, value: u32inline(4032)},
		{tag: tagPixelYDimension, typ: typeShort, count: 1, value: u16padded(3024)},
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
	}
	exifTIFF := buildSubIFD(exifEntries)

	// IFD0 carries the camera tags and pointers to the two sub-IFDs. The
	// sub-IFD blobs are appended and pointed at by offset.
	tiff := buildTIFFWithSubIFDs(
		[]tiffEntry{
			{tag: tagMake, typ: typeASCII, count: 6, extEnd: ascii("Canon")},
			{tag: tagModel, typ: typeASCII, count: 9, extEnd: ascii("EOS R5")},
			{tag: tagOrientation, typ: typeShort, count: 1, value: u16padded(6)},
		},
		exifTIFF, gpsTIFF,
	)

	md, err := Read(bytes.NewReader(buildJPEG(tiff)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md.CameraMake != "Canon" {
		t.Errorf("make = %q, want Canon", md.CameraMake)
	}
	if md.CameraModel != "EOS R5" {
		t.Errorf("model = %q, want EOS R5", md.CameraModel)
	}
	if md.Orientation != 6 {
		t.Errorf("orientation = %d, want 6", md.Orientation)
	}
	if md.Width != 4032 || md.Height != 3024 {
		t.Errorf("dimensions = %dx%d, want 4032x3024", md.Width, md.Height)
	}
	want := time.Date(2021, 7, 14, 9, 30, 15, 0, time.UTC)
	if !md.CaptureTime.Equal(want) {
		t.Errorf("capture = %v, want %v", md.CaptureTime, want)
	}
	if !md.HasGPS {
		t.Fatal("HasGPS = false, want true")
	}
	if d := md.Latitude - 48.8567; d > 0.001 || d < -0.001 {
		t.Errorf("lat = %v, want ~48.8567", md.Latitude)
	}
	if d := md.Longitude - 2.3508; d > 0.001 || d < -0.001 {
		t.Errorf("lon = %v, want ~2.3508", md.Longitude)
	}
}

func TestReadNoEXIF(t *testing.T) {
	// A bare JPEG (SOI + EOI, no APP1) and a non-JPEG blob both degrade to a
	// zero Metadata with no error.
	for name, data := range map[string][]byte{
		"bare-jpeg": {0xFF, 0xD8, 0xFF, 0xD9},
		"not-jpeg":  []byte("not an image at all"),
		"empty":     {},
	} {
		md, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
		if md != (Metadata{}) {
			t.Errorf("%s: md = %+v, want zero", name, md)
		}
	}
}

func TestReadEXIFNoGPS(t *testing.T) {
	// EXIF present (a capture date) but no GPS sub-IFD: HasGPS stays false and
	// the coordinate fields stay zero, no crash.
	exifTIFF := buildSubIFD([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2020:01:02 03:04:05")},
	})
	tiff := buildTIFFWithSubIFDs([]tiffEntry{
		{tag: tagMake, typ: typeASCII, count: 6, extEnd: ascii("Apple")},
	}, exifTIFF, nil)

	md, err := Read(bytes.NewReader(buildJPEG(tiff)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md.HasGPS {
		t.Error("HasGPS = true, want false")
	}
	if md.CameraMake != "Apple" {
		t.Errorf("make = %q, want Apple", md.CameraMake)
	}
	if md.CaptureTime.IsZero() {
		t.Error("capture time is zero, want set")
	}
}

func TestReadEXIFGPSDateTimeCorrectsCaptureOffset(t *testing.T) {
	exifTIFF := buildSubIFD([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
	})
	gpsTIFF := buildSubIFD([]tiffEntry{
		{tag: gpsTagTimeStamp, typ: typeRational, count: 3, extEnd: rational3(13, 30, 15)},
		{tag: gpsTagDateStamp, typ: typeASCII, count: 11, extEnd: ascii("2021:07:14")},
	})
	tiff := buildTIFFWithSubIFDs(nil, exifTIFF, gpsTIFF)

	md, err := Read(bytes.NewReader(buildJPEG(tiff)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := md.CaptureTime.Format("2006-01-02T15:04:05-07:00"), "2021-07-14T09:30:15-04:00"; got != want {
		t.Errorf("capture = %s, want %s", got, want)
	}
	if want := time.Date(2021, 7, 14, 13, 30, 15, 0, time.UTC); !md.CaptureTime.Equal(want) {
		t.Errorf("capture instant = %v, want %v", md.CaptureTime, want)
	}
}

func TestReadEXIFOffsetTimeOriginalNormalizesToUTC(t *testing.T) {
	// DateTimeOriginal is local wall-clock; OffsetTimeOriginal "+02:00" means the
	// true instant is 07:30:15 UTC, and CaptureTime must carry the +02:00 zone
	// for display while comparing equal to that UTC instant.
	exifTIFF := buildSubIFD([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
		{tag: tagOffsetTimeOriginal, typ: typeASCII, count: 7, extEnd: ascii("+02:00")},
	})
	tiff := buildTIFFWithSubIFDs(nil, exifTIFF, nil)

	md, err := Read(bytes.NewReader(buildJPEG(tiff)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !md.HasCaptureOffset {
		t.Error("HasCaptureOffset = false, want true")
	}
	if got, want := md.CaptureTime.Format("2006-01-02T15:04:05-07:00"), "2021-07-14T09:30:15+02:00"; got != want {
		t.Errorf("capture = %s, want %s (offset retained for display)", got, want)
	}
	if want := time.Date(2021, 7, 14, 7, 30, 15, 0, time.UTC); !md.CaptureTime.Equal(want) {
		t.Errorf("capture instant = %v, want %v", md.CaptureTime.UTC(), want)
	}
}

func TestReadEXIFOffsetTimeOriginalTakesPriorityOverGPS(t *testing.T) {
	// Both OffsetTimeOriginal (-05:00) and GPS-derived offset (would be -04:00)
	// are present; the explicit OffsetTimeOriginal wins (authoritative source).
	exifTIFF := buildSubIFD([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
		{tag: tagOffsetTimeOriginal, typ: typeASCII, count: 7, extEnd: ascii("-05:00")},
	})
	gpsTIFF := buildSubIFD([]tiffEntry{
		{tag: gpsTagTimeStamp, typ: typeRational, count: 3, extEnd: rational3(13, 30, 15)}, // implies -04:00
		{tag: gpsTagDateStamp, typ: typeASCII, count: 11, extEnd: ascii("2021:07:14")},
	})
	tiff := buildTIFFWithSubIFDs(nil, exifTIFF, gpsTIFF)

	md, err := Read(bytes.NewReader(buildJPEG(tiff)))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := md.CaptureTime.Format("-07:00"), "-05:00"; got != want {
		t.Errorf("offset = %s, want %s (OffsetTimeOriginal beats GPS)", got, want)
	}
}

func TestReadEXIFCrossTimezoneChronologicalOrder(t *testing.T) {
	// Two photos, same wall-clock 09:30:15, different zones. The eastern one
	// (+09:00) is the earlier instant; sorting by CaptureTime.UTC() must order it
	// before the western one (-05:00) — the mis-sort a naive-local read produces.
	build := func(offset string) time.Time {
		exifTIFF := buildSubIFD([]tiffEntry{
			{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
			{tag: tagOffsetTimeOriginal, typ: typeASCII, count: 7, extEnd: ascii(offset)},
		})
		md, err := Read(bytes.NewReader(buildJPEG(buildTIFFWithSubIFDs(nil, exifTIFF, nil))))
		if err != nil {
			t.Fatalf("Read %s: %v", offset, err)
		}
		return md.CaptureTime
	}
	east := build("+09:00")
	west := build("-05:00")
	if !east.Before(west) {
		t.Errorf("east %v should sort before west %v (true instant order)", east, west)
	}
}

func TestReadEXIFBadOffsetTimeFallsBackToUTC(t *testing.T) {
	// A blank/placeholder offset ("   :  ") leaves CaptureTime as UTC wall-clock,
	// HasCaptureOffset false — the documented no-zone fallback.
	exifTIFF := buildSubIFD([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
		{tag: tagOffsetTimeOriginal, typ: typeASCII, count: 7, extEnd: ascii("   :  ")},
	})
	md, err := Read(bytes.NewReader(buildJPEG(buildTIFFWithSubIFDs(nil, exifTIFF, nil))))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md.HasCaptureOffset {
		t.Error("HasCaptureOffset = true, want false for placeholder offset")
	}
	if want := time.Date(2021, 7, 14, 9, 30, 15, 0, time.UTC); !md.CaptureTime.Equal(want) {
		t.Errorf("capture = %v, want %v (UTC wall-clock fallback)", md.CaptureTime, want)
	}
}

func TestParseOffsetTime(t *testing.T) {
	cases := []struct {
		in   string
		secs int
		ok   bool
	}{
		{"+02:00", 2 * 3600, true},
		{"-05:00", -5 * 3600, true},
		{"+05:30", 5*3600 + 30*60, true},
		{"+00:00", 0, true},
		{"+14:00", 14 * 3600, true},
		{"", 0, false},
		{"   :  ", 0, false},
		{"+2:00", 0, false},  // wrong width
		{"02:00", 0, false},  // no sign
		{"+15:00", 0, false}, // hour out of range
		{"+02:60", 0, false}, // minute out of range
		{"+0a:00", 0, false}, // non-digit
		{"+02-00", 0, false}, // wrong separator
	}
	for _, c := range cases {
		secs, ok := parseOffsetTime(c.in)
		if ok != c.ok || secs != c.secs {
			t.Errorf("parseOffsetTime(%q) = (%d,%v), want (%d,%v)", c.in, secs, ok, c.secs, c.ok)
		}
	}
}

func TestParseExifTruncatedDoesNotPanic(t *testing.T) {
	// A TIFF header claiming an IFD0 offset past the buffer, and a 1-byte
	// payload, must not panic — they yield a zero Metadata.
	for _, tiff := range [][]byte{
		{'I', 'I', 0x2A, 0x00, 0xFF, 0xFF, 0xFF, 0xFF}, // IFD0 offset far past end
		{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}, // empty buffer past header
		{0x00},
	} {
		_ = parseExif(tiff) // must not panic
	}
}

func TestRationalAtOutOfRange(t *testing.T) {
	for _, off := range []int{-1, 0, 1, 8} {
		if got := rationalAt([]byte{1, 2, 3}, binary.LittleEndian, off); got != 0 {
			t.Fatalf("rationalAt offset %d = %v, want 0", off, got)
		}
	}
}

// u16padded packs a SHORT value into the inline 4-byte field (value in the
// first 2 bytes, little-endian, rest zero).
func u16padded(v uint16) []byte {
	out := make([]byte, 4)
	binary.LittleEndian.PutUint16(out[:2], v)
	return out
}

// TestJpegExifSegmentTruncatedAPP1 drives jpegExifSegment directly (not via
// Read/parseExif) with an APP1 segment whose declared length claims more body
// bytes than the stream actually provides. io.ReadFull must hit EOF/
// ErrUnexpectedEOF while filling body, and the function must degrade to
// errNoExif rather than blocking or returning a partial payload.
func TestJpegExifSegmentTruncatedAPP1(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	declaredBody := append([]byte("Exif\x00\x00"), make([]byte, 100)...)
	length := len(declaredBody) + 2
	b.Write([]byte{0xFF, 0xE1, byte(length >> 8), byte(length)})
	b.Write(declaredBody[:10]) // far fewer bytes than declared

	_, err := jpegExifSegment(bytes.NewReader(b.Bytes()))
	if !errors.Is(err, errNoExif) {
		t.Errorf("err = %v, want errNoExif", err)
	}
}

// TestJpegExifSegmentZeroLengthBody drives an APP1 segment whose length field
// equals exactly 2 (the length field's own size), so bodyLen computes to 0.
// The zero-length body is too short to carry the 6-byte "Exif\x00\x00"
// identifier, so the segment must be treated as non-Exif and the scan must
// continue (here hitting EOI) rather than panicking on a body[0:6] slice of
// an empty buffer.
func TestJpegExifSegmentZeroLengthBody(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})             // SOI
	b.Write([]byte{0xFF, 0xE1, 0x00, 0x02}) // APP1, length=2 -> bodyLen=0
	b.Write([]byte{0xFF, 0xD9})             // EOI

	_, err := jpegExifSegment(bytes.NewReader(b.Bytes()))
	if !errors.Is(err, errNoExif) {
		t.Errorf("err = %v, want errNoExif", err)
	}
}

// TestJpegExifSegmentNonExifAPP1 drives an APP1 (0xFFE1) segment whose body is
// present and long enough but does not carry the "Exif\x00\x00" identifier
// (e.g. an XMP APP1 payload). jpegExifSegment must skip it as an ordinary
// segment and keep scanning rather than mistaking any APP1 for Exif.
func TestJpegExifSegmentNonExifAPP1(t *testing.T) {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	body := append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta/>")...)
	length := len(body) + 2
	b.Write([]byte{0xFF, 0xE1, byte(length >> 8), byte(length)})
	b.Write(body)
	b.Write([]byte{0xFF, 0xD9}) // EOI

	_, err := jpegExifSegment(bytes.NewReader(b.Bytes()))
	if !errors.Is(err, errNoExif) {
		t.Errorf("err = %v, want errNoExif", err)
	}
}

// TestJpegExifSegmentBoundaryLength drives a genuine Exif APP1 segment whose
// body is 65533 bytes — the largest bodyLen a real 16-bit JPEG segment-length
// field can ever produce (0xFFFF - 2 = 65533 < maxExifSegment's 1<<16), so
// this is the true upper boundary the maxExifSegment guard must accept. The
// TIFF payload itself is garbage past the 8-byte header, which is fine: this
// test asserts the segment is read through to completion (no rejection at the
// boundary), not that the TIFF decodes.
func TestJpegExifSegmentBoundaryLength(t *testing.T) {
	const maxBodyLen = 0xFFFF - 2  // largest bodyLen a 16-bit segment-length field can encode (length<=65535, bodyLen=length-2)
	const tiffLen = maxBodyLen - 6 // remainder after the 6-byte "Exif\x00\x00" identifier
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	app1 := append([]byte("Exif\x00\x00"), make([]byte, tiffLen)...)
	length := len(app1) + 2
	b.Write([]byte{0xFF, 0xE1, byte(length >> 8), byte(length)})
	b.Write(app1)
	b.Write([]byte{0xFF, 0xD9}) // EOI

	payload, err := jpegExifSegment(bytes.NewReader(b.Bytes()))
	if err != nil {
		t.Fatalf("jpegExifSegment: %v", err)
	}
	if len(payload) != tiffLen {
		t.Errorf("payload len = %d, want %d", len(payload), tiffLen)
	}
}

// TestParseExifIFDOffsetBoundary drives readIFD's offset guard at its exact
// boundary — the guard rewritten to the subtraction form so a crafted uint32
// offset near the int ceiling cannot wrap offset+2 past a naive check on
// 32-bit builds. An IFD0 offset with fewer than 2 bytes left cannot hold the
// entry count and must yield a zero Metadata; an in-range offset whose entry
// count claims more 12-byte entries than the buffer holds must stop at the
// table edge. Neither may panic (parseExif's "every offset is bounds-checked"
// contract).
func TestParseExifIFDOffsetBoundary(t *testing.T) {
	header := func(ifd0 uint32) []byte {
		var b bytes.Buffer
		b.Write([]byte{'I', 'I'})
		b.Write(u16(0x002A))
		b.Write(u32(ifd0))
		return b.Bytes()
	}
	// Offset len-1: one byte left, too short for the 2-byte entry count.
	if md := parseExif(header(7)); md != (Metadata{}) {
		t.Errorf("IFD0 offset at len-1: md = %+v, want zero", md)
	}
	// Offset len-2: the entry count is readable but no entry fits; the walk
	// must stop cleanly at the table edge.
	_ = parseExif(header(6))
	// A count claiming 65535 entries with none present must stop at the edge.
	if md := parseExif(append(header(8), u16(0xFFFF)...)); md != (Metadata{}) {
		t.Errorf("truncated entry table: md = %+v, want zero", md)
	}
}

// TestGPSValueOffsetPastBuffer drives gpsDMS and gpsTimeStamp with out-of-line
// offsets the 24-byte rational triple cannot fit within: one byte past the
// last in-range offset, at the buffer end, and the maximal uint32 values whose
// naive off+24 arithmetic would wrap on 32-bit builds. All must report !ok —
// never panic, and never fabricate a zeroed coordinate as ok.
func TestGPSValueOffsetPastBuffer(t *testing.T) {
	tiff := make([]byte, 32)
	bo := binary.LittleEndian
	for _, off := range []uint32{9, 32, 0xFFFFFFE8, 0xFFFFFFFF} {
		if v, ok := gpsDMS(tiff, bo, typeRational, 3, u32(off)); ok {
			t.Errorf("gpsDMS(off=%d) = (%v, true), want ok=false", off, v)
		}
		if _, _, _, ok := gpsTimeStamp(tiff, bo, typeRational, 3, u32(off)); ok {
			t.Errorf("gpsTimeStamp(off=%d) ok = true, want false", off)
		}
	}
	// The last in-range offset (len-24) still reads: a zeroed triple's
	// zero-denominator rationals fold to 0 degrees, ok=true.
	if _, ok := gpsDMS(tiff, bo, typeRational, 3, u32(8)); !ok {
		t.Error("gpsDMS(off=8) ok = false, want true at the last in-range offset")
	}
}

// TestAsciiValueOffsetPastBuffer drives asciiValue's out-of-line branch
// (comps > 4) with counts and offsets running past the buffer end: each must
// yield "" rather than slice past the payload. The in-range control asserts
// the branch still reads real data.
func TestAsciiValueOffsetPastBuffer(t *testing.T) {
	tiff := []byte("IIabcdefgh")
	bo := binary.LittleEndian
	// In order: a count running one byte past the 10-byte end, a start offset
	// at the end, and a huge crafted offset.
	for _, c := range []struct {
		off   uint32
		comps int
	}{
		{2, 10},
		{10, 5},
		{0xFFFFFFFF, 8},
	} {
		if got := asciiValue(tiff, bo, typeASCII, c.comps, u32(c.off)); got != "" {
			t.Errorf("asciiValue(off=%d, comps=%d) = %q, want empty", c.off, c.comps, got)
		}
	}
	if got := asciiValue(tiff, bo, typeASCII, 5, u32(2)); got != "abcde" {
		t.Errorf("asciiValue(off=2, comps=5) = %q, want abcde", got)
	}
}

// TestJpegExifSegmentGuardBoundary is a mutation-style sanity check for the
// `bodyLen > maxExifSegment` guard in jpegExifSegment. The JPEG
// segment-length field is 16 bits, so a real marker can never declare a
// bodyLen above 65533 (0xFFFF - 2) — strictly less than maxExifSegment
// (1<<16 = 65536). That means the guard's reject branch is unreachable via
// any well-formed marker stream: this is a documented finding, not something
// this test can exercise from the wire. What IS mutation-sensitive here is
// the boundary itself: this test asserts the guard's comparison constant has
// not regressed below the format's own ceiling. If a future edit changes
// `>` to `>=` or shrinks maxExifSegment below 65533, the largest legal
// segment (already covered end-to-end by TestJpegExifSegmentBoundaryLength)
// would start being wrongly rejected — catching that class of regression is
// this test's job.
func TestJpegExifSegmentGuardBoundary(t *testing.T) {
	const formatMaxBodyLen = 0xFFFF - 2 // largest bodyLen a 16-bit length field can encode
	if maxExifSegment < formatMaxBodyLen {
		t.Fatalf("maxExifSegment = %d, want >= %d (the format's own max body length), or the guard would reject every maximal legal Exif segment", maxExifSegment, formatMaxBodyLen)
	}
}

func TestReadContextBoundsAndCancelsMarkerScan(t *testing.T) {
	var excessive bytes.Buffer
	excessive.Write([]byte{0xFF, 0xD8})
	for i := 0; i <= maxJPEGMarkerCount; i++ {
		excessive.Write([]byte{0xFF, 0xE0, 0x00, 0x02})
	}
	if _, err := ReadContext(context.Background(), bytes.NewReader(excessive.Bytes())); !errors.Is(err, errMarkerScanLimit) {
		t.Fatalf("marker scan error = %v, want errMarkerScanLimit", err)
	}

	var oversized bytes.Buffer
	oversized.Write([]byte{0xFF, 0xD8})
	segmentBody := make([]byte, 0xFFFF-2)
	for oversized.Len() <= maxJPEGMetadataBytes {
		oversized.Write([]byte{0xFF, 0xE0, 0xFF, 0xFF})
		oversized.Write(segmentBody)
	}
	if _, err := ReadContext(context.Background(), bytes.NewReader(oversized.Bytes())); !errors.Is(err, errMarkerScanLimit) {
		t.Fatalf("metadata byte-limit error = %v, want errMarkerScanLimit", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadContext(ctx, bytes.NewReader(buildJPEG(nil))); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scan error = %v, want context.Canceled", err)
	}
}

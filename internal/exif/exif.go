// Package exif is a minimal, dependency-free reader for the handful of EXIF
// tags Beamfall's image Media pipeline needs: capture
// timestamp, camera make/model, pixel dimensions, orientation, and GPS
// coordinates. It is deliberately NOT a general EXIF library — it parses only
// the TIFF structure holding those tags and the specific tags below, then
// stops. That structure is found in a JPEG's APP1 (Exif) segment, at the start
// of a TIFF-based RAW (CR2, NEF, NRW, ARW, DNG, ORF, RW2, PEF, SRW, 3FR), in
// the embedded JPEG of a Fujifilm RAF, or in a Canon CR3's CMT boxes
// (container.go). Anything it cannot read (another container, a file with no
// Exif segment, a malformed IFD, an absent tag) yields a zero value for that
// field and never an error the caller must handle as a failure: missing
// metadata degrades to empty, exactly the scraper framework's "unresolved path
// is omitted, not an error" posture.
//
// Hand-rolled vs a dependency: the needed tag set is small and fixed, and a
// general EXIF dependency would pull a large surface (every maker-note dialect,
// every container) for six tags. A focused reader keeps the plugin free of
// vendored code and the parse path auditable against decode-bomb risk: it
// reads a bounded segment, never decodes pixels.
package exif

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
)

// Metadata is the generic, domain-agnostic image metadata read from a file's
// EXIF segment. Every field is optional — a still with no Exif segment, or a
// segment missing a given tag, leaves that field at its zero value. The names
// are media-shape names (capture, camera, dimensions, location), never domain
// vocabulary: a Photos plugin maps them onto its Media fields, but Core only
// ever sees generic image facts.
type Metadata struct {
	// CaptureTime is EXIF DateTimeOriginal (tag 0x9003), parsed from the
	// "YYYY:MM:DD HH:MM:SS" EXIF form, normalized so its instant is correct in
	// UTC while its Location carries the capture-site offset for display
	// Zero when absent or unparseable. The offset is resolved
	// in priority order: EXIF OffsetTimeOriginal (tag 0x9011) when present, else
	// a GPS-UTC-derived offset when the GPS date/time tags are present, else the
	// value is interpreted as UTC wall-clock (offset zero). HasCaptureOffset
	// reports whether a real zone offset (0x9011 or GPS) was applied.
	CaptureTime time.Time
	// HasCaptureOffset is true when CaptureTime's zone offset came from an
	// explicit source (OffsetTimeOriginal or GPS) rather than the UTC wall-clock
	// fallback. It lets a caller distinguish "photo taken at +02:00, known" from
	// "no zone info, assumed UTC" — the latter still sorts, but its offset is a
	// guess, not a fact.
	HasCaptureOffset bool
	// CameraMake / CameraModel are EXIF Make (0x010F) / Model (0x0110),
	// trimmed of trailing NULs. Empty when absent.
	CameraMake  string
	CameraModel string
	// Width / Height are the pixel dimensions from EXIF PixelXDimension
	// (0xA002) / PixelYDimension (0xA003). Zero when absent — these EXIF tags
	// are optional and many encoders omit them even when the image obviously
	// has dimensions; reading them from the actual image stream is the decoder's
	// job, not this metadata reader's.
	Width  int
	Height int
	// Orientation is the EXIF Orientation tag (0x0112): 1–8, where 1 is the
	// normal top-left orientation. Zero when absent (treat as 1).
	Orientation int
	// HasGPS reports whether a GPS latitude and longitude were both read. When
	// false, Latitude/Longitude are meaningless and must be ignored.
	HasGPS    bool
	Latitude  float64
	Longitude float64
}

// maxExifSegment caps the APP1 payload this reader will buffer. The JPEG APP1
// length field is a 16-bit value, so an Exif segment is at most ~64 KiB by the
// format itself; the cap is a defensive belt against a crafted length, never a
// real-file limit.
const (
	maxExifSegment       = 1 << 16
	maxJPEGMarkerCount   = 4096
	maxJPEGMetadataBytes = 8 << 20
)

// ReadFile reads the EXIF metadata from the file at path. A file that is not a
// readable JPEG with an Exif segment returns a zero Metadata and a nil error —
// "no EXIF" is the common case for PNG/GIF/screenshots and is not a failure.
// An I/O error opening the file is returned so the caller can log it.
func ReadFile(path string) (Metadata, error) {
	return ReadFileContext(context.Background(), path)
}

// ReadFileContext is ReadFile with cancellation checked throughout the marker
// scan so an abandoned import does not continue walking a hostile JPEG header.
func ReadFileContext(ctx context.Context, path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer func() { _ = f.Close() }()
	return ReadContext(ctx, f)
}

// Read reads EXIF metadata from an open reader positioned at the start of an
// image file. Like ReadFile, a stream with no recognizable Exif segment yields
// a zero Metadata and nil error.
func Read(r io.Reader) (Metadata, error) {
	return ReadContext(context.Background(), r)
}

// ReadContext is Read with cancellation propagated through the marker scan.
// A RAW container is recognized by its signature and parsed from a bounded
// prefix; anything else takes the JPEG marker scan.
func ReadContext(ctx context.Context, r io.Reader) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	head, stream := peekSignature(r)
	if parse := containerParser(head); parse != nil {
		return readContainer(stream, parse), nil
	}
	payload, err := jpegExifSegmentContext(ctx, stream)
	if err != nil {
		if errors.Is(err, errNoExif) {
			return Metadata{}, nil
		}
		return Metadata{}, err
	}
	return parseExif(payload), nil
}

var (
	errNoExif          = errors.New("exif: no Exif segment")
	errMarkerScanLimit = errors.New("exif: JPEG metadata scan limit exceeded")
)

// jpegExifSegment scans JPEG markers for the APP1 (0xFFE1) segment carrying the
// "Exif\x00\x00" identifier and returns its TIFF payload (the bytes after the
// identifier). It reads marker-by-marker rather than the whole file, skipping
// other segments' bodies by their length, and stops at the start of scan data
// (SOS, 0xFFDA) — EXIF always precedes the image data.
func jpegExifSegment(r io.Reader) ([]byte, error) {
	return jpegExifSegmentContext(context.Background(), r)
}

func jpegExifSegmentContext(ctx context.Context, r io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil {
		return nil, errNoExif
	}
	if soi[0] != 0xFF || soi[1] != 0xD8 { // not a JPEG SOI
		return nil, errNoExif
	}
	var hdr [4]byte
	bytesScanned := 2
	for markerCount := 0; markerCount < maxJPEGMarkerCount; markerCount++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Each marker is 0xFF then a marker byte. Standalone 0xFF padding
		// bytes are skipped.
		if _, err := io.ReadFull(r, hdr[:2]); err != nil {
			return nil, errNoExif
		}
		bytesScanned += 2
		for hdr[0] != 0xFF {
			hdr[0] = hdr[1]
			if _, err := io.ReadFull(r, hdr[1:2]); err != nil {
				return nil, errNoExif
			}
			bytesScanned++
			if bytesScanned > maxJPEGMetadataBytes {
				return nil, errMarkerScanLimit
			}
		}
		for hdr[1] == 0xFF { // run of padding
			if _, err := io.ReadFull(r, hdr[1:2]); err != nil {
				return nil, errNoExif
			}
			bytesScanned++
			if bytesScanned > maxJPEGMetadataBytes {
				return nil, errMarkerScanLimit
			}
		}
		marker := hdr[1]
		if marker == 0xD9 || marker == 0xDA { // EOI or SOS: no more metadata
			return nil, errNoExif
		}
		// Read the 2-byte big-endian segment length (includes the length
		// bytes themselves).
		if _, err := io.ReadFull(r, hdr[2:4]); err != nil {
			return nil, errNoExif
		}
		bytesScanned += 2
		length := int(binary.BigEndian.Uint16(hdr[2:4]))
		if length < 2 {
			return nil, errNoExif
		}
		bodyLen := length - 2
		if bodyLen > maxExifSegment {
			return nil, errNoExif
		}
		if bodyLen > maxJPEGMetadataBytes-bytesScanned {
			return nil, errMarkerScanLimit
		}
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, errNoExif
		}
		bytesScanned += bodyLen
		if marker == 0xE1 && len(body) >= 6 &&
			body[0] == 'E' && body[1] == 'x' && body[2] == 'i' && body[3] == 'f' &&
			body[4] == 0x00 && body[5] == 0x00 {
			return body[6:], nil
		}
		// Any other segment (APP0/JFIF, quantization tables, …): already
		// consumed its body above, loop to the next marker.
	}
	return nil, errMarkerScanLimit
}

// EXIF tag IDs this reader extracts.
const (
	tagMake               = 0x010F
	tagModel              = 0x0110
	tagOrientation        = 0x0112
	tagExifIFDPointer     = 0x8769
	tagGPSIFDPointer      = 0x8825
	tagDateTimeOriginal   = 0x9003
	tagOffsetTimeOriginal = 0x9011
	tagPixelXDimension    = 0xA002
	tagPixelYDimension    = 0xA003

	gpsTagLatitudeRef  = 0x0001
	gpsTagLatitude     = 0x0002
	gpsTagLongitudeRef = 0x0003
	gpsTagLongitude    = 0x0004
	gpsTagTimeStamp    = 0x0007
	gpsTagDateStamp    = 0x001D
)

// TIFF field types (the subset used by the tags above).
const (
	typeASCII    uint16 = 2
	typeShort    uint16 = 3
	typeLong     uint16 = 4
	typeRational uint16 = 5
)

// parseExif walks the TIFF structure in a buffered Exif payload and fills the
// fields it recognizes. A malformed structure stops the walk and returns
// whatever was read so far — never a panic (every offset is bounds-checked).
func parseExif(tiff []byte) Metadata {
	var md Metadata
	walkTIFF(tiff, &md)
	return md
}

// walkTIFF reads one TIFF structure's IFD0, then its Exif and GPS sub-IFDs,
// into md. A structure without a valid header leaves md untouched.
func walkTIFF(tiff []byte, md *Metadata) {
	bo, ifd0, ok := tiffHeader(tiff)
	if !ok {
		return
	}
	exifIFD, gpsIFD := readIFD(tiff, bo, ifd0, md)
	if exifIFD > 0 {
		readIFD(tiff, bo, exifIFD, md)
	}
	if gpsIFD > 0 {
		readGPSIFD(tiff, bo, gpsIFD, md)
	}
}

// tiffMagics are the 16-bit values accepted after the byte-order mark: 42 for
// TIFF and the TIFF-based RAWs (CR2, NEF, NRW, ARW, DNG, PEF, SRW, 3FR), plus
// the Olympus ORF ("RO", "RS") and Panasonic RW2 ("U\x00") variants, whose IFDs
// are standard TIFF behind a different magic.
var tiffMagics = map[uint16]bool{0x002A: true, 0x4F52: true, 0x5352: true, 0x0055: true}

// tiffByteOrders maps the TIFF byte-order mark to its decoding.
var tiffByteOrders = map[string]binary.ByteOrder{"II": binary.LittleEndian, "MM": binary.BigEndian}

// tiffHeader reads a TIFF header's byte order and IFD0 offset; ok is false
// when the buffer does not open with a recognized TIFF header.
func tiffHeader(tiff []byte) (bo binary.ByteOrder, ifd0 int, ok bool) {
	if len(tiff) < 8 {
		return nil, 0, false
	}
	bo = tiffByteOrders[string(tiff[:2])]
	if bo == nil {
		return nil, 0, false
	}
	if !tiffMagics[bo.Uint16(tiff[2:4])] {
		return nil, 0, false
	}
	return bo, int(bo.Uint32(tiff[4:8])), true
}

// readIFD walks one image file directory, filling md from the recognized tags
// and returning the offsets of the Exif and GPS sub-IFDs if their pointer tags
// are present (0 when absent). It deliberately ignores the next-IFD link: only
// IFD0 is followed, and only for its sub-IFD pointers.
func readIFD(tiff []byte, bo binary.ByteOrder, offset int, md *Metadata) (exifIFD, gpsIFD int) {
	// Subtraction form: offset+2 could wrap past a naive check on 32-bit
	// builds where the attacker-controlled uint32 offset nears the int ceiling
	// (same overflow posture as asciiValue/rationalAt).
	if offset < 0 || offset > len(tiff)-2 {
		return 0, 0
	}
	count := int(bo.Uint16(tiff[offset : offset+2]))
	entry := offset + 2
	// DateTimeOriginal and OffsetTimeOriginal may appear in either entry order
	// within one IFD, so collect the offset across this walk and combine after
	// the loop. Across IFDs the offset applies only when its IFD is walked in
	// or after the timestamp's (IFD0 before the Exif sub-IFD); in practice both
	// tags are Exif-sub-IFD tags, so one walk sees both.
	var offsetOriginal string
	for i := 0; i < count; i++ {
		if entry+12 > len(tiff) {
			break
		}
		tag := bo.Uint16(tiff[entry : entry+2])
		typ := bo.Uint16(tiff[entry+2 : entry+4])
		comps := int(bo.Uint32(tiff[entry+4 : entry+8]))
		valueField := tiff[entry+8 : entry+12]
		switch tag {
		case tagMake:
			md.CameraMake = asciiValue(tiff, bo, typ, comps, valueField)
		case tagModel:
			md.CameraModel = asciiValue(tiff, bo, typ, comps, valueField)
		case tagOrientation:
			if v, ok := shortValue(bo, typ, valueField); ok {
				md.Orientation = int(v)
			}
		case tagPixelXDimension:
			if v, ok := longOrShort(bo, typ, valueField); ok {
				md.Width = int(v)
			}
		case tagPixelYDimension:
			if v, ok := longOrShort(bo, typ, valueField); ok {
				md.Height = int(v)
			}
		case tagDateTimeOriginal:
			md.CaptureTime = parseExifTime(asciiValue(tiff, bo, typ, comps, valueField))
		case tagOffsetTimeOriginal:
			offsetOriginal = asciiValue(tiff, bo, typ, comps, valueField)
		case tagExifIFDPointer:
			exifIFD = int(bo.Uint32(valueField))
		case tagGPSIFDPointer:
			gpsIFD = int(bo.Uint32(valueField))
		}
		entry += 12
	}
	applyOffsetOriginal(md, offsetOriginal)
	return exifIFD, gpsIFD
}

// applyOffsetOriginal re-anchors a UTC-wall-clock CaptureTime onto the explicit
// zone offset from EXIF OffsetTimeOriginal (tag 0x9011), the authoritative offset
// source. The wall-clock reading is the *local* capture time, so the same
// wall-clock digits in the offset's zone name a different instant: e.g.
// "2021:07:14 09:30:15" with "+02:00" is 07:30:15 UTC. Absent or unparseable
// offset leaves CaptureTime as the UTC-wall-clock fallback for GPS or the caller
// to refine.
func applyOffsetOriginal(md *Metadata, offset string) {
	if md.CaptureTime.IsZero() || md.HasCaptureOffset {
		return
	}
	secs, ok := parseOffsetTime(offset)
	if !ok {
		return
	}
	md.CaptureTime = time.Date(
		md.CaptureTime.Year(), md.CaptureTime.Month(), md.CaptureTime.Day(),
		md.CaptureTime.Hour(), md.CaptureTime.Minute(), md.CaptureTime.Second(),
		md.CaptureTime.Nanosecond(), time.FixedZone("", secs),
	)
	md.HasCaptureOffset = true
}

// parseOffsetTime parses an EXIF OffsetTime value ("+HH:MM" / "-HH:MM") to a
// signed second count. The blank "   :  " placeholder some encoders write, an
// empty string, or any malformed form yields ok=false.
func parseOffsetTime(s string) (int, bool) {
	if len(s) != 6 || (s[0] != '+' && s[0] != '-') || s[3] != ':' {
		return 0, false
	}
	hh, hok := twoDigit(s[1], s[2])
	mm, mok := twoDigit(s[4], s[5])
	if !hok || !mok || hh > 14 || mm > 59 {
		return 0, false
	}
	secs := hh*3600 + mm*60
	if s[0] == '-' {
		secs = -secs
	}
	return secs, true
}

func twoDigit(hi, lo byte) (int, bool) {
	if hi < '0' || hi > '9' || lo < '0' || lo > '9' {
		return 0, false
	}
	return int(hi-'0')*10 + int(lo-'0'), true
}

// readGPSIFD reads the GPS sub-IFD's latitude/longitude and UTC timestamp.
// Coordinates need both a value and reference, or HasGPS stays false.
func readGPSIFD(tiff []byte, bo binary.ByteOrder, offset int, md *Metadata) {
	// Subtraction form avoids offset+2 int overflow on 32-bit (see readIFD).
	if offset < 0 || offset > len(tiff)-2 {
		return
	}
	count := int(bo.Uint16(tiff[offset : offset+2]))
	entry := offset + 2
	var lat, lon float64
	var latOK, lonOK bool
	var latRef, lonRef string
	var gpsHour, gpsMin, gpsSec int
	var gpsTimeOK bool
	var gpsDate string
	for i := 0; i < count; i++ {
		if entry+12 > len(tiff) {
			break
		}
		tag := bo.Uint16(tiff[entry : entry+2])
		typ := bo.Uint16(tiff[entry+2 : entry+4])
		comps := int(bo.Uint32(tiff[entry+4 : entry+8]))
		valueField := tiff[entry+8 : entry+12]
		switch tag {
		case gpsTagLatitude:
			lat, latOK = gpsDMS(tiff, bo, typ, comps, valueField)
		case gpsTagLongitude:
			lon, lonOK = gpsDMS(tiff, bo, typ, comps, valueField)
		case gpsTagLatitudeRef:
			latRef = asciiValue(tiff, bo, typ, comps, valueField)
		case gpsTagLongitudeRef:
			lonRef = asciiValue(tiff, bo, typ, comps, valueField)
		case gpsTagTimeStamp:
			gpsHour, gpsMin, gpsSec, gpsTimeOK = gpsTimeStamp(tiff, bo, typ, comps, valueField)
		case gpsTagDateStamp:
			gpsDate = asciiValue(tiff, bo, typ, comps, valueField)
		}
		entry += 12
	}
	// OffsetTimeOriginal (tag 0x9011) is authoritative; only derive an offset
	// from GPS UTC when no explicit offset was already applied.
	if !md.HasCaptureOffset && gpsTimeOK && gpsDate != "" && !md.CaptureTime.IsZero() {
		if gpsUTC, ok := parseGPSDateTime(gpsDate, gpsHour, gpsMin, gpsSec); ok {
			if t, ok := captureTimeWithGPSOffset(md.CaptureTime, gpsUTC); ok {
				md.CaptureTime = t
				md.HasCaptureOffset = true
			}
		}
	}
	if latOK && lonOK {
		if latRef == "S" {
			lat = -lat
		}
		if lonRef == "W" {
			lon = -lon
		}
		md.HasGPS = true
		md.Latitude = lat
		md.Longitude = lon
	}
}

// gpsDMS reads a 3-rational degrees/minutes/seconds GPS coordinate and folds
// it to decimal degrees. The value never fits the inline 4-byte field (three
// rationals = 24 bytes), so it always lives at the offset the field holds.
func gpsDMS(tiff []byte, bo binary.ByteOrder, typ uint16, comps int, valueField []byte) (float64, bool) {
	if typ != typeRational || comps != 3 {
		return 0, false
	}
	off := int(bo.Uint32(valueField))
	// Subtraction form avoids off+24 int overflow on 32-bit (see readIFD).
	if off < 0 || off > len(tiff)-24 {
		return 0, false
	}
	deg := rationalAt(tiff, bo, off)
	min := rationalAt(tiff, bo, off+8)
	sec := rationalAt(tiff, bo, off+16)
	return deg + min/60 + sec/3600, true
}

func gpsTimeStamp(tiff []byte, bo binary.ByteOrder, typ uint16, comps int, valueField []byte) (hour, min, sec int, ok bool) {
	if typ != typeRational || comps != 3 {
		return 0, 0, 0, false
	}
	off := int(bo.Uint32(valueField))
	// Subtraction form avoids off+24 int overflow on 32-bit (see readIFD).
	if off < 0 || off > len(tiff)-24 {
		return 0, 0, 0, false
	}
	hour = int(rationalAt(tiff, bo, off))
	min = int(rationalAt(tiff, bo, off+8))
	sec = int(rationalAt(tiff, bo, off+16))
	if hour > 23 || min > 59 || sec > 59 {
		return 0, 0, 0, false
	}
	return hour, min, sec, true
}

func rationalAt(tiff []byte, bo binary.ByteOrder, off int) float64 {
	if off < 0 || off > len(tiff) || len(tiff)-off < 8 {
		return 0
	}
	num := bo.Uint32(tiff[off : off+4])
	den := bo.Uint32(tiff[off+4 : off+8])
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// asciiValue reads an ASCII tag value. Short strings live inline in the 4-byte
// value field; longer ones at the offset it holds. Trailing NUL terminators are
// trimmed.
func asciiValue(tiff []byte, bo binary.ByteOrder, typ uint16, comps int, valueField []byte) string {
	if typ != typeASCII || comps <= 0 {
		return ""
	}
	var raw []byte
	if comps <= 4 {
		raw = valueField[:comps]
	} else {
		off := int(bo.Uint32(valueField))
		// uint-based check avoids int overflow on 32-bit builds where
		// off+comps could wrap and slip past a naive int bounds check.
		if off < 0 || uint(off)+uint(comps) > uint(len(tiff)) {
			return ""
		}
		raw = tiff[off : off+comps]
	}
	// Trim at the first NUL, then any trailing whitespace.
	end := len(raw)
	for i, b := range raw {
		if b == 0 {
			end = i
			break
		}
	}
	out := raw[:end]
	for len(out) > 0 && (out[len(out)-1] == ' ' || out[len(out)-1] == 0) {
		out = out[:len(out)-1]
	}
	return string(out)
}

// shortValue reads a SHORT tag from the inline value field.
func shortValue(bo binary.ByteOrder, typ uint16, valueField []byte) (uint16, bool) {
	if typ != typeShort {
		return 0, false
	}
	return bo.Uint16(valueField[:2]), true
}

// longOrShort reads a dimension tag stored as either SHORT or LONG inline.
func longOrShort(bo binary.ByteOrder, typ uint16, valueField []byte) (uint32, bool) {
	switch typ {
	case typeShort:
		return uint32(bo.Uint16(valueField[:2])), true
	case typeLong:
		return bo.Uint32(valueField), true
	default:
		return 0, false
	}
}

// parseExifTime parses the EXIF "YYYY:MM:DD HH:MM:SS" timestamp form. The tag
// itself carries no zone, so this reads it as UTC wall-clock — a provisional
// reading that applyOffsetOriginal (0x9011) or the GPS-UTC offset then re-anchors
// to the true zone. When neither offset source is present the
// UTC-wall-clock reading stands as the documented fallback. An unparseable or
// zero ("0000:00:00 00:00:00") value yields the zero Time.
func parseExifTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseGPSDateTime(date string, hour, min, sec int) (time.Time, bool) {
	d, err := time.ParseInLocation("2006:01:02", date, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hour, min, sec, 0, time.UTC), true
}

func captureTimeWithGPSOffset(captureUTCWall, gpsUTC time.Time) (time.Time, bool) {
	offset := int(captureUTCWall.Sub(gpsUTC).Seconds())
	const maxZoneOffset = 14 * 60 * 60
	if offset < -maxZoneOffset || offset > maxZoneOffset {
		return time.Time{}, false
	}
	return time.Date(
		captureUTCWall.Year(), captureUTCWall.Month(), captureUTCWall.Day(),
		captureUTCWall.Hour(), captureUTCWall.Minute(), captureUTCWall.Second(),
		captureUTCWall.Nanosecond(), time.FixedZone("", offset),
	), true
}

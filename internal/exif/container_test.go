package exif

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// canonUUIDForTest is the Canon metadata uuid box type that carries a CR3's
// CMT1..CMT4 TIFF payloads. Spelled out here, independent of the reader, so
// the fixture encodes the format rather than the implementation.
var canonUUIDForTest = []byte{
	0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0,
	0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48,
}

var rawCaptureTime = time.Date(2021, 7, 14, 9, 30, 15, 0, time.UTC)

// rawLittleEndianTIFF is the Intel-order ("II") TIFF structure most RAW
// formats (CR2, ARW, DNG, SRW, 3FR) open with: IFD0 carries Make and
// Orientation, its Exif sub-IFD carries DateTimeOriginal.
func rawLittleEndianTIFF() []byte {
	return buildTIFFWithSubIFDs(
		[]tiffEntry{
			{tag: tagMake, typ: typeASCII, count: 6, extEnd: ascii("Canon")},
			{tag: tagOrientation, typ: typeShort, count: 1, value: u16padded(6)},
		},
		[]tiffEntry{
			{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
		},
		nil,
	)
}

// rawBigEndianTIFF lays out a Motorola-order ("MM") TIFF, the byte order NEF,
// NRW and PEF files use: IFD0 with Orientation inline and DateTimeOriginal out
// of line right after the entry table.
func rawBigEndianTIFF() []byte {
	be := binary.BigEndian
	const entries = 2
	dataOffset := 8 + 2 + entries*12 + 4
	b := []byte{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08}
	b = be.AppendUint16(b, entries)
	b = be.AppendUint16(b, tagOrientation)
	b = be.AppendUint16(b, typeShort)
	b = be.AppendUint32(b, 1)
	b = be.AppendUint32(b, 6<<16) // SHORT 6 in the first two bytes of the field
	b = be.AppendUint16(b, tagDateTimeOriginal)
	b = be.AppendUint16(b, typeASCII)
	b = be.AppendUint32(b, 20)
	b = be.AppendUint32(b, uint32(dataOffset))
	b = be.AppendUint32(b, 0) // no next IFD
	return append(b, ascii("2021:07:14 09:30:15")...)
}

// withTIFFMagic rewrites the 16-bit TIFF magic of a little-endian TIFF: ORF
// files carry "RO"/"RS" and RW2 files "U\x00" where plain TIFF carries 42.
func withTIFFMagic(tiff []byte, magic uint16) []byte {
	out := bytes.Clone(tiff)
	binary.LittleEndian.PutUint16(out[2:4], magic)
	return out
}

// buildRAF wraps an embedded JPEG in a Fujifilm RAF header: the big-endian
// JPEG offset and length sit at bytes 84 and 88.
func buildRAF(jpeg []byte) []byte {
	header := make([]byte, 0x94)
	copy(header, "FUJIFILMCCD-RAW 0201FF383501")
	binary.BigEndian.PutUint32(header[84:88], uint32(len(header)))
	binary.BigEndian.PutUint32(header[88:92], uint32(len(jpeg)))
	return append(header, jpeg...)
}

// isoBox encodes one ISOBMFF box with a 32-bit size header.
func isoBox(kind string, body ...[]byte) []byte {
	payload := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	out = append(out, kind...)
	return append(out, payload...)
}

// cr3Metadata builds the Canon uuid box a CR3's moov opens with: CMT1 holds
// IFD0 (camera + orientation), CMT2 the Exif IFD, CMT4 the GPS IFD — each a
// complete TIFF structure of its own.
func cr3Metadata() []byte {
	cmt1 := buildTIFFWithSubIFDs([]tiffEntry{
		{tag: tagMake, typ: typeASCII, count: 6, extEnd: ascii("Canon")},
		{tag: tagModel, typ: typeASCII, count: 7, extEnd: ascii("EOS R5")},
		{tag: tagOrientation, typ: typeShort, count: 1, value: u16padded(6)},
	}, nil, nil)
	cmt2 := buildTIFFWithSubIFDs([]tiffEntry{
		{tag: tagDateTimeOriginal, typ: typeASCII, count: 20, extEnd: ascii("2021:07:14 09:30:15")},
	}, nil, nil)
	cmt4 := buildTIFFWithSubIFDs([]tiffEntry{
		{tag: gpsTagLatitudeRef, typ: typeASCII, count: 2, value: pad4(ascii("N"))},
		{tag: gpsTagLatitude, typ: typeRational, count: 3, extEnd: rational3(48, 51, 24)},
		{tag: gpsTagLongitudeRef, typ: typeASCII, count: 2, value: pad4(ascii("E"))},
		{tag: gpsTagLongitude, typ: typeRational, count: 3, extEnd: rational3(2, 21, 3)},
	}, nil, nil)
	return isoBox("uuid", canonUUIDForTest,
		isoBox("CNCV", []byte("CanonCR3_001/00.09.00/00.00.00")),
		isoBox("CMT1", cmt1),
		isoBox("CMT2", cmt2),
		isoBox("CMT4", cmt4),
	)
}

func buildCR3() []byte {
	return bytes.Join([][]byte{
		isoBox("ftyp", []byte("crx "), []byte{0, 0, 0, 1}, []byte("crx isom")),
		isoBox("moov", cr3Metadata()),
		isoBox("mdat", make([]byte, 64)),
	}, nil)
}

// TestReadFileRAWContainers checks that every RAW extension core scans must yield its orientation (the value core's
// derivative pipeline rotates by) and capture time, not the JPEG-only zero.
func TestReadFileRAWContainers(t *testing.T) {
	cases := []struct {
		name string
		ext  string
		data []byte
	}{
		{"cr2", "cr2", rawLittleEndianTIFF()},
		{"nef", "nef", rawBigEndianTIFF()},
		{"arw", "arw", rawLittleEndianTIFF()},
		{"dng", "dng", rawLittleEndianTIFF()},
		{"orf-IIRO", "orf", withTIFFMagic(rawLittleEndianTIFF(), 0x4F52)},
		{"orf-IIRS", "orf", withTIFFMagic(rawLittleEndianTIFF(), 0x5352)},
		{"rw2", "rw2", withTIFFMagic(rawLittleEndianTIFF(), 0x0055)},
		{"pef", "pef", rawBigEndianTIFF()},
		{"srw", "srw", rawLittleEndianTIFF()},
		{"3fr", "3fr", rawLittleEndianTIFF()},
		{"nrw", "nrw", rawBigEndianTIFF()},
		{"raf", "raf", buildRAF(buildJPEG(rawLittleEndianTIFF()))},
		{"cr3", "cr3", buildCR3()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample."+c.ext)
			if err := os.WriteFile(path, c.data, 0o600); err != nil {
				t.Fatal(err)
			}
			md, err := ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if md.Orientation != 6 {
				t.Errorf("orientation = %d, want 6", md.Orientation)
			}
			if !md.CaptureTime.Equal(rawCaptureTime) {
				t.Errorf("capture = %v, want %v", md.CaptureTime, rawCaptureTime)
			}
		})
	}
}

// TestReadCR3ReadsEveryCanonMetadataBox asserts the CR3 path merges all three
// TIFF payloads: camera from CMT1, capture time from CMT2, GPS from CMT4.
func TestReadCR3ReadsEveryCanonMetadataBox(t *testing.T) {
	md, err := Read(bytes.NewReader(buildCR3()))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md.CameraMake != "Canon" || md.CameraModel != "EOS R5" {
		t.Errorf("camera = %q %q, want Canon EOS R5", md.CameraMake, md.CameraModel)
	}
	if !md.CaptureTime.Equal(rawCaptureTime) {
		t.Errorf("capture = %v, want %v", md.CaptureTime, rawCaptureTime)
	}
	if !md.HasGPS {
		t.Fatal("HasGPS = false, want the CMT4 GPS IFD read")
	}
	if d := md.Latitude - 48.8567; d > 0.001 || d < -0.001 {
		t.Errorf("lat = %v, want ~48.8567", md.Latitude)
	}

	// TestReadFileRAWContainers exercises ReadFile for cr3 but only checks
	// orientation and capture time; confirm the camera fields asserted above
	// via Read also reach callers that go through the file-path entry point.
	path := filepath.Join(t.TempDir(), "sample.cr3")
	if err := os.WriteFile(path, buildCR3(), 0o600); err != nil {
		t.Fatal(err)
	}
	fileMD, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if fileMD.CameraMake != "Canon" || fileMD.CameraModel != "EOS R5" {
		t.Errorf("ReadFile camera = %q %q, want Canon EOS R5", fileMD.CameraMake, fileMD.CameraModel)
	}
}

// countingReader records how many bytes the reader under test consumed.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// TestReadContainerPrefixIsBounded places a valid IFD0 (orientation 6) past
// the bounded prefix: the reader must stop at the bound and degrade to zero
// metadata instead of reading the whole RAW to reach it.
func TestReadContainerPrefixIsBounded(t *testing.T) {
	const farIFD = 2 * maxContainerMetadataBytes
	data := make([]byte, farIFD, farIFD+18)
	copy(data, "II\x2A\x00")
	binary.LittleEndian.PutUint32(data[4:8], farIFD)
	data = binary.LittleEndian.AppendUint16(data, 1)
	data = binary.LittleEndian.AppendUint16(data, tagOrientation)
	data = binary.LittleEndian.AppendUint16(data, typeShort)
	data = binary.LittleEndian.AppendUint32(data, 1)
	data = binary.LittleEndian.AppendUint32(data, 6)
	data = binary.LittleEndian.AppendUint32(data, 0)

	src := &countingReader{r: bytes.NewReader(data)}
	md, err := Read(src)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md != (Metadata{}) {
		t.Errorf("md = %+v, want zero: the IFD lies past the bounded prefix", md)
	}
	if src.read > maxContainerMetadataBytes {
		t.Errorf("consumed %d bytes, want at most %d", src.read, maxContainerMetadataBytes)
	}
}

// TestReadContainerDegradesToZero: a recognized container whose structure is
// truncated, points outside the prefix or lacks the metadata box yields zero
// metadata and no error, the same posture as a JPEG with no Exif segment.
func TestReadContainerDegradesToZero(t *testing.T) {
	farJPEG := buildRAF(buildJPEG(rawLittleEndianTIFF()))
	binary.BigEndian.PutUint32(farJPEG[84:88], uint32(len(farJPEG)+64))
	otherUUID := bytes.Repeat([]byte{0x11}, 16)
	cases := map[string][]byte{
		"tiff-ifd0-past-end":    {'I', 'I', 0x2A, 0x00, 0xFF, 0xFF, 0xFF, 0x7F},
		"raf-truncated-header":  append(bytes.Clone(rafMagic), "0201"...),
		"raf-jpeg-past-end":     farJPEG,
		"raf-embedded-not-jpeg": buildRAF([]byte("not a jpeg preview")),
		"cr3-no-canon-uuid": bytes.Join([][]byte{
			isoBox("ftyp", []byte("crx ")),
			isoBox("moov", isoBox("uuid", otherUUID, isoBox("CMT1", rawLittleEndianTIFF()))),
		}, nil),
		"cr3-box-smaller-than-header": append(isoBox("ftyp", []byte("crx ")), 0, 0, 0, 4, 'm', 'o', 'o', 'v'),
		"cr3-largesize-truncated":     append(isoBox("ftyp", []byte("crx ")), 0, 0, 0, 1, 'm', 'o', 'o', 'v', 0),
	}
	for name, data := range cases {
		md, err := Read(bytes.NewReader(data))
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
		if md != (Metadata{}) {
			t.Errorf("%s: md = %+v, want zero", name, md)
		}
	}
}

// TestReadCR3BoxSizeForms covers the two ISOBMFF size escapes: a moov declared
// with a 64-bit largesize and a trailing mdat whose size 0 runs to end of file.
func TestReadCR3BoxSizeForms(t *testing.T) {
	body := cr3Metadata()
	moov := binary.BigEndian.AppendUint32(nil, 1)
	moov = append(moov, "moov"...)
	moov = binary.BigEndian.AppendUint64(moov, uint64(16+len(body)))
	moov = append(moov, body...)
	mdat := append([]byte{0, 0, 0, 0}, "mdat"...)
	data := bytes.Join([][]byte{
		isoBox("ftyp", []byte("crx "), []byte{0, 0, 0, 1}),
		moov,
		append(mdat, make([]byte, 32)...),
	}, nil)

	md, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if md.Orientation != 6 {
		t.Errorf("orientation = %d, want 6 from a largesize moov", md.Orientation)
	}
	if !md.CaptureTime.Equal(rawCaptureTime) {
		t.Errorf("capture = %v, want %v", md.CaptureTime, rawCaptureTime)
	}
}

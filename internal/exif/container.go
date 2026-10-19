package exif

import (
	"bytes"
	"encoding/binary"
	"io"
)

// maxContainerMetadataBytes bounds how much of a RAW container is read. Every
// supported RAW keeps its metadata TIFF, embedded-JPEG Exif segment or Canon
// metadata box near the start of the file; a structure pointing past this
// prefix degrades to zero metadata instead of reading a 50 MB sensor dump.
const maxContainerMetadataBytes = 1 << 20

// signatureBytes is how much of the stream is peeked to pick a container.
const signatureBytes = 16

// containerFormat pairs a signature test on the stream's first bytes with the
// parser for that container's bounded prefix.
type containerFormat struct {
	matches func(head []byte) bool
	parse   func(prefix []byte) Metadata
}

// containerFormats is the RAW dispatch table. A stream matching none of them
// (JPEG, PNG, HEIC, anything else) takes the JPEG marker scan.
var containerFormats = []containerFormat{
	{matches: isTIFFContainer, parse: parseExif},
	{matches: isRAF, parse: parseRAF},
	{matches: isCR3, parse: parseCR3},
}

// peekSignature reads the stream's first bytes and returns them with a reader
// that replays them ahead of the rest of the stream.
func peekSignature(r io.Reader) (head []byte, stream io.Reader) {
	buf := make([]byte, signatureBytes)
	n, _ := io.ReadFull(r, buf)
	head = buf[:n]
	return head, io.MultiReader(bytes.NewReader(head), r)
}

// containerParser returns the parser for the RAW container head opens, or nil.
func containerParser(head []byte) func([]byte) Metadata {
	for _, format := range containerFormats {
		if format.matches(head) {
			return format.parse
		}
	}
	return nil
}

// readContainer parses a bounded prefix of a RAW container. A read failure
// degrades to zero metadata, like a JPEG with no readable Exif segment.
func readContainer(r io.Reader, parse func([]byte) Metadata) Metadata {
	prefix, err := io.ReadAll(io.LimitReader(r, maxContainerMetadataBytes))
	if err != nil {
		return Metadata{}
	}
	return parse(prefix)
}

// isTIFFContainer matches a TIFF header, which CR2, NEF, NRW, ARW, DNG, ORF,
// RW2, PEF, SRW and 3FR files all open with.
func isTIFFContainer(head []byte) bool {
	_, _, ok := tiffHeader(head)
	return ok
}

// rafMagic opens every Fujifilm RAF file.
var rafMagic = []byte("FUJIFILMCCD-RAW ")

func isRAF(head []byte) bool {
	return bytes.HasPrefix(head, rafMagic)
}

// cr3Signature is the ISOBMFF ftyp box type plus the Canon "crx " brand at
// bytes 4..12 of a CR3.
const cr3Signature = "ftypcrx "

func isCR3(head []byte) bool {
	return len(head) >= 12 && string(head[4:12]) == cr3Signature
}

const (
	rafJPEGOffsetAt = 84 // big-endian offset of the embedded JPEG
	rafHeaderLen    = 92 // through the embedded JPEG's length field
)

// parseRAF reads the Exif segment of the JPEG a Fujifilm RAF embeds; the RAF
// header itself carries no orientation or capture time.
func parseRAF(prefix []byte) Metadata {
	if len(prefix) < rafHeaderLen {
		return Metadata{}
	}
	offset := binary.BigEndian.Uint32(prefix[rafJPEGOffsetAt:])
	if uint64(offset) >= uint64(len(prefix)) {
		return Metadata{}
	}
	payload, err := jpegExifSegment(bytes.NewReader(prefix[offset:]))
	if err != nil {
		return Metadata{}
	}
	return parseExif(payload)
}

// canonMetadataUUID is the uuid box type inside a CR3's moov that holds the
// CMT1..CMT4 metadata boxes.
var canonMetadataUUID = []byte{
	0x85, 0xc0, 0xb6, 0x87, 0x82, 0x0f, 0x11, 0xe0,
	0x81, 0x11, 0xf4, 0xce, 0x46, 0x2b, 0x6a, 0x48,
}

// parseCR3 reads a Canon CR3's metadata boxes, each a complete TIFF structure:
// CMT1 holds IFD0 (camera, orientation), CMT2 the Exif IFD (capture time) and
// CMT4 the GPS IFD. CMT3 is the maker note and is not read.
func parseCR3(prefix []byte) Metadata {
	moov := isoChild(prefix, "moov")
	canon := canonMetadataBox(moov)
	var md Metadata
	walkTIFF(isoChild(canon, "CMT1"), &md)
	walkTIFF(isoChild(canon, "CMT2"), &md)
	walkGPSTIFF(isoChild(canon, "CMT4"), &md)
	return md
}

// walkGPSTIFF reads a TIFF structure whose IFD0 is a GPS IFD.
func walkGPSTIFF(tiff []byte, md *Metadata) {
	bo, ifd0, ok := tiffHeader(tiff)
	if !ok {
		return
	}
	readGPSIFD(tiff, bo, ifd0, md)
}

// canonMetadataBox returns the children of moov's Canon metadata uuid box.
func canonMetadataBox(moov []byte) []byte {
	for _, box := range isoBoxes(moov) {
		if box.kind == "uuid" && bytes.HasPrefix(box.body, canonMetadataUUID) {
			return box.body[len(canonMetadataUUID):]
		}
	}
	return nil
}

// isoChild returns the body of the first box of the given type in buf.
func isoChild(buf []byte, kind string) []byte {
	for _, box := range isoBoxes(buf) {
		if box.kind == kind {
			return box.body
		}
	}
	return nil
}

// isoEntry is one ISOBMFF box: its four-character type and its body.
type isoEntry struct {
	kind string
	body []byte
}

// isoBoxes splits buf into its sequence of top-level boxes, stopping at the
// first malformed header.
func isoBoxes(buf []byte) []isoEntry {
	var boxes []isoEntry
	for {
		kind, end, header, ok := isoBoxHeader(buf)
		if !ok {
			return boxes
		}
		boxes = append(boxes, isoEntry{kind: kind, body: buf[header:end]})
		buf = buf[end:]
	}
}

// isoBoxHeader decodes the box header at the start of buf: its type, where the
// box ends within buf and the header length. A box running past buf (past the
// bounded prefix) is clamped to it; a size smaller than its own header is
// malformed.
func isoBoxHeader(buf []byte) (kind string, end, header int, ok bool) {
	if len(buf) < 8 {
		return "", 0, 0, false
	}
	size, header := isoBoxSize(buf)
	if size < uint64(header) {
		return "", 0, 0, false
	}
	end = int(min(size, uint64(len(buf))))
	return string(buf[4:8]), end, header, true
}

// isoBoxSize reads a box's declared size: 0 means the box runs to the end of
// the file, 1 means a 64-bit size follows the type.
func isoBoxSize(buf []byte) (size uint64, header int) {
	size = uint64(binary.BigEndian.Uint32(buf[:4]))
	switch {
	case size == 0:
		return uint64(len(buf)), 8
	case size == 1 && len(buf) >= 16:
		return binary.BigEndian.Uint64(buf[8:16]), 16
	}
	return size, 8
}

package imagemeta

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"slices"
	"testing"

	"github.com/bep/imagemeta"
)

// TIFF field types used by the fixture.
const (
	tiffASCII    = 2
	tiffShort    = 3
	tiffLong     = 4
	tiffRational = 5
)

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

func ascii(s string) ifdEntry {
	return ifdEntry{typ: tiffASCII, count: uint32(len(s) + 1), value: append([]byte(s), 0)}
}

func short(v uint16) ifdEntry {
	return ifdEntry{typ: tiffShort, count: 1, value: binary.LittleEndian.AppendUint16(nil, v)}
}

func long(v uint32) ifdEntry {
	return ifdEntry{typ: tiffLong, count: 1, value: binary.LittleEndian.AppendUint32(nil, v)}
}

func rational(num, den uint32) ifdEntry {
	b := binary.LittleEndian.AppendUint32(nil, num)
	return ifdEntry{typ: tiffRational, count: 1, value: binary.LittleEndian.AppendUint32(b, den)}
}

func tagged(tag uint16, e ifdEntry) ifdEntry {
	e.tag = tag
	return e
}

// tiffIFD lays out one little-endian IFD at offset off; values longer than
// four bytes go into a data area right behind it.
func tiffIFD(off uint32, entries []ifdEntry) []byte {
	le := binary.LittleEndian
	dataOff := off + 2 + uint32(len(entries))*12 + 4
	var head, data []byte
	head = le.AppendUint16(head, uint16(len(entries)))
	for _, e := range entries {
		head = le.AppendUint16(head, e.tag)
		head = le.AppendUint16(head, e.typ)
		head = le.AppendUint32(head, e.count)
		if len(e.value) <= 4 {
			head = append(head, e.value...)
			head = append(head, make([]byte, 4-len(e.value))...)
			continue
		}
		head = le.AppendUint32(head, dataOff+uint32(len(data)))
		data = append(data, e.value...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	head = le.AppendUint32(head, 0)
	return append(head, data...)
}

// exifTIFF builds the TIFF body of an EXIF segment: camera and copyright in
// IFD0, exposure values in the Exif sub-IFD it points to.
func exifTIFF() []byte {
	ifd0 := func(exifOff uint32) []ifdEntry {
		return []ifdEntry{
			tagged(0x010F, ascii("FUJIFILM")),
			tagged(0x0110, ascii("X-T5")),
			tagged(0x8298, ascii("Jane Doe")),
			tagged(0x8769, long(exifOff)),
		}
	}
	exif := []ifdEntry{
		tagged(0x829A, rational(1, 200)), // ExposureTime
		tagged(0x829D, rational(28, 10)), // FNumber
		tagged(0x8827, short(400)),       // ISOSpeedRatings
		tagged(0x920A, rational(35, 1)),  // FocalLength
	}
	head := []byte("II*\x00\x08\x00\x00\x00")
	exifOff := uint32(len(head) + len(tiffIFD(8, ifd0(0))))
	out := append(head, tiffIFD(8, ifd0(exifOff))...)
	return append(out, tiffIFD(exifOff, exif)...)
}

const xmpPacket = `<x:xmpmeta xmlns:x="adobe:ns:meta/">
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description xmlns:dc="http://purl.org/dc/elements/1.1/">
<dc:subject><rdf:Bag><rdf:li>moss</rdf:li><rdf:li>forest</rdf:li></rdf:Bag></dc:subject>
</rdf:Description>
</rdf:RDF>
</x:xmpmeta>`

func app1(payload []byte) []byte {
	seg := []byte{0xFF, 0xE1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+2))
	return append(seg, payload...)
}

// makeJPEGWithMetadata encodes a small JPEG and splices an EXIF and an XMP
// APP1 segment in right after SOI, where cameras put them.
func makeJPEGWithMetadata(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	plain := buf.Bytes()
	out := append([]byte{}, plain[:2]...)
	out = append(out, app1(append([]byte("Exif\x00\x00"), exifTIFF()...))...)
	out = append(out, app1(append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmpPacket...))...)
	return append(out, plain[2:]...)
}

// TestExtractReadsCameraJPEG guards the path the autouploader depends on:
// upstream cannot detect formats itself, so without detectFormat every call
// failed and captions silently lost their EXIF and copyright lines.
func TestExtractReadsCameraJPEG(t *testing.T) {
	m, err := Extract(makeJPEGWithMetadata(t))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if m.Make != "FUJIFILM" || m.Model != "X-T5" {
		t.Errorf("camera = %q %q, want FUJIFILM X-T5", m.Make, m.Model)
	}
	if m.Copyright != "Jane Doe" {
		t.Errorf("Copyright = %q, want Jane Doe", m.Copyright)
	}
	if m.FNumber != 2.8 || m.ExposureSeconds != 0.005 || m.ISO != 400 || m.FocalLengthMM != 35 {
		t.Errorf("exposure = f/%v %vs ISO %d %vmm, want f/2.8 0.005s ISO 400 35mm",
			m.FNumber, m.ExposureSeconds, m.ISO, m.FocalLengthMM)
	}
	if !slices.Equal(m.Keywords, []string{"moss", "forest"}) {
		t.Errorf("Keywords = %q, want [moss forest]", m.Keywords)
	}
}

func TestExtractAcceptsImagesWithoutMetadata(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	m, err := Extract(buf.Bytes())
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if m.Make != "" || len(m.Keywords) != 0 {
		t.Errorf("got metadata from a bare PNG: %+v", m)
	}
}

func TestExtractRejectsUnknownFormats(t *testing.T) {
	m, err := Extract([]byte("GIF89a not something we read"))
	if err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
	if m == nil {
		t.Fatal("callers rely on a non-nil Metadata alongside the error")
	}
}

func TestDetectFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		head   string
		want   imagemeta.ImageFormat
		wantOK bool
	}{
		{"jpeg", "\xFF\xD8\xFF\xE1", imagemeta.JPEG, true},
		{"png", "\x89PNG\r\n\x1a\n", imagemeta.PNG, true},
		{"webp", "RIFF\x00\x00\x00\x00WEBPVP8 ", imagemeta.WebP, true},
		{"tiff little endian", "II*\x00\x08\x00\x00\x00", imagemeta.TIFF, true},
		{"tiff big endian", "MM\x00*\x00\x00\x00\x08", imagemeta.TIFF, true},
		{"heic", "\x00\x00\x00\x18ftypheic", imagemeta.HEIF, true},
		{"heif mif1", "\x00\x00\x00\x18ftypmif1", imagemeta.HEIF, true},
		{"avif", "\x00\x00\x00\x1cftypavif", imagemeta.AVIF, true},
		{"mp4 is not an image", "\x00\x00\x00\x18ftypisom", imagemeta.ImageFormatAuto, false},
		{"gif", "GIF89a", imagemeta.ImageFormatAuto, false},
		{"too short", "\xFF\xD8", imagemeta.ImageFormatAuto, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := detectFormat([]byte(tc.head))
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("detectFormat = %v, %v; want %v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

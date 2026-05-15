// Package imagemeta wraps github.com/bep/imagemeta and exposes only the
// fields the autouploader actually uses (camera, lens, exposure, copyright,
// keywords). The wrapper isolates the rest of the codebase from the upstream
// callback-driven API.
package imagemeta

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/bep/imagemeta"
)

// Metadata holds the normalised fields we care about. Zero-valued strings /
// numbers indicate "not present". Keywords contains both IPTC Keywords and
// XMP dc:subject entries (best-effort). All values are raw data — formatting
// for display (e.g. rendering ExposureSeconds as "1/200s") is the caller's
// concern.
type Metadata struct {
	Make            string
	Model           string
	Lens            string
	FNumber         float64
	ExposureSeconds float64
	ISO             int
	FocalLengthMM   float64
	Copyright       string
	Artist          string
	Keywords        []string
}

// Extract decodes EXIF, IPTC, and XMP tags from the image bytes. Errors from
// the underlying decoder are non-fatal — callers receive a (possibly empty)
// Metadata struct alongside the error so that "no useful metadata" is
// distinguishable from "image format unknown".
func Extract(image []byte) (*Metadata, error) {
	m := &Metadata{}
	if len(image) == 0 {
		return m, fmt.Errorf("imagemeta: empty image")
	}

	seen := make(map[string]bool)
	addKeyword := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		m.Keywords = append(m.Keywords, s)
	}

	opts := imagemeta.Options{
		R:           bytes.NewReader(image),
		ImageFormat: imagemeta.ImageFormatAuto,
		Sources:     imagemeta.EXIF | imagemeta.IPTC | imagemeta.XMP,
		HandleTag: func(t imagemeta.TagInfo) error {
			switch t.Tag {
			case "Make":
				m.Make = toString(t.Value)
			case "Model":
				m.Model = toString(t.Value)
			case "LensModel":
				if m.Lens == "" {
					m.Lens = toString(t.Value)
				}
			case "Lens":
				if m.Lens == "" {
					m.Lens = toString(t.Value)
				}
			case "FNumber":
				m.FNumber = toFloat(t.Value)
			case "ExposureTime":
				m.ExposureSeconds = toFloat(t.Value)
			case "ISOSpeedRatings", "ISO", "PhotographicSensitivity":
				if m.ISO == 0 {
					m.ISO = toInt(t.Value)
				}
			case "FocalLength":
				m.FocalLengthMM = toFloat(t.Value)
			case "Copyright", "CopyrightNotice":
				if m.Copyright == "" {
					m.Copyright = toString(t.Value)
				}
			case "Artist", "By-line":
				if m.Artist == "" {
					m.Artist = toString(t.Value)
				}
			case "Keywords":
				switch v := t.Value.(type) {
				case string:
					addKeyword(v)
				case []string:
					for _, s := range v {
						addKeyword(s)
					}
				}
			}
			return nil
		},
		HandleXMP: func(r io.Reader) error {
			parseXMPSubjects(r, addKeyword)
			return nil
		},
	}

	if _, err := imagemeta.Decode(opts); err != nil {
		return m, err
	}
	return m, nil
}

// --- value coercion helpers ---------------------------------------------------

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []byte:
		return strings.TrimSpace(string(x))
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case float32:
		return float64(x)
	case float64:
		return x
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	case interface{ Float64() float64 }:
		return x.Float64()
	default:
		return 0
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int8:
		return int(x)
	case int16:
		return int(x)
	case int32:
		return int(x)
	case int64:
		return int(x)
	case uint:
		return int(x)
	case uint8:
		return int(x)
	case uint16:
		return int(x)
	case uint32:
		return int(x)
	case uint64:
		return int(x)
	case []uint16:
		if len(x) > 0 {
			return int(x[0])
		}
		return 0
	case []int:
		if len(x) > 0 {
			return x[0]
		}
		return 0
	case float32:
		return int(x)
	case float64:
		return int(x)
	case interface{ Float64() float64 }:
		return int(x.Float64())
	default:
		return 0
	}
}


// --- XMP dc:subject parser ---------------------------------------------------

// parseXMPSubjects scans the XMP RDF/XML body for <dc:subject>/<rdf:Bag>/<rdf:li>
// items and yields each as a keyword. Robust against namespace prefixes other
// than dc/rdf.
func parseXMPSubjects(r io.Reader, yield func(string)) {
	dec := xml.NewDecoder(r)
	inSubject := false
	inLi := false
	var current strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "subject" {
				inSubject = true
			} else if inSubject && t.Name.Local == "li" {
				inLi = true
				current.Reset()
			}
		case xml.CharData:
			if inLi {
				current.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "li" && inLi {
				yield(current.String())
				inLi = false
			} else if t.Name.Local == "subject" {
				inSubject = false
			}
		}
	}
}

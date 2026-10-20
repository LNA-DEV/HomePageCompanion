package imageresize

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"testing"
)

// makeJPEG returns a JPEG-encoded image of the given dimensions and quality.
// Pixel content is pseudo-random noise so the encoder produces a realistic
// byte size (constant-color images compress to almost nothing and would
// mask resize/encode bugs).
func makeJPEG(t *testing.T, w, h, quality int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(int64(w*1_000_000 + h*1000 + quality)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(int64(w*1_000_000 + h*1000)))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func decodeDims(t *testing.T, b []byte) (int, int, string) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode output: %v", err)
	}
	r := img.Bounds()
	return r.Dx(), r.Dy(), format
}

func TestPrepareForTarget_ZeroLimitsIsPassthrough(t *testing.T) {
	in := makeJPEG(t, 4000, 3000, 95)
	out, err := PrepareForTarget(in, Limits{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("zero-Limits should be byte-identical passthrough; len(in)=%d len(out)=%d", len(in), len(out))
	}
}

func TestPrepareForTarget_SmallJPEGPassthrough(t *testing.T) {
	in := makeJPEG(t, 100, 100, 90)
	for _, p := range []string{"bluesky", "mastodon", "pixelfed"} {
		t.Run(p, func(t *testing.T) {
			lim := DefaultsForPlatform(p)
			out, err := PrepareForTarget(in, lim)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(in, out) {
				t.Fatalf("%s: small JPEG should be passthrough; len(in)=%d len(out)=%d", p, len(in), len(out))
			}
		})
	}
}

func TestPrepareForTarget_BlueskyResize(t *testing.T) {
	in := makeJPEG(t, 4000, 3000, 95)
	lim := DefaultsForPlatform("bluesky")
	out, err := PrepareForTarget(in, lim)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) > lim.MaxBytes {
		t.Fatalf("bluesky output %d bytes exceeds cap %d", len(out), lim.MaxBytes)
	}
	w, h, format := decodeDims(t, out)
	if format != "jpeg" {
		t.Fatalf("expected jpeg output, got %s", format)
	}
	longEdge := w
	if h > w {
		longEdge = h
	}
	if longEdge > lim.MaxLongEdge {
		t.Fatalf("bluesky long edge %d exceeds cap %d", longEdge, lim.MaxLongEdge)
	}
	// Aspect ratio preserved (4:3 = 1.333...)
	got := float64(w) / float64(h)
	if math.Abs(got-4.0/3.0) > 0.02 {
		t.Fatalf("aspect ratio drifted: got %.4f, expected ~1.3333", got)
	}
}

func TestPrepareForTarget_MastodonMediumPassthrough(t *testing.T) {
	// 1500×1000 noise JPEG at quality 90 is comfortably under both the
	// Mastodon byte cap (8 MB) and dimension cap (2048 px).
	in := makeJPEG(t, 1500, 1000, 90)
	lim := DefaultsForPlatform("mastodon")
	if len(in) > lim.MaxBytes {
		t.Fatalf("test setup: input %d already exceeds mastodon cap %d", len(in), lim.MaxBytes)
	}
	out, err := PrepareForTarget(in, lim)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("expected byte-identical passthrough; len(in)=%d len(out)=%d", len(in), len(out))
	}
}

func TestPrepareForTarget_MastodonResize(t *testing.T) {
	in := makeJPEG(t, 4000, 3000, 95)
	lim := DefaultsForPlatform("mastodon")
	out, err := PrepareForTarget(in, lim)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w, h, _ := decodeDims(t, out)
	longEdge := w
	if h > w {
		longEdge = h
	}
	if longEdge > lim.MaxLongEdge {
		t.Fatalf("mastodon long edge %d exceeds cap %d", longEdge, lim.MaxLongEdge)
	}
	if len(out) > lim.MaxBytes {
		t.Fatalf("mastodon output %d exceeds cap %d", len(out), lim.MaxBytes)
	}
}

func TestPrepareForTarget_UnknownPlatformPassthrough(t *testing.T) {
	in := makeJPEG(t, 4000, 3000, 95)
	out, err := PrepareForTarget(in, DefaultsForPlatform("nostr"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("unknown platform → expected byte-identical passthrough")
	}
}

func TestPrepareForTarget_PNGGetsReencoded(t *testing.T) {
	// PNG input that exceeds the bluesky long-edge cap → output must be
	// JPEG, must fit dimensions, must fit bytes.
	in := makePNG(t, 3000, 2250)
	lim := DefaultsForPlatform("bluesky")
	out, err := PrepareForTarget(in, lim)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	w, h, format := decodeDims(t, out)
	if format != "jpeg" {
		t.Fatalf("expected jpeg, got %s", format)
	}
	longEdge := w
	if h > w {
		longEdge = h
	}
	if longEdge > lim.MaxLongEdge {
		t.Fatalf("png→jpeg long edge %d exceeds cap %d", longEdge, lim.MaxLongEdge)
	}
	if len(out) > lim.MaxBytes {
		t.Fatalf("png→jpeg %d bytes exceeds cap %d", len(out), lim.MaxBytes)
	}
}

func TestPrepareForTarget_InvalidBytesReturnsErrorAndOriginal(t *testing.T) {
	in := []byte("not actually an image")
	out, err := PrepareForTarget(in, DefaultsForPlatform("bluesky"))
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	if !bytes.Equal(in, out) {
		t.Fatalf("on decode error, output should be the original input")
	}
}

func TestDefaultsForPlatform_KnownAndUnknown(t *testing.T) {
	cases := []struct {
		platform string
		zero     bool
	}{
		{"bluesky", false},
		{"mastodon", false},
		{"pixelfed", false},
		{"instagram", true}, // intentional: we don't downsize for instagram
		{"", true},
		{"nostr", true},
	}
	for _, tc := range cases {
		t.Run(tc.platform, func(t *testing.T) {
			lim := DefaultsForPlatform(tc.platform)
			isZero := lim.MaxBytes == 0 && lim.MaxLongEdge == 0
			if isZero != tc.zero {
				t.Fatalf("DefaultsForPlatform(%q) = %+v, wantZero=%v", tc.platform, lim, tc.zero)
			}
		})
	}
}

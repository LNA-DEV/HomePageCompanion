// Package imageresize prepares image bytes to fit a target platform's
// size and dimension caps while preserving as much quality as possible.
//
// The package is intentionally config-agnostic: callers compose a Limits
// value (often via DefaultsForPlatform plus per-target overrides) and pass
// it to PrepareForTarget. A zero-value Limits means passthrough — useful
// for platforms we don't know about and for callers that want to opt out.
package imageresize

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	// Register decoders for the formats we actually receive from RSS feeds.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
)

// Limits describes the caps a prepared image must satisfy.
//
// MaxBytes is a soft cap on the encoded byte length. MaxLongEdge is a hard
// cap on the longer of the two dimensions. Zero on either field disables
// that check; Limits{} (both zero) makes PrepareForTarget a no-op.
type Limits struct {
	MaxBytes    int
	MaxLongEdge int

	// ForceReencode requires a full decode + JPEG re-encode even when the
	// input already fits both caps. Used to guarantee metadata (EXIF/GPS) is
	// stripped from stored images. Zero value (false) preserves the
	// fast-path passthrough used by the publish targets.
	ForceReencode bool
}

// DefaultsForPlatform returns the baked-in defaults for the well-known
// platforms we publish to. Values are conservative: each MaxBytes leaves
// headroom under the platform's documented hard cap, and each MaxLongEdge
// is well above what those platforms render at. Unknown platform → zero
// Limits (passthrough).
func DefaultsForPlatform(platform string) Limits {
	switch platform {
	case "bluesky":
		// Hard cap on com.atproto.repo.uploadBlob is 1,000,000 bytes.
		return Limits{MaxBytes: 950_000, MaxLongEdge: 2000}
	case "mastodon":
		// Mastodon's default upstream limit is 16 MB; stricter instances
		// often enforce 8 MB. Pick 8 MB as the safer default.
		return Limits{MaxBytes: 8_000_000, MaxLongEdge: 2048}
	case "pixelfed":
		// Pixelfed instances commonly allow 15 MB; some allow 50 MB.
		return Limits{MaxBytes: 14_000_000, MaxLongEdge: 4096}
	case "threads":
		// Threads fetches images by URL (no bytes uploaded by us), so these
		// caps are documentary — they describe what Threads itself accepts
		// when it pulls the image: 8 MB JPEG/PNG/HEIC, max 1920 px long edge.
		return Limits{MaxBytes: 8_000_000, MaxLongEdge: 1920}
	}
	return Limits{}
}

// PrepareForTarget returns image bytes that satisfy lim while preserving as
// much quality as possible.
//
// Decision tree:
//   - lim == Limits{}              → return input unchanged.
//   - input is JPEG and already
//     fits both caps               → return input unchanged.
//   - otherwise                    → resample (if oversized) and JPEG-encode
//     at the highest quality that fits.
//
// On decode failure the function returns the original bytes and a non-nil
// error so the caller can choose to upload the original and let the
// platform produce the authoritative error.
func PrepareForTarget(input []byte, lim Limits) ([]byte, error) {
	if lim.MaxBytes == 0 && lim.MaxLongEdge == 0 && !lim.ForceReencode {
		return input, nil
	}

	img, format, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return input, fmt.Errorf("decode image: %w", err)
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	longEdge := w
	if h > w {
		longEdge = h
	}

	needsResize := lim.MaxLongEdge > 0 && longEdge > lim.MaxLongEdge
	needsReencode := lim.MaxBytes > 0 && len(input) > lim.MaxBytes

	if !needsResize && !needsReencode && !lim.ForceReencode && format == "jpeg" {
		return input, nil
	}

	if needsResize {
		img = resizeLongEdge(img, lim.MaxLongEdge)
	}

	return encodeJPEGFitting(img, lim.MaxBytes)
}

// resizeLongEdge returns a copy of src scaled so its longer edge equals
// maxLongEdge. Aspect ratio is preserved. CatmullRom is the best practical
// resampler available in pure Go for photographic downscale.
func resizeLongEdge(src image.Image, maxLongEdge int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}

	var nw, nh int
	if w >= h {
		nw = maxLongEdge
		nh = int(float64(h) * float64(maxLongEdge) / float64(w))
	} else {
		nh = maxLongEdge
		nw = int(float64(w) * float64(maxLongEdge) / float64(h))
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// encodeJPEGFitting encodes img as JPEG, stepping quality down from 95 in
// 5-point increments to a floor of 60 to fit maxBytes. If quality 60 at the
// current dimensions still overshoots, it shrinks the long edge by 20% and
// retries, up to 3 iterations. If maxBytes is zero, encodes once at q=95.
func encodeJPEGFitting(img image.Image, maxBytes int) ([]byte, error) {
	const (
		startQuality = 95
		minQuality   = 60
		qualityStep  = 5
		maxShrinks   = 3
	)

	encode := func(im image.Image, q int) ([]byte, error) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, im, &jpeg.Options{Quality: q}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	current := img
	for shrink := 0; shrink <= maxShrinks; shrink++ {
		var best []byte
		for q := startQuality; q >= minQuality; q -= qualityStep {
			out, err := encode(current, q)
			if err != nil {
				return nil, fmt.Errorf("jpeg encode: %w", err)
			}
			if maxBytes == 0 || len(out) <= maxBytes {
				return out, nil
			}
			// Keep the smallest as a fallback in case nothing fits.
			if best == nil || len(out) < len(best) {
				best = out
			}
		}
		if shrink == maxShrinks {
			// Last resort: return the smallest encoding we produced even
			// though it exceeds maxBytes. The platform will reject it and
			// the caller logs that error — better than silently sending
			// raw bytes that are even larger.
			return best, nil
		}
		// Shrink long edge by 20% and retry.
		b := current.Bounds()
		w, h := b.Dx(), b.Dy()
		longEdge := w
		if h > w {
			longEdge = h
		}
		newLong := longEdge * 4 / 5
		if newLong < 1 {
			newLong = 1
		}
		current = resizeLongEdge(current, newLong)
	}
	// Unreachable.
	return nil, fmt.Errorf("imageresize: no encoding produced")
}

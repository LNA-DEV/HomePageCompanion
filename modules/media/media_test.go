package media_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/LNA-DEV/HomePageCompanion/capability/media"
	"github.com/LNA-DEV/HomePageCompanion/companiontest"
	mediamod "github.com/LNA-DEV/HomePageCompanion/modules/media"
)

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 99, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

func TestResizeOverHTTPAndInProcess(t *testing.T) {
	h := companiontest.New(t, "modules:\n  media: {}\n")
	big := jpegOf(800, 400)
	w := h.Admin("POST", "/api/b/media/resize?maxLongEdge=200", big)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || cfg.Width != 200 || cfg.Height != 100 {
		t.Fatalf("resized %+v %v", cfg, err)
	}
	var m media.Image = companiontest.Instance[*mediamod.Images](h, "media:default")
	out, err := m.Resize(context.Background(), big, media.Limits{})
	if err != nil || !bytes.Equal(out, big) {
		t.Fatal("no limits must pass the image through")
	}
	if w := h.Admin("POST", "/api/b/media/exif", []byte("not an image")); w.Code != 422 {
		t.Fatalf("exif of garbage: %d", w.Code)
	}
}

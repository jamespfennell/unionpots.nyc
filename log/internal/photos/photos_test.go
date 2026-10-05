package photos

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"
)

// testJPEG makes a w×h JPEG with an EXIF block carrying the given
// orientation (and a fake GPS tag, to check it's stripped).
func testJPEG(t *testing.T, w, h int, orientation uint16) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	var body bytes.Buffer
	if err := jpeg.Encode(&body, img, nil); err != nil {
		t.Fatal(err)
	}
	raw := body.Bytes()
	if orientation == 0 {
		return raw
	}
	// APP1 Exif segment: big-endian TIFF with one IFD0 entry (Orientation)
	// plus a GPS IFD pointer tag, inserted right after SOI.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8,
		0, 2, // two entries
		0x01, 0x12, 0, 3, 0, 0, 0, 1, byte(orientation >> 8), byte(orientation), 0, 0, // Orientation
		0x88, 0x25, 0, 4, 0, 0, 0, 1, 0, 0, 0, 0, // GPSInfo pointer (dummy)
		0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	seg = append(seg, payload...)
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg...)
	return append(out, raw[2:]...)
}

func TestIngestRotatesResizesAndStrips(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	// 2000×1000 landscape with orientation 6 (rotate 90° CW) is a portrait photo.
	data := testJPEG(t, 2000, 1000, 6)
	p, err := s.Ingest(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1000 || p.Height != 2000 || p.Ext != ".jpg" {
		t.Fatalf("photo = %+v, want upright 1000×2000", p)
	}
	orig, _ := s.ReadFile(p.OriginalPath())
	if !bytes.Equal(orig, data) {
		t.Errorf("original should be kept untouched")
	}
	for size, want := range map[int][2]int{ThumbSize: {300, 600}, DisplaySize: {800, 1600}} {
		b, err := s.ReadFile(p.DerivedPath(size))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte("Exif")) {
			t.Errorf("%d copy should carry no EXIF", size)
		}
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width != want[0] || cfg.Height != want[1] {
			t.Errorf("%d copy is %dx%d, want %dx%d", size, cfg.Width, cfg.Height, want[0], want[1])
		}
	}
	// Same upload again is stored once, same identity.
	if p2, err := s.Ingest(data); err != nil || p2 != p {
		t.Fatalf("re-ingest = %+v, %v", p2, err)
	}
}

func TestIngestSmallPNGAndRejects(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	var buf bytes.Buffer
	png.Encode(&buf, image.NewGray(image.Rect(0, 0, 300, 200)))
	p, err := s.Ingest(buf.Bytes())
	if err != nil || p.Ext != ".png" {
		t.Fatalf("png: %+v, %v", p, err)
	}
	b, _ := s.ReadFile(p.DerivedPath(DisplaySize))
	if cfg, _ := jpeg.DecodeConfig(bytes.NewReader(b)); cfg.Width != 300 {
		t.Errorf("small images aren't enlarged: %d wide", cfg.Width)
	}
	if _, err := s.Ingest([]byte("hello, not an image")); err != ErrNotImage {
		t.Errorf("text: %v", err)
	}
	if _, err := s.Ingest(make([]byte, MaxBytes+1)); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("too big: %v", err)
	}
}

func TestServableAndDelete(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	p, err := s.Ingest(testJPEG(t, 50, 50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if path := s.ServablePath(p.SHA256 + "_600.jpg"); path == "" {
		t.Errorf("thumbnail should be servable")
	} else if _, err := os.Stat(path); err != nil {
		t.Errorf("servable path doesn't exist: %v", err)
	}
	for _, bad := range []string{p.SHA256 + ".jpg", "../../etc/passwd", p.SHA256 + "_800.jpg", "abc_600.jpg", strings.Repeat("0", 64) + "_600.jpg"} {
		if s.ServablePath(bad) != "" {
			t.Errorf("%q shouldn't be servable", bad)
		}
	}
	// A missing copy is made again from the original.
	os.Remove(s.path(p.DerivedPath(ThumbSize)))
	if path := s.ServablePath(p.SHA256 + "_600.jpg"); path == "" {
		t.Errorf("missing thumbnail should be remade")
	}

	if err := s.Delete(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(p.OriginalPath()); !os.IsNotExist(err) {
		t.Errorf("original should be gone: %v", err)
	}
}

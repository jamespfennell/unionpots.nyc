// Package photos stores piece photos on disk: the untouched original (kept
// private) plus a square thumbnail and a display-size copy, both re-encoded
// so they carry no metadata (in particular no GPS location).
//
// Files are named by the SHA-256 of the original, so an identical upload is
// stored once:
//
//	originals/ab/abcdef….jpg
//	derived/ab/abcdef…_600.jpg   thumbnail, fits in 600×600 (not cropped)
//	derived/ab/abcdef…_1600.jpg  fits in 1600×1600
//
// A missing derived copy (say after the sizes change) is made again from
// the original when it's first asked for.
//
// The same relative paths are used under "photos/" in backup storage.
package photos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"
)

// Sizes of the derived copies, in pixels.
const (
	ThumbSize   = 600
	DisplaySize = 1600
)

// MaxBytes is the largest original accepted.
const MaxBytes = 25 << 20

// ErrNotImage means the upload isn't an image we can read.
var ErrNotImage = errors.New("not a JPEG, PNG, WebP or GIF image")

type Store struct {
	Dir string // e.g. /data/photos

	// decoding processes one photo at a time: a 36 MP photo takes ~150 MB
	// to decode.
	decoding sync.Mutex
}

// Photo describes a stored photo.
type Photo struct {
	SHA256        string
	Ext           string // of the original, e.g. ".jpg"
	Width, Height int    // of the original, after rotating it upright
}

var extByType = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

// Ingest stores an uploaded image and its derived copies. Storing the same
// image twice is harmless.
func (s *Store) Ingest(data []byte) (Photo, error) {
	if len(data) > MaxBytes {
		return Photo{}, fmt.Errorf("photo is larger than %d MB", MaxBytes>>20)
	}
	ext, ok := extByType[http.DetectContentType(data)]
	if !ok {
		return Photo{}, ErrNotImage
	}
	s.decoding.Lock()
	defer s.decoding.Unlock()
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return Photo{}, ErrNotImage
	}
	sum := sha256.Sum256(data)
	p := Photo{SHA256: hex.EncodeToString(sum[:]), Ext: ext, Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}

	if err := writeOnce(s.path(p.OriginalPath()), func(f *os.File) error {
		_, err := f.Write(data)
		return err
	}); err != nil {
		return Photo{}, err
	}
	for _, size := range []int{ThumbSize, DisplaySize} {
		if err := s.writeDerived(p, img, size); err != nil {
			return Photo{}, err
		}
	}
	return p, nil
}

// writeDerived writes the copy of img that fits in size×size (never
// enlarged), as a JPEG without metadata.
func (s *Store) writeDerived(p Photo, img image.Image, size int) error {
	if img.Bounds().Dx() > size || img.Bounds().Dy() > size {
		img = imaging.Fit(img, size, size, imaging.Lanczos)
	}
	return writeOnce(s.path(p.DerivedPath(size)), func(f *os.File) error {
		return imaging.Encode(f, img, imaging.JPEG, imaging.JPEGQuality(85))
	})
}

// OriginalPath and DerivedPath are relative to the store (and to "photos/"
// in backup storage).
func (p Photo) OriginalPath() string {
	return filepath.ToSlash(filepath.Join("originals", p.SHA256[:2], p.SHA256+p.Ext))
}

func (p Photo) DerivedPath(size int) string {
	return filepath.ToSlash(filepath.Join("derived", p.SHA256[:2], fmt.Sprintf("%s_%d.jpg", p.SHA256, size)))
}

// Paths lists every file stored for the photo.
func (p Photo) Paths() []string {
	return []string{p.OriginalPath(), p.DerivedPath(ThumbSize), p.DerivedPath(DisplaySize)}
}

func (s *Store) path(rel string) string { return filepath.Join(s.Dir, filepath.FromSlash(rel)) }

// ReadFile returns a stored file by its relative path.
func (s *Store) ReadFile(rel string) ([]byte, error) { return os.ReadFile(s.path(rel)) }

var servable = regexp.MustCompile(`^([0-9a-f]{64})_(600|1600)\.jpg$`)

// ServablePath maps a public file name ("<sha>_600.jpg") to the derived copy
// on disk, making it from the original if it's missing. It returns "" if the
// name isn't one we serve or there's no such photo. Originals are never
// served.
func (s *Store) ServablePath(name string) string {
	m := servable.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	path := s.path(filepath.ToSlash(filepath.Join("derived", m[1][:2], name)))
	if _, err := os.Stat(path); err == nil {
		return path
	}
	originals, _ := filepath.Glob(s.path(filepath.ToSlash(filepath.Join("originals", m[1][:2], m[1]+".*"))))
	if len(originals) != 1 {
		return ""
	}
	s.decoding.Lock()
	defer s.decoding.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path // made while we waited
	}
	data, err := os.ReadFile(originals[0])
	if err != nil {
		return ""
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return ""
	}
	size, _ := strconv.Atoi(m[2])
	if s.writeDerived(Photo{SHA256: m[1]}, img, size) != nil {
		return ""
	}
	return path
}

// Delete removes the photo's files.
func (s *Store) Delete(p Photo) error {
	for _, rel := range p.Paths() {
		if err := os.Remove(s.path(rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// writeOnce writes a file atomically unless it already exists.
func writeOnce(path string, write func(*os.File) error) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

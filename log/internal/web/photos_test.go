package web

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/photos"
)

type fakePhotoBackup struct {
	kicks   int
	deleted []string
}

func (f *fakePhotoBackup) Kick() { f.kicks++ }
func (f *fakePhotoBackup) DeletePhoto(_ context.Context, p photos.Photo) error {
	f.deleted = append(f.deleted, p.SHA256)
	return nil
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// upload posts files as "photos"; script marks it as coming from the page's
// uploader (plain-text errors, the section as the answer).
func (a *testApp) upload(target string, script bool, files map[string][]byte) *httptest.ResponseRecorder {
	a.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("photos", name)
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest("POST", target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if script {
		req.Header.Set("X-Photos", "1")
	}
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func TestPhotoUploadServeAndRemove(t *testing.T) {
	a := newApp(t)
	a.login()
	fb := &fakePhotoBackup{}
	a.srv.PhotoBackup = fb
	ctx := context.Background()
	ids, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Action: "thrown", Date: "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}

	// Without script: two photos, then back to the piece page.
	rec := a.upload("/pieces/120/photos", false, map[string][]byte{"a.jpg": jpegBytes(t, 800, 600), "b.jpg": jpegBytes(t, 10, 10)})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/pieces/120" {
		t.Fatalf("upload: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	ps, _ := a.store.PiecePhotos(ctx, ids[0])
	if len(ps) != 2 || fb.kicks != 1 {
		t.Fatalf("photos = %d, kicks = %d", len(ps), fb.kicks)
	}

	// The piece page shows thumbnails; the thumbnail is served, the original isn't.
	page := a.do("GET", "/pieces/120", nil).Body.String()
	thumb := "/photos/" + ps[0].SHA256 + "_600.jpg"
	if !strings.Contains(page, thumb) || strings.Contains(page, "Remove</button>") {
		t.Fatalf("piece page should show thumbnails without Remove")
	}
	if rec := a.do("GET", thumb, nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("thumb: %d %v", rec.Code, rec.Header())
	}
	if rec := a.do("GET", "/photos/"+ps[0].SHA256+".jpg", nil); rec.Code != 404 {
		t.Fatalf("originals must not be served: %d", rec.Code)
	}
	a.cookie = nil
	if rec := a.do("GET", thumb, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("photos need a login: %d", rec.Code)
	}
	a.login()

	// From the script: the answer is the section; errors are plain text.
	rec = a.upload("/pieces/120/photos?edit=1", true, map[string][]byte{"c.jpg": jpegBytes(t, 20, 20)})
	if rec.Code != 200 || !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), `<section class="photos"`) || !strings.Contains(rec.Body.String(), "Remove</button>") {
		t.Fatalf("script upload: %d %s", rec.Code, rec.Body)
	}
	rec = a.upload("/pieces/120/photos", true, map[string][]byte{"notes.txt": []byte("hello")})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "notes.txt isn’t a photo") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("bad upload: %d %s", rec.Code, rec.Body)
	}
	if rec := a.upload("/pieces/999/photos", true, map[string][]byte{"c.jpg": jpegBytes(t, 20, 20)}); rec.Code != 400 && rec.Code != 404 {
		t.Fatalf("missing piece: %d", rec.Code)
	}

	// Removing deletes the record, the files and the backup copy.
	rec = a.do("POST", fmt.Sprintf("/pieces/120/photos/%d/delete?edit=1", ps[0].ID), nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/pieces/120/edit" {
		t.Fatalf("remove: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if left, _ := a.store.PiecePhotos(ctx, ids[0]); len(left) != 2 {
		t.Fatalf("after remove: %d photos", len(left))
	}
	if rec := a.do("GET", thumb, nil); rec.Code != 404 {
		t.Fatalf("removed photo still served: %d", rec.Code)
	}
	if len(fb.deleted) != 1 || fb.deleted[0] != ps[0].SHA256 {
		t.Fatalf("backup copy not deleted: %v", fb.deleted)
	}
}

func TestPhotoViewer(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	if _, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 2, Action: "thrown", Date: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	a.upload("/pieces/120/photos", true, map[string][]byte{"a.jpg": jpegBytes(t, 40, 30)})
	a.upload("/pieces/120/photos", true, map[string][]byte{"b.jpg": jpegBytes(t, 30, 40)})
	a.upload("/pieces/121/photos", true, map[string][]byte{"c.jpg": jpegBytes(t, 20, 20)})
	ps, _ := a.store.PiecePhotos(ctx, 120)
	other, _ := a.store.PiecePhotos(ctx, 121)

	// Thumbnails open the viewer.
	first := fmt.Sprintf("/pieces/120/photos/%d", ps[0].ID)
	second := fmt.Sprintf("/pieces/120/photos/%d", ps[1].ID)
	if page := a.do("GET", "/pieces/120", nil).Body.String(); !strings.Contains(page, `<a href="`+first+`"><img`) {
		t.Fatalf("thumbnail should link to the viewer:\n%s", page)
	}

	// The first photo: the large copy, 1 of 2, a next arrow but no previous,
	// and Share as a download link to the same file.
	page := a.do("GET", first, nil).Body.String()
	for _, want := range []string{
		`<img src="/photos/` + ps[0].SHA256 + `_1600.jpg"`, "1 of 2",
		`href="` + second + `" hx-boost="false" data-viewer-next`,
		`href="/photos/` + ps[0].SHA256 + `_1600.jpg" download="union-pots-120-1.jpg"`,
		`href="/pieces/120#photos" hx-boost="false" data-back`, // back, not to #photos: keeps the place
	} {
		if !strings.Contains(page, want) {
			t.Errorf("viewer should have %s:\n%s", want, page)
		}
	}
	if strings.Contains(page, "data-viewer-prev") {
		t.Errorf("the first photo has no previous one")
	}
	// Tapping the black closes it; tapping the photo doesn't.
	if !strings.Contains(page, `class="viewer-stage" data-viewer-stage data-viewer-backdrop`) || strings.Count(page, `class="viewer-bar" data-viewer-backdrop`) != 2 {
		t.Errorf("the black around the photo should close the viewer:\n%s", page)
	}
	if regexp.MustCompile(`<img[^>]*data-viewer-backdrop`).MatchString(page) {
		t.Errorf("the photo itself shouldn't close the viewer")
	}
	if page := a.do("GET", second, nil).Body.String(); !strings.Contains(page, "2 of 2") || !strings.Contains(page, `href="`+first+`" hx-boost="false" data-viewer-prev`) || strings.Contains(page, "data-viewer-next") {
		t.Errorf("second photo:\n%s", page)
	}

	// A piece's only photo: no count, a plain file name.
	page = a.do("GET", fmt.Sprintf("/pieces/121/photos/%d", other[0].ID), nil).Body.String()
	if strings.Contains(page, " of 1") || !strings.Contains(page, `download="union-pots-121.jpg"`) {
		t.Errorf("single photo:\n%s", page)
	}

	// Another piece's photo, or a missing one, isn't found.
	for _, path := range []string{fmt.Sprintf("/pieces/120/photos/%d", other[0].ID), "/pieces/120/photos/999", "/pieces/999/photos/1"} {
		if rec := a.do("GET", path, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

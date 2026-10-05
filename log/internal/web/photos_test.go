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

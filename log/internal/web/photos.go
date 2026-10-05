package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"runtime/debug"
	"time"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/photos"
)

// PhotoBackup copies photos to backup storage (nil when backups are off).
type PhotoBackup interface {
	Kick()
	DeletePhoto(ctx context.Context, p photos.Photo) error
}

// maxPhotosPerRequest bounds one upload. The page sends one photo per
// request; this is for the no-JavaScript form.
const maxPhotosPerRequest = 10

// addPhotos stores uploaded photos ("photos" files) for a piece. From the
// page's script it answers with the updated photos section; otherwise it
// goes back to the page it came from.
func (s *Server) addPhotos(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.Store.GetPiece(r.Context(), id); err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotosPerRequest*photos.MaxBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return userErr("Choose a photo to upload.")
	}
	n := 0
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return userErr("That upload is too large.")
			}
			return err
		}
		if part.FormName() != "photos" || part.FileName() == "" {
			continue
		}
		if n++; n > maxPhotosPerRequest {
			return userErr("Upload at most %d photos at a time.", maxPhotosPerRequest)
		}
		if err := s.addPhoto(r.Context(), id, part); err != nil {
			return err
		}
	}
	if n == 0 {
		return userErr("Choose a photo to upload.")
	}
	if s.PhotoBackup != nil {
		s.PhotoBackup.Kick()
	}
	return s.photosResponse(w, r, id)
}

func (s *Server) addPhoto(ctx context.Context, pieceID int64, part *multipart.Part) error {
	data, err := io.ReadAll(io.LimitReader(part, photos.MaxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return userErr("That upload is too large.")
		}
		return err
	}
	p, err := s.Photos.Ingest(data) // one at a time; see photos.Store
	size := len(data)
	data = nil
	debug.FreeOSMemory() // hand the decoded image's memory back straight away
	if errors.Is(err, photos.ErrNotImage) {
		return userErr("%s isn’t a photo this can read (JPEG, PNG, WebP or GIF).", part.FileName())
	} else if err != nil && size > photos.MaxBytes {
		return userErr("%s is larger than %d MB.", part.FileName(), photos.MaxBytes>>20)
	} else if err != nil {
		return err
	}
	_, err = s.Store.AddPhoto(ctx, pieceID, p.SHA256, p.Ext, p.Width, p.Height)
	return err
}

// deletePhoto removes a photo from a piece, from disk and from backup
// storage.
func (s *Server) deletePhoto(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	photoID, err := pathID(r, "pid")
	if err != nil {
		return err
	}
	p, err := s.Store.DeletePhoto(r.Context(), id, photoID)
	if err != nil {
		return err
	}
	files := photos.Photo{SHA256: p.SHA256, Ext: p.Ext}
	if err := s.Photos.Delete(files); err != nil {
		s.Log.Error("deleting photo files", "sha256", p.SHA256, "err", err)
	}
	if s.PhotoBackup != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.PhotoBackup.DeletePhoto(ctx, files); err != nil {
			s.Log.Error("deleting photo from backup storage", "sha256", p.SHA256, "err", err)
		}
	}
	return s.photosResponse(w, r, id)
}

func (s *Server) photosResponse(w http.ResponseWriter, r *http.Request, pieceID int64) error {
	if r.Header.Get("X-Photos") == "" && r.Header.Get("HX-Request") == "" {
		back := fmt.Sprintf("/pieces/%d", pieceID)
		if r.URL.Query().Get("edit") != "" {
			back += "/edit"
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return nil
	}
	ps, err := s.Store.PiecePhotos(r.Context(), pieceID)
	if err != nil {
		return err
	}
	return s.renderPartial(w, "photos", photoSection{PieceID: pieceID, Photos: ps, Edit: r.URL.Query().Get("edit") != ""})
}

// photoSection is the data for the "photos" partial.
type photoSection struct {
	PieceID int64
	Photos  []db.Photo
	Edit    bool // show Remove buttons
}

// photoFile serves a derived (metadata-free) copy. File names are content
// hashes, so they never change and can be cached for good.
func (s *Server) photoFile(w http.ResponseWriter, r *http.Request) {
	path := s.Photos.ServablePath(r.PathValue("name"))
	if path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

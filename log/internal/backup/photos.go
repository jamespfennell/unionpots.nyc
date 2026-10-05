package backup

import (
	"context"
	"fmt"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/photos"
)

// PhotoSource is the part of the database that tracks which photos still
// need copying to backup storage.
type PhotoSource interface {
	PhotosNotBackedUp(ctx context.Context) ([]db.Photo, error)
	MarkPhotoBackedUp(ctx context.Context, id int64) error
}

// Kick asks the running backup loop to upload new photos now rather than at
// the next interval. It never blocks.
func (b *Backuper) Kick() {
	select {
	case b.kicks() <- struct{}{}:
	default:
	}
}

// SyncPhotos uploads the files of every photo not yet backed up.
func (b *Backuper) SyncPhotos(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.syncPhotos(ctx)
}

func (b *Backuper) syncPhotos(ctx context.Context) error {
	if b.Photos == nil {
		return nil
	}
	pending, err := b.Photos.PhotosNotBackedUp(ctx)
	if err != nil {
		return err
	}
	files := &photos.Store{Dir: b.PhotoDir}
	for _, p := range pending {
		ph := photos.Photo{SHA256: p.SHA256, Ext: p.Ext}
		for _, rel := range ph.Paths() {
			data, err := files.ReadFile(rel)
			if err != nil {
				return fmt.Errorf("photo %d: %w", p.ID, err)
			}
			if err := b.Store.Put(ctx, PhotoPrefix+rel, data); err != nil {
				return fmt.Errorf("photo %d: %w", p.ID, err)
			}
		}
		if err := b.Photos.MarkPhotoBackedUp(ctx, p.ID); err != nil {
			return err
		}
		b.Log.Info("photo backed up", "id", p.ID, "sha256", p.SHA256)
	}
	return nil
}

// DeletePhoto removes a photo's files from backup storage (when it has been
// removed from a piece).
func (b *Backuper) DeletePhoto(ctx context.Context, p photos.Photo) error {
	for _, rel := range p.Paths() {
		if err := b.Store.Delete(ctx, PhotoPrefix+rel); err != nil {
			return err
		}
	}
	return nil
}

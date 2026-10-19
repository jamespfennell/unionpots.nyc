package backup

import (
	"context"
	"fmt"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/photos"
)

// PhotoSource lists the photos whose files are backed up.
type PhotoSource interface {
	AllPhotos(ctx context.Context) ([]db.Photo, error)
}

// Kick asks the running backup loop to upload new photos now rather than at
// the next interval. It never blocks.
func (b *Backuper) Kick() {
	select {
	case b.kicks() <- struct{}{}:
	default:
	}
}

// SyncPhotos uploads to each target the photo files it is missing.
func (b *Backuper) SyncPhotos(ctx context.Context) error {
	b.run.Lock()
	defer b.run.Unlock()
	return eachTarget(ctx, b.Targets, func(ctx context.Context, t Target) error {
		if err := b.syncPhotos(ctx, t); err != nil {
			err = fmt.Errorf("photo backup: %w", err)
			b.recordError(t.Name, err)
			return err
		}
		return nil
	})
}

// syncPhotos compares the target's photo files with the photos in the
// database and uploads the missing ones. Listing the target, rather than
// remembering what was sent, means a new target, or one that was down or
// lost files, catches up by itself.
func (b *Backuper) syncPhotos(ctx context.Context, t Target) error {
	if b.Photos == nil {
		return nil
	}
	all, err := b.Photos.AllPhotos(ctx)
	if err != nil {
		return err
	}
	stored, err := t.Store.List(ctx, PhotoPrefix)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(stored))
	for _, k := range stored {
		have[k] = true
	}
	files := &photos.Store{Dir: b.PhotoDir}
	for _, p := range all {
		ph := photos.Photo{SHA256: p.SHA256, Ext: p.Ext}
		uploaded := false
		for _, rel := range ph.Paths() {
			if have[PhotoPrefix+rel] {
				continue
			}
			data, err := files.ReadFile(rel)
			if err != nil {
				return fmt.Errorf("photo %d: %w", p.ID, err)
			}
			if err := t.Store.Put(ctx, PhotoPrefix+rel, data); err != nil {
				return fmt.Errorf("photo %d: %w", p.ID, err)
			}
			uploaded = true
		}
		if uploaded {
			b.Log.Info("photo backed up", "target", t.Name, "id", p.ID, "sha256", p.SHA256)
		}
	}
	return nil
}

// DeletePhoto removes a photo's files from every target (when it has been
// removed from a piece). A target that can't be reached keeps its copy.
func (b *Backuper) DeletePhoto(ctx context.Context, p photos.Photo) error {
	return eachTarget(ctx, b.Targets, func(ctx context.Context, t Target) error {
		for _, rel := range p.Paths() {
			if err := t.Store.Delete(ctx, PhotoPrefix+rel); err != nil {
				return err
			}
		}
		return nil
	})
}

// Package storage owns the on-disk layout of jonus's data directory.
//
// It exists so that exactly one place knows how files are arranged. The queue,
// the HTTP layer, and the CLI all ask for paths rather than building them, and
// changing the layout is a change to this package alone.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shaolianbo/jonus/internal/job"
)

// Subdirectory names inside the data root.
const (
	dbFile    = "jonus.db"
	tracksDir = "tracks"
	peaksDir  = "peaks"
)

// Layout is the arrangement of files under a data root.
//
// The zero value is not usable; build one with NewLayout, which creates the
// directories and checks that the root can actually be written to.
type Layout struct {
	root string
}

// NewLayout prepares the data directory rooted at root.
func NewLayout(root string) (*Layout, error) {
	if root == "" {
		return nil, errors.New("storage: data root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage: resolve data root %s: %w", root, err)
	}

	l := &Layout{root: abs}
	for _, dir := range []string{l.tracksRoot(), l.peaksRoot()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create %s: %w", dir, err)
		}
	}
	if err := l.checkWritable(); err != nil {
		return nil, err
	}
	return l, nil
}

// Root is the absolute data directory.
func (l *Layout) Root() string { return l.root }

// DBPath is the SQLite database file.
func (l *Layout) DBPath() string { return filepath.Join(l.root, dbFile) }

// tracksRoot is the directory holding one subdirectory per job.
func (l *Layout) tracksRoot() string { return filepath.Join(l.root, tracksDir) }

// peaksRoot is the directory holding cached waveform summaries.
func (l *Layout) peaksRoot() string { return filepath.Join(l.root, peaksDir) }

// JobDir is where a job's audio lives. It is the directory handed to the engine
// as its output directory.
func (l *Layout) JobDir(id job.ID) string {
	return filepath.Join(l.tracksRoot(), string(id))
}

// EnsureJobDir creates a job's directory and returns it.
func (l *Layout) EnsureJobDir(id job.ID) (string, error) {
	dir := l.JobDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("storage: create job directory %s: %w", dir, err)
	}
	return dir, nil
}

// PeaksPath is where a track's cached waveform summary is stored. It is derived
// from the track ID rather than the job, because a stem has its own waveform.
func (l *Layout) PeaksPath(trackID job.ID) string {
	return filepath.Join(l.peaksRoot(), string(trackID)+".json")
}

// RemoveJob deletes a job's audio and the cached waveforms of the tracks it
// produced.
//
// The caller passes the track IDs because the layout does not read the
// database. A missing directory is not an error: the goal is that the files are
// gone, and a job that failed before writing anything has none.
func (l *Layout) RemoveJob(id job.ID, trackIDs []job.ID) error {
	if err := os.RemoveAll(l.JobDir(id)); err != nil {
		return fmt.Errorf("storage: remove job directory for %s: %w", id, err)
	}
	for _, trackID := range trackIDs {
		if err := os.Remove(l.PeaksPath(trackID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("storage: remove peaks for %s: %w", trackID, err)
		}
	}
	return nil
}

// checkWritable proves the root accepts writes, rather than discovering it when
// the first job fails minutes into generation.
func (l *Layout) checkWritable() error {
	f, err := os.CreateTemp(l.root, ".jonus-write-check-*")
	if err != nil {
		return fmt.Errorf("storage: data directory %s is not writable: %w", l.root, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("storage: data directory %s is not writable: %w", l.root, err)
	}
	return nil
}

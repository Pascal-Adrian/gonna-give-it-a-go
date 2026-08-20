// Package store persists objects to disk, one JSON file per object.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileStore writes values to <root>/<category>/<id>.json.
type FileStore struct {
	root string
}

func New(root string) *FileStore {
	return &FileStore{root: root}
}

// Save writes the file for id and reports whether it had to, leaving an
// unchanged file untouched so its modification time still means something to
// anything watching the directory.
//
// The write is not atomic: an interrupted run is meant to be re-run, not
// resumed.
func (s *FileStore) Save(category, id string, v any) (bool, error) {
	if !validName(category) {
		return false, fmt.Errorf("%q is not a usable category", category)
	}
	if !validName(id) {
		return false, fmt.Errorf("%q is not a usable file name", id)
	}

	// Marshal before touching the filesystem, so a failure here leaves no
	// empty directory behind.
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshaling %s/%s: %w", category, id, err)
	}
	data = append(data, '\n')

	dir := filepath.Join(s.root, category)
	path := filepath.Join(dir, id+".json")

	// Comparing against the file rather than a remembered hash keeps the store
	// stateless, and correct across restarts.
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, data) {
		return false, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", dir, err)
	}
	// 0600: the export carries user email addresses.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// validName keeps a path segment from escaping the directory it belongs in.
func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

// Package store persists objects to disk, one JSON file per object.
package store

import (
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

// Save overwrites the file for id, creating the category directory as needed.
// The write is not atomic: an interrupted run is meant to be re-run, not
// resumed.
func (s *FileStore) Save(category, id string, v any) error {
	if !validName(category) {
		return fmt.Errorf("%q is not a usable category", category)
	}
	if !validName(id) {
		return fmt.Errorf("%q is not a usable file name", id)
	}

	// Marshal before touching the filesystem, so a failure here leaves no
	// empty directory behind.
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s/%s: %w", category, id, err)
	}

	dir := filepath.Join(s.root, category)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	// 0600: the export carries user email addresses.
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// validName keeps a path segment from escaping the directory it belongs in.
func validName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

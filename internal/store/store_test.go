package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type user struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

func TestSave(t *testing.T) {
	root := t.TempDir()
	s := New(root)

	want := user{GID: "42", Name: "Ada"}
	if err := s.Save("users", want.GID, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "users", "42.json"))
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	var got user
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("file does not end in a newline: %q", string(data))
	}
}

// A shorter object must not leave bytes of the longer one behind.
func TestSaveOverwrites(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	path := filepath.Join(root, "users", "42.json")

	if err := s.Save("users", "42", user{GID: "42", Name: strings.Repeat("long", 100)}); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	if err := s.Save("users", "42", user{GID: "42", Name: "Ada"}); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	var got user
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if got.Name != "Ada" {
		t.Errorf("Name = %q, want Ada", got.Name)
	}
}

func TestSaveRejectsUnusableIDs(t *testing.T) {
	ids := []string{"", "a/b", `a\b`, "..", "../escape", "12..34"}

	for _, id := range ids {
		root := t.TempDir()
		if err := New(root).Save("users", id, user{}); err == nil {
			t.Errorf("Save(%q) error = nil, want error", id)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("reading root: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("Save(%q) created %d entries, want none", id, len(entries))
		}
	}
}

func TestSaveErrors(t *testing.T) {
	t.Run("unmarshalable value", func(t *testing.T) {
		root := t.TempDir()
		err := New(root).Save("users", "42", make(chan int))
		if err == nil || !strings.Contains(err.Error(), "marshaling") {
			t.Fatalf("Save() error = %v, want a marshaling error", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "users", "42.json")); statErr == nil {
			t.Error("a file was written despite the marshaling failure")
		}
	})

	// A plain file where the category directory needs to go: portable, unlike
	// making a directory unwritable, which does nothing when run as root.
	t.Run("category path taken by a file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "users"), []byte("in the way"), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		err := New(root).Save("users", "42", user{})
		if err == nil || !strings.Contains(err.Error(), "creating") {
			t.Fatalf("Save() error = %v, want a directory creation error", err)
		}
	})
}

package cache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyIsStable(t *testing.T) {
	a, err := Key(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	b, err := Key(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if a != b {
		t.Fatalf("keys differ for the same material: %s != %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("key length = %d, want 64", len(a))
	}
}

func TestKeyDependsOnMaterial(t *testing.T) {
	a, _ := Key(map[string]any{"model": "one"})
	b, _ := Key(map[string]any{"model": "two"})
	if a == b {
		t.Fatal("different material produced the same key")
	}
}

func TestKeyRejectsUnmarshalableMaterial(t *testing.T) {
	if _, err := Key(map[string]any{"fn": func() {}}); err == nil {
		t.Fatal("Key accepted a value that cannot be marshalled")
	}
}

func TestPutGet(t *testing.T) {
	c := Open(t.TempDir())
	if err := c.Put("abcdef", []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	data, ok := c.Get("abcdef")
	if !ok {
		t.Fatal("Get after Put missed")
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("Get = %q", data)
	}
}

// Entries are spread over two-character subdirectories.
func TestPutLayout(t *testing.T) {
	dir := t.TempDir()
	c := Open(dir)
	if err := c.Put("abcdef", nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ab", "abcdef.json")); err != nil {
		t.Fatalf("entry is not at ab/abcdef.json: %v", err)
	}
}

func TestGetMiss(t *testing.T) {
	c := Open(t.TempDir())
	if _, ok := c.Get("absent"); ok {
		t.Fatal("Get hit on an empty cache")
	}
}

// An empty key is not an entry, so it never hits and never writes.
func TestEmptyKey(t *testing.T) {
	dir := t.TempDir()
	c := Open(dir)
	if err := c.Put("", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := c.Get(""); ok {
		t.Fatal("the empty key hit")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache directory has %d entries, want none", len(entries))
	}
}

// A nil cache is the "caching is off" case and must stay usable.
func TestNilCache(t *testing.T) {
	var c *Cache
	if _, ok := c.Get("key"); ok {
		t.Fatal("a nil cache hit")
	}
	if err := c.Put("key", []byte("x")); err != nil {
		t.Fatalf("Put on a nil cache: %v", err)
	}
	if c.Dir() != "" {
		t.Fatalf("Dir = %q, want empty", c.Dir())
	}
	if Open("") != nil {
		t.Fatal("Open with no directory returned a cache")
	}
}

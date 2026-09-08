// Package cache stores step results on disk so that a repeated run does not
// redo the work (docs/ru/spec.md, section 10.3).
//
// The cache is content addressed: the caller computes a key from everything
// that can change the result of a step -- its normalized definition, its
// rendered inputs, the files it reads, the model and the baton version -- and
// gets back the entry stored under that key, if any. Nothing here interprets
// the payload; it is opaque JSON owned by the engine.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Cache is a directory of entries. The zero value is not usable; use Open.
type Cache struct {
	dir string
}

// Open prepares the cache directory. It is created on the first write, so a
// read-only run never leaves an empty directory behind.
func Open(dir string) *Cache {
	if dir == "" {
		return nil
	}
	return &Cache{dir: dir}
}

// Dir is the directory the entries live in.
func (c *Cache) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// Key hashes the material of an entry. The material is marshalled as JSON, so
// map keys are sorted and the key does not depend on Go map iteration order.
func Key(material any) (string, error) {
	data, err := json.Marshal(material)
	if err != nil {
		return "", fmt.Errorf("cache: key material: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Get returns the entry stored under key. A cache that cannot be read is a
// miss, not an error: a broken cache must never stop a run.
func (c *Cache) Get(key string) ([]byte, bool) {
	if c == nil || key == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores an entry under key, replacing any previous one.
func (c *Cache) Put(key string, data []byte) error {
	if c == nil || key == "" {
		return nil
	}
	path := c.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}

// path spreads entries over two-character subdirectories so that a long
// running cache does not end up with one huge directory.
func (c *Cache) path(key string) string {
	if len(key) < 2 {
		return filepath.Join(c.dir, key+".json")
	}
	return filepath.Join(c.dir, key[:2], key+".json")
}

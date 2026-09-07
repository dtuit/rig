// Package instcache keeps a local name-to-IP cache of rig instances so the
// ssh-heavy commands can resolve a named instance without a provider call
// (issue 13). Advisory only (ADR-0006): entries are written on up and adopt,
// removed on down, refreshed or dropped whenever a fresh provider listing
// comes through, and stop being trusted once older than MaxAge.
package instcache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/dtuit/rig/internal/config"
	"github.com/dtuit/rig/internal/provider"
)

// MaxAge bounds how long an entry is trusted: instances are ephemeral, so an
// address older than a typical TTL is more likely stale than not.
const MaxAge = 12 * time.Hour

// Entry is a cached instance plus when it was recorded.
type Entry struct {
	provider.Instance
	CachedAt time.Time `json:"cached_at"`
}

// Path returns the cache location, beside the config file (the ledger
// precedent).
func Path() string {
	return filepath.Join(filepath.Dir(config.DefaultPath()), "instances.json")
}

func fresh(e Entry, now time.Time) bool {
	return now.Sub(e.CachedAt) < MaxAge
}

// load reads every entry; a missing or corrupt cache is an empty cache.
func load(path string) ([]Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, nil
	}
	return entries, nil
}

// save rewrites the cache atomically (temp file plus rename), so a reader
// racing a writer sees either the old or the new list, never a torn one.
func save(path string, entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	// a unique temp file per writer: two rig processes saving at once (the
	// parallel-agent case this cache exists for) must not truncate each other
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Read returns the cached instances still within MaxAge; a missing cache is empty.
func Read(path string, now time.Time) ([]provider.Instance, error) {
	entries, err := load(path)
	if err != nil {
		return nil, err
	}
	var out []provider.Instance
	for _, e := range entries {
		if fresh(e, now) {
			out = append(out, e.Instance)
		}
	}
	return out, nil
}

// Put records in, replacing any entry with the same ID or name and dropping
// entries older than MaxAge.
func Put(path string, in provider.Instance, now time.Time) error {
	entries, err := load(path)
	if err != nil {
		return err
	}
	kept := make([]Entry, 0, len(entries)+1)
	for _, e := range entries {
		if e.ID == in.ID || e.Name == in.Name || !fresh(e, now) {
			continue
		}
		kept = append(kept, e)
	}
	kept = append(kept, Entry{Instance: in, CachedAt: now})
	return save(path, kept)
}

// Remove drops the entry with that hostname or ID; removing an absent entry
// is a no-op.
func Remove(path, key string) error {
	entries, err := load(path)
	if err != nil {
		return err
	}
	kept := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Name == key || e.ID == key {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(entries) {
		return nil
	}
	return save(path, kept)
}

// Reconcile aligns the cache with a fresh provider listing: entries whose
// instance is still running are refreshed from it, the rest are dropped. It
// never adds entries — only up and adopt do — so a missing cache stays
// missing.
func Reconcile(path string, live []provider.Instance, now time.Time) error {
	entries, err := load(path)
	if err != nil || len(entries) == 0 {
		return err
	}
	byID := make(map[string]provider.Instance, len(live))
	for _, in := range live {
		byID[in.ID] = in
	}
	kept := make([]Entry, 0, len(entries))
	for _, e := range entries {
		in, ok := byID[e.ID]
		if !ok || in.Status != "running" || in.IP == "" {
			continue
		}
		kept = append(kept, Entry{Instance: in, CachedAt: now})
	}
	return save(path, kept)
}

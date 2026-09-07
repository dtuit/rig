package instcache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dtuit/rig/internal/provider"
)

var t0 = time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)

func inst(id, name, ip string) provider.Instance {
	return provider.Instance{ID: id, Name: name, IP: ip, Status: "running", Type: "1H100.80S.30V"}
}

func names(ins []provider.Instance) []string {
	var out []string
	for _, in := range ins {
		out = append(out, in.Name)
	}
	return out
}

func TestReadMissingIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	got, err := Read(path, t0)
	if err != nil || len(got) != 0 {
		t.Fatalf("Read(missing) = %v, %v; want empty, nil", got, err)
	}
	if err := Remove(path, "rig-nothing"); err != nil {
		t.Errorf("Remove on a missing cache should be a no-op, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a no-op Remove must not create the cache file")
	}
}

func TestPutReadRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "instances.json") // parent dir created on demand
	if err := Put(path, inst("id-1", "rig-a", "10.0.0.1"), t0); err != nil {
		t.Fatal(err)
	}
	if err := Put(path, inst("id-2", "rig-b", "10.0.0.2"), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, t0.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if n := names(got); len(n) != 2 || n[0] != "rig-a" || n[1] != "rig-b" {
		t.Fatalf("Read = %v, want [rig-a rig-b]", n)
	}
	if got[0].IP != "10.0.0.1" || got[0].Type != "1H100.80S.30V" || got[0].Status != "running" {
		t.Errorf("cached fields lost: %+v", got[0])
	}

	// re-putting the same name replaces, it does not duplicate
	if err := Put(path, inst("id-1", "rig-a", "10.0.0.9"), t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _ = Read(path, t0.Add(4*time.Minute))
	if len(got) != 2 {
		t.Fatalf("Put of an existing name must replace, got %v", names(got))
	}
	for _, in := range got {
		if in.Name == "rig-a" && in.IP != "10.0.0.9" {
			t.Errorf("replaced entry kept the old IP: %s", in.IP)
		}
	}

	if err := Remove(path, "rig-a"); err != nil {
		t.Fatal(err)
	}
	got, _ = Read(path, t0.Add(5*time.Minute))
	if n := names(got); len(n) != 1 || n[0] != "rig-b" {
		t.Errorf("after Remove(rig-a) = %v, want [rig-b]", n)
	}
	if err := Remove(path, "id-2"); err != nil { // by ID too
		t.Fatal(err)
	}
	if got, _ = Read(path, t0.Add(5*time.Minute)); len(got) != 0 {
		t.Errorf("after Remove(id-2) = %v, want empty", names(got))
	}
}

func TestExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	if err := Put(path, inst("id-1", "rig-a", "10.0.0.1"), t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := Read(path, t0.Add(MaxAge-time.Second)); len(got) != 1 {
		t.Errorf("just under MaxAge should still be served, got %v", names(got))
	}
	if got, _ := Read(path, t0.Add(MaxAge)); len(got) != 0 {
		t.Errorf("at MaxAge the entry must be gone, got %v", names(got))
	}
	// a Put prunes expired entries from the file itself
	if err := Put(path, inst("id-2", "rig-b", "10.0.0.2"), t0.Add(MaxAge+time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := load(path)
	if err != nil || len(entries) != 1 || entries[0].Name != "rig-b" {
		t.Errorf("Put should drop expired entries on disk, file has %v", entries)
	}
}

func TestReconcile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	for _, in := range []provider.Instance{
		inst("id-1", "rig-a", "10.0.0.1"),
		inst("id-2", "rig-b", "10.0.0.2"),
		inst("id-3", "rig-c", "10.0.0.3"),
	} {
		if err := Put(path, in, t0); err != nil {
			t.Fatal(err)
		}
	}
	live := []provider.Instance{
		inst("id-1", "rig-a", "10.0.0.7"),                                   // moved: refresh
		{ID: "id-2", Name: "rig-b", IP: "10.0.0.2", Status: "discontinued"}, // torn down: drop
		inst("id-9", "rig-z", "10.0.0.9"),                                   // unknown to the cache: not added
		// id-3 gone from the provider entirely: drop
	}
	later := t0.Add(MaxAge - time.Minute)
	if err := Reconcile(path, live, later); err != nil {
		t.Fatal(err)
	}
	got, _ := Read(path, later)
	if n := names(got); len(n) != 1 || n[0] != "rig-a" {
		t.Fatalf("after Reconcile = %v, want [rig-a]", n)
	}
	if got[0].IP != "10.0.0.7" {
		t.Errorf("Reconcile should refresh fields from the listing, IP = %s", got[0].IP)
	}
	if got, _ := Read(path, later.Add(2*time.Minute)); len(got) != 1 {
		t.Errorf("a refreshed entry gets a new CachedAt; it must outlive the original MaxAge")
	}
}

func TestCorruptCacheIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path, t0)
	if err != nil || len(got) != 0 {
		t.Errorf("corrupt cache should read as empty, got %v, %v", got, err)
	}
	if err := Put(path, inst("id-1", "rig-a", "10.0.0.1"), t0); err != nil {
		t.Fatalf("Put must recover a corrupt cache: %v", err)
	}
	if got, _ := Read(path, t0); len(got) != 1 {
		t.Errorf("after recovery Read = %v", names(got))
	}
}

// TestConcurrentSaves is the parallel-agent case: two processes saving at once
// must not truncate each other's temp file, and the file must end up valid.
func TestConcurrentSaves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := provider.Instance{ID: fmt.Sprintf("id-%d", i), Name: fmt.Sprintf("rig-x%d", i), IP: "1.2.3.4", Status: "running"}
			if err := Put(path, in, now); err != nil {
				t.Errorf("Put %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if _, err := Read(path, now); err != nil {
		t.Fatalf("Read after concurrent saves: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

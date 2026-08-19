package cli

import (
	"slices"
	"testing"
)

func TestSyncExcludes(t *testing.T) {
	base := syncExcludes(false, nil)
	for _, want := range []string{".env", ".git", ".venv"} {
		if !slices.Contains(base, want) {
			t.Errorf("default excludes missing %q: %v", want, base)
		}
	}

	withGit := syncExcludes(true, nil)
	if slices.Contains(withGit, ".git") {
		t.Errorf("--include-git should drop .git: %v", withGit)
	}

	extra := syncExcludes(false, []string{"images/", "*.ckpt"})
	for _, want := range []string{".env", "images/", "*.ckpt"} {
		if !slices.Contains(extra, want) {
			t.Errorf("user patterns must stack on defaults, missing %q: %v", want, extra)
		}
	}
}

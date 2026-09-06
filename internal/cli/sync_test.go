package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSplitSyncArg(t *testing.T) {
	cases := []struct{ arg, src, dest string }{
		{"./data", "./data", ""},
		{"./data:/root/data", "./data", "/root/data"},
		{"./data:~/data", "./data", "~/data"},
		{"~/src/proj:/root/proj", "~/src/proj", "/root/proj"},
		{"a:b:/root/x", "a:b", "/root/x"},                  // last colon only
		{"C:\\Users\\me\\data", "C:\\Users\\me\\data", ""}, // drive letter is not a dest
		{"odd:name", "odd:name", ""},                       // dest must look like a remote path
		{"./data:", "./data:", ""},                         // empty tail is not a dest
		{"./secrets.env:~", "./secrets.env", "~"},
	}
	for _, c := range cases {
		src, dest := splitSyncArg(c.arg)
		if src != c.src || dest != c.dest {
			t.Errorf("splitSyncArg(%q) = %q, %q; want %q, %q", c.arg, src, dest, c.src, c.dest)
		}
	}
}

func TestPlanSync(t *testing.T) {
	home, _ := os.UserHomeDir()
	cases := []struct {
		name, arg, dest string
		isDir           bool
		want            syncPair
	}{
		{"dir default dest", "./proj", "", true, syncPair{"./proj/", "~/proj/"}},
		{"dir strips and re-adds slash", "./proj/", "", true, syncPair{"./proj/", "~/proj/"}},
		{"dir --dest", "./proj", "/root/project", true, syncPair{"./proj/", "/root/project/"}},
		{"dir --dest with slash", "./proj", "/root/project/", true, syncPair{"./proj/", "/root/project/"}},
		{"dir own dest beats --dest", "./proj:/root/a", "/root/b", true, syncPair{"./proj/", "/root/a/"}},
		{"file default dest is ~/<basename>, no slash", "./secrets.env", "", false, syncPair{"./secrets.env", "~/secrets.env"}},
		{"file own dest is the file path", "./secrets.env:/root/.wandb/key", "", false, syncPair{"./secrets.env", "/root/.wandb/key"}},
		{"file into a directory keeps rsync's slash meaning", "./secrets.env:/root/secrets/", "", false, syncPair{"./secrets.env", "/root/secrets/"}},
		{"local ~ expands, remote ~ stays", "~/data:~/data", "", true, syncPair{filepath.Join(home, "data") + "/", "~/data/"}},
	}
	for _, c := range cases {
		if got := planSync(c.arg, c.dest, c.isDir); got != c.want {
			t.Errorf("%s: planSync(%q, %q, %v) = %+v, want %+v", c.name, c.arg, c.dest, c.isDir, got, c.want)
		}
	}
}

func TestShowProgress(t *testing.T) {
	cases := []struct {
		quiet, force, tty bool
		jobs              int
		want              bool
	}{
		{false, false, true, 1, true},   // human at a terminal, one stream
		{false, false, false, 1, false}, // piped (an agent): auto-quiet
		{false, false, true, 4, false},  // parallel bars are unreadable
		{true, false, true, 1, false},   // --quiet wins on a terminal
		{true, true, true, 1, false},    // --quiet beats --progress
		{false, true, false, 1, true},   // --progress forces it back on when piped
		{false, true, true, 4, true},    // ... and when parallel
	}
	for _, c := range cases {
		if got := showProgress(c.quiet, c.force, c.tty, c.jobs); got != c.want {
			t.Errorf("showProgress(quiet=%v force=%v tty=%v jobs=%d) = %v, want %v",
				c.quiet, c.force, c.tty, c.jobs, got, c.want)
		}
	}
}

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

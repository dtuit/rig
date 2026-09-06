package shellq

import (
	"os/exec"
	"testing"
)

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"":           "''",
		"train.sh":   "train.sh",
		"--lr=0.1":   "--lr=0.1",
		"A D E":      "'A D E'",
		"it's":       `'it'\''s'`,
		"%s\n":       "'%s\n'",
		"a && b":     "'a && b'",
		"~/data":     "'~/data'",
		"$HOME":      "'$HOME'",
		"x;rm -rf /": "'x;rm -rf /'",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestJoin(t *testing.T) {
	got := Join([]string{"bash", "pipeline.sh", "700", "32", "A D E", ""})
	want := "bash pipeline.sh 700 32 'A D E' ''"
	if got != want {
		t.Errorf("Join = %s, want %s", got, want)
	}
}

// TestJoinRoundTripsThroughSh is issue 10's done-when: a quoted list argument
// arrives on the instance as one word.
func TestJoinRoundTripsThroughSh(t *testing.T) {
	out, err := exec.Command("sh", "-c", Join([]string{"printf", "%s\n", "A D E", "it's", "", "x"})).Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "A D E\nit's\n\nx\n"; string(out) != want {
		t.Errorf("sh output = %q, want %q", out, want)
	}
}

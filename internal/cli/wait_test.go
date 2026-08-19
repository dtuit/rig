package cli

import (
	"errors"
	"testing"

	"github.com/dtuit/rig/internal/remote"
)

func TestWaitOutcome(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		err      error
		wantCode int // -1 = expect nil error, -2 = expect plain error
	}{
		{"success is silent", 0, nil, -1},
		{"job exit code propagates", 3, nil, 3},
		{"timeout maps to 124", 0, remote.ErrWaitTimeout, 124},
		{"other errors pass through", 0, errors.New("boom"), -2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := waitOutcome(c.code, c.err, "train")
			var ee exitError
			switch {
			case c.wantCode == -1:
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
			case c.wantCode == -2:
				if err == nil || errors.As(err, &ee) {
					t.Fatalf("err = %v, want plain error", err)
				}
			default:
				if !errors.As(err, &ee) || ee.code != c.wantCode {
					t.Fatalf("err = %v, want exit code %d", err, c.wantCode)
				}
			}
		})
	}
}

package cli

import (
	"errors"
	"fmt"
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
			checkExit(t, err, c.wantCode)
		})
	}
}

// TestWaitForOutcome covers the --for exit contract: 0 with the line printed,
// 124 on timeout, 125 when the job ended first (ADR-0005).
func TestWaitForOutcome(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"match is exit 0", nil, -1},
		{"timeout maps to 124", remote.ErrWaitTimeout, 124},
		{"job ended first maps to 125", fmt.Errorf("job exited 3: %w", remote.ErrNoMatch), 125},
		{"other errors pass through", errors.New("boom"), -2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkExit(t, waitForOutcome("stage: train", c.err, "train"), c.wantCode)
		})
	}
}

// checkExit asserts err's shape: -1 = nil, -2 = plain error, else that exit code.
func checkExit(t *testing.T, err error, wantCode int) {
	t.Helper()
	var ee exitError
	switch {
	case wantCode == -1:
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	case wantCode == -2:
		if err == nil || errors.As(err, &ee) {
			t.Fatalf("err = %v, want plain error", err)
		}
	default:
		if !errors.As(err, &ee) || ee.code != wantCode {
			t.Fatalf("err = %v, want exit code %d", err, wantCode)
		}
	}
}

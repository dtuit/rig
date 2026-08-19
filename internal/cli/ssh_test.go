package cli

import (
	"errors"
	"os/exec"
	"testing"
)

func TestPropagateExit(t *testing.T) {
	if err := propagateExit(nil); err != nil {
		t.Errorf("nil should pass through, got %v", err)
	}

	plain := errors.New("boom")
	if err := propagateExit(plain); err != plain {
		t.Errorf("non-exit errors should pass through, got %v", err)
	}

	execErr := exec.Command("sh", "-c", "exit 3").Run()
	err := propagateExit(execErr)
	var ee exitError
	if !errors.As(err, &ee) || ee.code != 3 {
		t.Fatalf("err = %v, want exitError code 3", err)
	}
	if ee.msg != "" {
		t.Errorf("remote exit should not add rig noise, msg = %q", ee.msg)
	}
}

package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dtuit/rig/internal/sshx"
)

// SuperviseCommand is the hidden rig subcommand --detach re-execs;
// its one argument is the initial State as JSON.
const SuperviseCommand = "tunnel-supervise"

// Launcher returns the production Launch seam: re-exec rig as a detached
// supervisor (its own session, stdio to the tunnel's log file) so it
// survives the ensure process and its terminal.
func Launcher(dir string) func(State) (int, error) {
	return func(st State) (int, error) {
		exe, err := os.Executable()
		if err != nil {
			return 0, err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		logf, err := os.OpenFile(st.logPath(dir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 0, err
		}
		defer logf.Close()
		b, err := json.Marshal(st)
		if err != nil {
			return 0, err
		}
		c := exec.Command(exe, SuperviseCommand, string(b))
		c.Stdout, c.Stderr = logf, logf
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			return 0, err
		}
		pid := c.Process.Pid
		c.Process.Release()
		return pid, nil
	}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func stopSupervisor(pid int) error {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Supervise is the body of the hidden supervisor subcommand: it runs the ssh
// forward child for st and owns st's state file — written on phase change,
// removed on clean stop (SIGTERM). No reconnect yet (issue 05): when the ssh
// child dies the supervisor records why and exits, leaving a dead-pid state
// file that readers reap. Its own stdio is the tunnel's log file.
func Supervise(dir string, st State, target sshx.Target) error {
	st.PID = os.Getpid()
	st.StartedAt = time.Now()
	path := st.statePath(dir)

	// two ensures racing to the same forward set: the incumbent wins
	if old, err := readState(path); err == nil && old.PID != st.PID && pidAlive(old.PID) {
		return fmt.Errorf("tunnel already supervised by pid %d", old.PID)
	}
	st.Phase = PhaseStarting
	if err := writeState(dir, st); err != nil {
		return err
	}

	fail := func(err error) error {
		st.Phase = PhaseGaveUp
		st.LastError = err.Error()
		writeState(dir, st)
		return err
	}

	// A foreign listener on a requested port would answer the connected
	// probe below and make exit 0 point at the wrong backend (story 5) —
	// refuse it up front. The patience absorbs a just-replaced tunnel's ssh
	// child still releasing the port.
	for _, f := range st.Forwards {
		if err := waitPortFree(f.Local, 2*time.Second); err != nil {
			return fail(err)
		}
	}

	var pairs [][2]int
	for _, f := range st.Forwards {
		pairs = append(pairs, f.pair())
	}
	ssh := target.SupervisedTunnelCommand(pairs)
	ssh.Stdout, ssh.Stderr = os.Stdout, os.Stderr

	if err := ssh.Start(); err != nil {
		return fail(fmt.Errorf("starting ssh: %w", err))
	}

	// clean stop: take the ssh child down and remove the state file
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		ssh.Process.Signal(syscall.SIGTERM)
		os.Remove(path)
		os.Exit(0)
	}()

	done := make(chan error, 1)
	go func() { done <- ssh.Wait() }()

	// Connected means usable: every local forward accepts a TCP connection.
	// ssh binds them only after authenticating, and ExitOnForwardFailure
	// turns a failed bind into a child exit — so this loop always ends.
	pending := map[int]bool{}
	for _, f := range st.Forwards {
		pending[f.Local] = true
	}
	startupDeath := func(err error) error {
		msg := fmt.Sprintf("ssh exited during startup: %v", err)
		// ssh's own explanation (bind refusal, auth failure) went to the log
		if line := lastLogLine(st.logPath(dir)); line != "" {
			msg += ": " + line
		}
		return fail(errors.New(msg))
	}
	for len(pending) > 0 {
		select {
		case err := <-done:
			return startupDeath(err)
		case <-time.After(250 * time.Millisecond):
			for port := range pending {
				conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
				if err == nil {
					conn.Close()
					delete(pending, port)
				}
			}
		}
	}
	// the ports answered — but only ssh's survival proves it was ssh answering
	select {
	case err := <-done:
		return startupDeath(err)
	default:
	}
	st.Phase = PhaseConnected
	if err := writeState(dir, st); err != nil {
		return err
	}

	return fail(fmt.Errorf("ssh exited: %v", <-done))
}

// waitPortFree confirms nothing else listens on the local port, giving a
// recently released one up to patience to come free.
func waitPortFree(port int, patience time.Duration) error {
	deadline := time.Now().Add(patience)
	for {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			l.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local port %d is already in use by a process outside rig", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// lastLogLine returns the log's final non-empty line, or "".
func lastLogLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Package sshx shells out to the system ssh/rsync binaries. Instances are
// ephemeral so host keys are not pinned; agent forwarding is used instead of
// placing credentials on remote machines.
package sshx

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Target struct {
	User         string
	Host         string
	ForwardAgent bool
	IdentityFile string // explicit private key; keeps rig working when no agent is alive
	AgentSocket  string // SSH_AUTH_SOCK override (rig-managed agent)
}

func (t Target) env() []string {
	if t.AgentSocket == "" {
		return nil
	}
	env := os.Environ()
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, "SSH_AUTH_SOCK=") {
			out = append(out, e)
		}
	}
	return append(out, "SSH_AUTH_SOCK="+t.AgentSocket)
}

func (t Target) addr() string { return t.User + "@" + t.Host }

func (t Target) baseArgs() []string {
	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=~/.ssh/rig-%C",
		"-o", "ControlPersist=600",
	}
	if t.ForwardAgent {
		args = append(args, "-A")
	}
	if t.IdentityFile != "" {
		args = append(args, "-i", t.IdentityFile)
	}
	return args
}

func (t Target) command(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	if env := t.env(); env != nil {
		c.Env = env
	}
	return c
}

// EnsureAgent makes sure an ssh-agent listens on socket and holds keys,
// starting one and loading them if needed. Keys must be passphrase-less.
func EnsureAgent(socket string, keys []string) error {
	check := exec.Command("ssh-add", "-l")
	check.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
	if err := check.Run(); err != nil {
		// exit 1 = agent alive but no keys; anything else = no usable agent
		var ee *exec.ExitError
		if !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			os.Remove(socket)
			if out, err := exec.Command("ssh-agent", "-a", socket).CombinedOutput(); err != nil {
				return fmt.Errorf("starting ssh-agent: %v: %s", err, out)
			}
		}
	}
	for _, k := range keys {
		add := exec.Command("ssh-add", "-q", k)
		add.Env = append(os.Environ(), "SSH_AUTH_SOCK="+socket)
		if out, err := add.CombinedOutput(); err != nil {
			return fmt.Errorf("ssh-add %s: %v: %s", k, err, out)
		}
	}
	return nil
}

// Run executes cmd on the target and returns combined output.
func (t Target) Run(cmd string) (string, error) {
	args := append(t.baseArgs(), t.addr(), cmd)
	out, err := t.command("ssh", args...).CombinedOutput()
	return string(out), err
}

// RunWithStdin executes cmd on the target feeding stdin.
func (t Target) RunWithStdin(stdin, cmd string) (string, error) {
	args := append(t.baseArgs(), t.addr(), cmd)
	c := t.command("ssh", args...)
	c.Stdin = bytes.NewBufferString(stdin)
	out, err := c.CombinedOutput()
	return string(out), err
}

// Interactive replaces stdio with a live ssh session (or remote command).
func (t Target) Interactive(extraArgs []string, remoteCmd string) error {
	args := append(t.baseArgs(), "-t")
	args = append(args, extraArgs...)
	args = append(args, t.addr())
	if remoteCmd != "" {
		args = append(args, remoteCmd)
	}
	c := t.command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// Stream runs cmd remotely with stdout/stderr attached to ours (non-tty).
func (t Target) Stream(cmd string) error {
	args := append(t.baseArgs(), t.addr(), cmd)
	c := t.command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// Tunnel opens local port forwards and blocks until interrupted.
// forwards entries are "local:remote" port pairs.
func (t Target) Tunnel(forwards [][2]int) error {
	args := t.baseArgs()
	for _, f := range forwards {
		args = append(args, "-L", fmt.Sprintf("%d:localhost:%d", f[0], f[1]))
	}
	args = append(args, "-N", t.addr())
	c := t.command("ssh", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// Rsync copies src to dest on the target (dest is a remote path).
func (t Target) Rsync(src, dest string, excludes []string, delete bool) error {
	sshCmd := "ssh " + strings.Join(t.baseArgs(), " ")
	args := []string{"-az", "--info=progress2", "-e", sshCmd}
	for _, e := range excludes {
		args = append(args, "--exclude", e)
	}
	if delete {
		args = append(args, "--delete")
	}
	args = append(args, src, t.addr()+":"+dest)
	c := t.command("rsync", args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// WaitForSSH polls until the target accepts TCP and answers a trivial command.
func (t Target) WaitForSSH(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(t.Host, "22"), 5*time.Second)
		if err == nil {
			conn.Close()
			if _, err := t.Run("true"); err == nil {
				return nil
			}
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("ssh to %s not ready after %s", t.Host, timeout)
}

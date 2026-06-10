// Package sshx shells out to the system ssh/rsync binaries. Instances are
// ephemeral so host keys are not pinned; agent forwarding is used instead of
// placing credentials on remote machines.
package sshx

import (
	"bytes"
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
	return args
}

// Run executes cmd on the target and returns combined output.
func (t Target) Run(cmd string) (string, error) {
	args := append(t.baseArgs(), t.addr(), cmd)
	out, err := exec.Command("ssh", args...).CombinedOutput()
	return string(out), err
}

// RunWithStdin executes cmd on the target feeding stdin.
func (t Target) RunWithStdin(stdin, cmd string) (string, error) {
	args := append(t.baseArgs(), t.addr(), cmd)
	c := exec.Command("ssh", args...)
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
	c := exec.Command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// Stream runs cmd remotely with stdout/stderr attached to ours (non-tty).
func (t Target) Stream(cmd string) error {
	args := append(t.baseArgs(), t.addr(), cmd)
	c := exec.Command("ssh", args...)
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
	c := exec.Command("ssh", args...)
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
	c := exec.Command("rsync", args...)
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

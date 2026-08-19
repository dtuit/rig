package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dtuit/rig/internal/config"
)

func newFetchCmd() *cobra.Command {
	var dest string
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "Download a file onto the instance directly (not through this machine)",
		Long: `Runs the download on the instance, where the bandwidth is: curl for
http(s) URLs, aws s3 cp for s3:// URLs. For S3, short-lived credentials are
taken from your local AWS identity (session creds are forwarded as-is;
long-lived keys are exchanged via STS first) and fed to the one remote
command over stdin — nothing is persisted on the instance.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			in, err := a.Resolve(cmd.Context(), instanceFlag)
			if err != nil {
				return err
			}
			remoteCmd, s3, err := fetchPlan(args[0], dest)
			if err != nil {
				return err
			}
			t := a.Target(in)
			if !s3 {
				return t.Stream(remoteCmd)
			}
			env, err := shortLivedAWSEnv(func(awsArgs ...string) (string, error) {
				out, err := exec.CommandContext(cmd.Context(), "aws", awsArgs...).Output()
				return string(out), err
			})
			if err != nil {
				return fmt.Errorf("resolving AWS credentials locally: %w", err)
			}
			return t.StreamWithStdin(env, remoteCmd)
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "remote destination path (default ~/<basename>; trailing / keeps the basename)")
	return cmd
}

// fetchPlan builds the remote download command. s3 reports whether AWS
// credentials must accompany it.
func fetchPlan(rawURL, dest string) (remoteCmd string, s3 bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false, fmt.Errorf("bad url %q: %w", rawURL, err)
	}
	base := path.Base(u.Path)
	if base == "/" || base == "." || base == "" {
		return "", false, fmt.Errorf("cannot derive a filename from %q; pass --dest", rawURL)
	}
	switch {
	case dest == "":
		dest = "~/" + base
	case strings.HasSuffix(dest, "/"):
		dest += base
	}
	switch u.Scheme {
	case "http", "https":
		return fmt.Sprintf("curl -fSL --create-dirs -o %s %s", shellPath(dest), shq(rawURL)), false, nil
	case "s3":
		return fmt.Sprintf(
			`command -v aws >/dev/null 2>&1 || { echo "rig fetch: aws CLI not installed on the instance" >&2; exit 127; }; `+
				`set -a; . /dev/stdin; set +a; aws s3 cp %s %s`,
			shq(rawURL), shellPath(dest)), true, nil
	default:
		return "", false, fmt.Errorf("unsupported scheme %q (http, https, s3)", u.Scheme)
	}
}

// shq single-quotes s for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellPath quotes a remote path while letting a leading ~/ expand.
func shellPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		return `"$HOME"/` + shq(p[2:])
	}
	return shq(p)
}

// shortLivedAWSEnv returns KEY=VALUE lines of short-lived AWS credentials
// derived from the operator's local identity. Session credentials (SSO,
// assumed role) are forwarded as-is; a long-lived access key is exchanged
// for an STS session token so it never leaves this machine (issue 04).
// run invokes the local aws CLI.
func shortLivedAWSEnv(run func(args ...string) (string, error)) (string, error) {
	out, err := run("configure", "export-credentials", "--format", "env")
	if err != nil {
		return "", fmt.Errorf("aws configure export-credentials: %w (is the aws CLI configured?)", err)
	}
	env := config.ParseEnv(out)
	creds := map[string]string{
		"AWS_ACCESS_KEY_ID":     env["AWS_ACCESS_KEY_ID"],
		"AWS_SECRET_ACCESS_KEY": env["AWS_SECRET_ACCESS_KEY"],
		"AWS_SESSION_TOKEN":     env["AWS_SESSION_TOKEN"],
	}
	if creds["AWS_ACCESS_KEY_ID"] == "" || creds["AWS_SECRET_ACCESS_KEY"] == "" {
		return "", fmt.Errorf("no AWS credentials in the local environment")
	}
	if creds["AWS_SESSION_TOKEN"] == "" {
		stsOut, err := run("sts", "get-session-token", "--duration-seconds", "3600", "--output", "json")
		if err != nil {
			return "", fmt.Errorf("aws sts get-session-token: %w", err)
		}
		var resp struct {
			Credentials struct {
				AccessKeyId     string
				SecretAccessKey string
				SessionToken    string
			}
		}
		if err := json.Unmarshal([]byte(stsOut), &resp); err != nil {
			return "", fmt.Errorf("parsing sts response: %w", err)
		}
		creds["AWS_ACCESS_KEY_ID"] = resp.Credentials.AccessKeyId
		creds["AWS_SECRET_ACCESS_KEY"] = resp.Credentials.SecretAccessKey
		creds["AWS_SESSION_TOKEN"] = resp.Credentials.SessionToken
	}
	var b strings.Builder
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		fmt.Fprintf(&b, "%s=%s\n", k, creds[k])
	}
	if region, err := run("configure", "get", "region"); err == nil {
		if r := strings.TrimSpace(region); r != "" {
			fmt.Fprintf(&b, "AWS_DEFAULT_REGION=%s\n", r)
		}
	}
	return b.String(), nil
}

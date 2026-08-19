package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestFetchPlan(t *testing.T) {
	cmd, s3, err := fetchPlan("https://host/a/b.tar.gz", "")
	if err != nil || s3 {
		t.Fatalf("http plan: s3=%v err=%v", s3, err)
	}
	if !strings.Contains(cmd, "curl") || !strings.Contains(cmd, "b.tar.gz") {
		t.Errorf("http plan = %q", cmd)
	}

	cmd, s3, err = fetchPlan("s3://bucket/data/set.tar", "")
	if err != nil || !s3 {
		t.Fatalf("s3 plan: s3=%v err=%v", s3, err)
	}
	if !strings.Contains(cmd, "aws s3 cp") || !strings.Contains(cmd, "set.tar") {
		t.Errorf("s3 plan = %q", cmd)
	}

	cmd, _, err = fetchPlan("https://host/a.bin?sig=abc", "/data/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "'/data/a.bin'") {
		t.Errorf("dir dest should gain the url basename (query stripped): %q", cmd)
	}

	if _, _, err := fetchPlan("ftp://host/x", ""); err == nil {
		t.Error("ftp should be rejected")
	}
}

func TestShortLivedAWSEnvForwardsSessionCreds(t *testing.T) {
	var calls [][]string
	run := func(args ...string) (string, error) {
		calls = append(calls, args)
		if args[0] == "configure" && args[1] == "export-credentials" {
			return "export AWS_ACCESS_KEY_ID=ASIAXXX\nexport AWS_SECRET_ACCESS_KEY=sec\nexport AWS_SESSION_TOKEN=tok\n", nil
		}
		if args[0] == "configure" && args[1] == "get" {
			return "eu-west-1\n", nil
		}
		return "", fmt.Errorf("unexpected aws %v", args)
	}
	env, err := shortLivedAWSEnv(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AWS_ACCESS_KEY_ID=ASIAXXX", "AWS_SESSION_TOKEN=tok", "AWS_DEFAULT_REGION=eu-west-1"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
	for _, c := range calls {
		if c[0] == "sts" {
			t.Error("session creds must be forwarded, not re-minted")
		}
	}
}

func TestShortLivedAWSEnvMintsForLongLivedKeys(t *testing.T) {
	run := func(args ...string) (string, error) {
		switch args[0] {
		case "configure":
			if args[1] == "export-credentials" {
				return "export AWS_ACCESS_KEY_ID=AKIAXXX\nexport AWS_SECRET_ACCESS_KEY=sec\n", nil
			}
			return "", fmt.Errorf("no region")
		case "sts":
			return `{"Credentials":{"AccessKeyId":"ASIAMINTED","SecretAccessKey":"minted","SessionToken":"mintedtok"}}`, nil
		}
		return "", fmt.Errorf("unexpected aws %v", args)
	}
	env, err := shortLivedAWSEnv(run)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env, "AKIAXXX") {
		t.Error("long-lived key must never be forwarded")
	}
	for _, want := range []string{"AWS_ACCESS_KEY_ID=ASIAMINTED", "AWS_SESSION_TOKEN=mintedtok"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %q:\n%s", want, env)
		}
	}
}

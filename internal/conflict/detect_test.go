package conflict

import (
	"strings"
	"testing"
)

func TestUnmergedFiles(t *testing.T) {
	in := "1 .M N... 100644 100644 100644 aaa bbb a.go\n" +
		"u UU N... 100644 100644 100644 100644 s1 s2 s3 auth.go\n" +
		"u UU N... 100644 100644 100644 100644 s1 s2 s3 api.go\n"
	got := unmergedFiles(in)
	if strings.Join(got, ",") != "auth.go,api.go" {
		t.Errorf("got %v", got)
	}
}

func TestParseHeadName(t *testing.T) {
	if parseHeadName("refs/heads/feat/x\n") != "feat/x" {
		t.Error("strip refs/heads/ and newline")
	}
	if parseHeadName("feat/y") != "feat/y" {
		t.Error("passthrough bare name")
	}
}

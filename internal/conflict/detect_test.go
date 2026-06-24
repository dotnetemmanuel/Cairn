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

func TestStageForSide(t *testing.T) {
	// Rebase: ours(:2) is the branch being replayed onto = incoming;
	// theirs(:3) is your replayed commit = yours.
	if stageForSide(OpRebase, SideIncoming) != 2 {
		t.Error("rebase incoming -> :2")
	}
	if stageForSide(OpRebase, SideYours) != 3 {
		t.Error("rebase yours -> :3")
	}
	// Merge inverts: ours(:2) is your current branch; theirs(:3) is the merged-in ref.
	if stageForSide(OpMerge, SideIncoming) != 3 {
		t.Error("merge incoming -> :3")
	}
	if stageForSide(OpMerge, SideYours) != 2 {
		t.Error("merge yours -> :2")
	}
}

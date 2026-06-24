package conflict

import "strings"

// stageForSide maps a human Side to the git merge-stage number whose blob holds
// that side's full-file version. The mapping inverts between rebase and merge:
// during a rebase git replays your commits onto the incoming branch, so :2
// ("ours") is the incoming branch and :3 ("theirs") is your commit; during a
// merge it's the conventional :2 = your current branch, :3 = the merged-in ref.
func stageForSide(op Op, side Side) int {
	rebase := op == OpRebase
	switch side {
	case SideIncoming:
		if rebase {
			return 2
		}
		return 3
	default: // SideYours
		if rebase {
			return 3
		}
		return 2
	}
}

// SideBlob returns the full-file contents of one side of a conflict, as lines,
// read from the merge stage that side maps to (`git show :<stage>:<path>`). It
// only feeds extra context to the reference panes — the picker itself works off
// Parse. A missing stage (e.g. add/add or a modify/delete conflict) yields nil
// with no error, so callers can simply show nothing for that side.
func SideBlob(dir, path string, op Op, side Side) ([]string, error) {
	stage := stageForSide(op, side)
	out, err := git(dir, "show", spec(stage, path))
	if err != nil {
		return nil, nil
	}
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func spec(stage int, path string) string {
	return ":" + string(rune('0'+stage)) + ":" + path
}

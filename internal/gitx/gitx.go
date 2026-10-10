// Package gitx answers yes/no questions about the repository a project lives
// in, read-only, so a run can verify git evidence against the real thing
// instead of trusting a note. Every function returns false when git is
// missing or the directory is not a repository.
package gitx

import (
	"os/exec"
	"strings"
)

func run(dir string, args ...string) (string, bool) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	out, ok := run(dir, "rev-parse", "--is-inside-work-tree")
	return ok && out == "true"
}

// BranchExists reports whether a local branch by that name exists.
func BranchExists(dir, branch string) bool {
	_, ok := run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return ok
}

// CommitExists reports whether the revision resolves to a commit.
func CommitExists(dir, rev string) bool {
	_, ok := run(dir, "cat-file", "-e", rev+"^{commit}")
	return ok
}

// CommitOnBranch reports whether the commit is reachable from the branch.
func CommitOnBranch(dir, rev, branch string) bool {
	out, ok := run(dir, "branch", "--contains", rev, "--format=%(refname:short)")
	if !ok {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == branch {
			return true
		}
	}
	return false
}

// Pushed reports whether the branch has a remote-tracking ref on origin,
// which git updates on a successful push. No network call is made.
func Pushed(dir, branch string) bool {
	_, ok := run(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	return ok
}

// CurrentBranch returns the checked-out branch name, or "".
func CurrentBranch(dir string) string {
	out, _ := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return out
}

// Package gitx answers yes/no questions about the repository a project lives
// in, read-only, so a run can verify git evidence against the real thing
// instead of trusting a note. Every function returns false when git is
// missing or the directory is not a repository.
package gitx

import (
	"os/exec"
	"strings"
	"time"
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

// Resolve returns the full sha for a revision, or "".
func Resolve(dir, rev string) string {
	out, _ := run(dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	return out
}

// CommitTime returns the committer time of a revision.
func CommitTime(dir, rev string) (time.Time, bool) {
	out, ok := run(dir, "show", "-s", "--format=%cI", rev)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, out)
	return t, err == nil
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

// Ancestor reports whether a is reachable from b (a is an ancestor of, or
// equal to, b).
func Ancestor(dir, a, b string) bool {
	c := exec.Command("git", "merge-base", "--is-ancestor", a, b)
	c.Dir = dir
	return c.Run() == nil
}

// Pushed reports whether origin has the branch and it includes every local
// commit on it. No network call is made; the remote-tracking ref is what
// git updated on the last successful push or fetch.
func Pushed(dir, branch string) bool {
	if _, ok := run(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); !ok {
		return false
	}
	return Ancestor(dir, "refs/heads/"+branch, "refs/remotes/origin/"+branch)
}

// Merged reports whether branch is fully contained in base (local base, or
// origin/base when the local one is missing).
func Merged(dir, branch, base string) bool {
	if BranchExists(dir, base) && Ancestor(dir, "refs/heads/"+branch, "refs/heads/"+base) {
		return true
	}
	return Ancestor(dir, "refs/heads/"+branch, "refs/remotes/origin/"+base)
}

// Clean reports whether the working tree has no uncommitted changes.
func Clean(dir string) bool {
	out, ok := run(dir, "status", "--porcelain")
	return ok && out == ""
}

// CurrentBranch returns the checked-out branch name, or "".
func CurrentBranch(dir string) string {
	out, _ := run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	return out
}

// RemoteURL is origin's fetch URL, or "".
func RemoteURL(dir string) string {
	out, ok := run(dir, "remote", "get-url", "origin")
	if !ok {
		return ""
	}
	return out
}

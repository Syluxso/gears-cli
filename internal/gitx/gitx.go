// Package gitx answers yes/no questions about the repository a project lives
// in, read-only, so a run can verify git evidence against the real thing
// instead of trusting a note. Every function returns false when git is
// missing or the directory is not a repository.
package gitx

import (
	"os/exec"
	"strconv"
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

// NormalizeRemote makes git@github.com:a/b.git and https://github.com/a/b
// compare equal: host/path, lower-case, no scheme, no .git. Same rule as
// gears-api, so a remote the API already holds is not re-sent every sync.
func NormalizeRemote(r string) string {
	r = strings.TrimSpace(r)
	if r == "" {
		return ""
	}
	r = strings.TrimSuffix(r, ".git")
	if i := strings.Index(r, "://"); i >= 0 {
		r = r[i+3:]
	}
	if i := strings.Index(r, "@"); i >= 0 && !strings.Contains(r[:i], "/") {
		r = r[i+1:]
	}
	r = strings.Replace(r, ":", "/", 1)
	return strings.ToLower(strings.TrimSuffix(r, "/"))
}

// Snap is a repository at a glance, read without touching the network or
// the index: what status --porcelain=v2 --branch and log -1 report.
type Snap struct {
	Branch   string // "" when detached
	Detached bool
	Upstream string // "" when the branch tracks nothing
	Ahead    int
	Behind   int
	Dirty    int // changed, staged, untracked and conflicted paths
	Head     string
	Subject  string
	At       time.Time // committer time of HEAD; zero with no commits
}

// Snapshot reads a repository's branch, upstream counts, dirty paths and
// last commit. Ahead/behind are against the remote-tracking ref as of the
// last fetch; nothing is fetched. ok is false when dir is not a repository.
func Snapshot(dir string) (Snap, bool) {
	var s Snap
	// The two reads are independent; run them at once.
	logc := make(chan string, 1)
	go func() {
		out, _ := run(dir, "log", "-1", "--format=%h%x00%cI%x00%s")
		logc <- out
	}()
	out, ok := run(dir, "--no-optional-locks", "status", "--porcelain=v2", "--branch")
	log := <-logc
	if !ok {
		return s, false
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "# branch.head "):
			s.Branch = strings.TrimPrefix(line, "# branch.head ")
			if s.Branch == "(detached)" {
				s.Branch, s.Detached = "", true
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(f) == 2 {
				s.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				s.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(line, "#"):
		default:
			s.Dirty++
		}
	}
	if log != "" {
		if f := strings.SplitN(log, "\x00", 3); len(f) == 3 {
			s.Head, s.Subject = f[0], f[2]
			s.At, _ = time.Parse(time.RFC3339, f[1])
		}
	}
	return s, true
}

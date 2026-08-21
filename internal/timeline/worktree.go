package timeline

import (
	"os"
	"path/filepath"
	"strings"
)

// Worktree names the checkout a session ran in.
//
// Two sessions in the same repository but different linked worktrees are
// different work: separate branches, separate build state, usually separate
// tasks. Their cwd basenames are often identical, so a row labelled by
// basename alone makes them indistinguishable — which is exactly the pair a
// reader most needs to tell apart.
type Worktree struct {
	// Name is the linked worktree's directory name, empty for a main
	// checkout. Git names a linked worktree in
	// <main>/.git/worktrees/<name>, and that name is what `git worktree
	// list` shows.
	Name string
	// Repo is the basename of the repository the worktree belongs to, which
	// for a linked worktree is the main checkout rather than the worktree's
	// own directory.
	Repo string
}

// worktreeOf resolves the checkout containing dir.
//
// It reads .git directly rather than shelling out to git. A week-wide fleet
// view resolves this for every session, most of them sharing a handful of
// directories, and a subprocess per row would cost more than the whole rest of
// the build. The two shapes .git takes are both trivial to read:
//
//   - a directory: an ordinary checkout, so the repo is dir's own root.
//   - a file "gitdir: <main>/.git/worktrees/<name>": a linked worktree, which
//     names both the worktree and the repo it belongs to.
//
// Anything else — no .git anywhere up the tree, an unreadable file, a shape
// git does not currently produce — yields the zero value. The label falls back
// to the cwd basename it used before, so an unrecognised layout degrades to
// today's behaviour rather than to an error.
func worktreeOf(dir string) Worktree {
	root, gitPath := findGit(dir)
	if root == "" {
		return Worktree{}
	}
	info, err := os.Stat(gitPath)
	if err != nil {
		return Worktree{}
	}
	if info.IsDir() {
		return Worktree{Repo: filepath.Base(root)}
	}

	data, err := os.ReadFile(gitPath)
	if err != nil {
		return Worktree{Repo: filepath.Base(root)}
	}
	line := strings.TrimSpace(string(data))
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitdir == line || gitdir == "" {
		return Worktree{Repo: filepath.Base(root)}
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(root, gitdir)
	}
	gitdir = filepath.Clean(gitdir)

	// .../<main>/.git/worktrees/<name>
	name := filepath.Base(gitdir)
	if filepath.Base(filepath.Dir(gitdir)) != "worktrees" {
		// A submodule points into .git/modules/<path>; the checkout is a
		// normal one from the reader's point of view.
		return Worktree{Repo: filepath.Base(root)}
	}
	mainGit := filepath.Dir(filepath.Dir(gitdir))
	return Worktree{Name: name, Repo: filepath.Base(filepath.Dir(mainGit))}
}

// findGit walks up from dir to the nearest .git, returning the checkout root
// and the .git path. Bounded by the filesystem root.
func findGit(dir string) (root, gitPath string) {
	for d := filepath.Clean(dir); d != "" && d != "/"; d = filepath.Dir(d) {
		p := filepath.Join(d, ".git")
		if _, err := os.Lstat(p); err == nil {
			return d, p
		}
	}
	return "", ""
}

package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GitStatus describes whether a repo has upstream changes.
type GitStatus struct {
	HasUpdates bool
	LocalHead  string
	RemoteHead string
}

func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
		"GIT_OPTIONAL_LOCKS=0",
	)
	return cmd
}

// GitFetchAndDiff performs a git fetch and compares the local HEAD with the
// remote tracking branch. Returns whether there are new commits to pull.
func GitFetchAndDiff(repoPath, branch string) (*GitStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := runGit(ctx, repoPath, "fetch", "origin"); err != nil {
		return nil, fmt.Errorf("git fetch: %w", err)
	}

	localHead, err := gitOutput(ctx, repoPath, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	remote := fmt.Sprintf("origin/%s", branch)
	remoteHead, err := gitOutput(ctx, repoPath, "rev-parse", remote)
	if err != nil {
		return nil, fmt.Errorf("git rev-parse %s: %w", remote, err)
	}

	return &GitStatus{
		HasUpdates: localHead != remoteHead,
		LocalHead:  localHead,
		RemoteHead: remoteHead,
	}, nil
}

// GitPull performs a git pull on the given repo directory.
func GitPull(repoPath, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if branch != "" {
		return runGit(ctx, repoPath, "pull", "origin", branch)
	}
	return runGit(ctx, repoPath, "pull")
}

// GitClone clones a repository to the given local path.
func GitClone(gitURL, localPath, branch string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := []string{"clone"}
	if branch != "" {
		args = append(args, "--branch", branch, "--single-branch")
	}
	args = append(args, gitURL, localPath)

	cmd := gitCmd(ctx, "", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %s: %w", stderr.String(), err)
	}
	return nil
}

// GitCurrentHead returns the current HEAD commit hash.
func GitCurrentHead(repoPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return gitOutput(ctx, repoPath, "rev-parse", "HEAD")
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := gitCmd(ctx, dir, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", strings.Join(args, " "), stderr.String())
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitCmd(ctx, dir, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}


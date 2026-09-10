package qa

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/openwiki/orchestrator/internal/db"
)

// Runner executes an OpenWiki QA session command for a repository.
type Runner struct {
	OpenwikiCLI string
	Timeout     time.Duration
}

// NewRunner creates a new QA runner instance.
func NewRunner(cli string, timeout time.Duration) *Runner {
	if cli == "" {
		cli = "openwiki"
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	return &Runner{
		OpenwikiCLI: cli,
		Timeout:     timeout,
	}
}

// StreamChat executes openwiki code chat and streams standard output line by line to outWriter.
// It records the complete session to SQLite once finished.
func (r *Runner) StreamChat(ctx context.Context, repo *db.Repo, userID string, question string, outWriter io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.OpenwikiCLI, "code", "chat", "--print", question)
	cmd.Dir = repo.LocalPath

	// Copy current environment variables
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		errMsg := fmt.Sprintf("failed to start chat command: %v", err)
		if outWriter != nil {
			fmt.Fprintf(outWriter, "data: [Error: %s]\n\n", errMsg)
		}
		_ = db.RecordQASession(repo.ID, userID, question, "Error: "+errMsg)
		return fmt.Errorf("%s", errMsg)
	}

	var answerBuf []byte
	scanner := bufio.NewScanner(stdout)

	// Stream stdout to caller line by line
	for scanner.Scan() {
		line := scanner.Text()
		answerBuf = append(answerBuf, line...)
		answerBuf = append(answerBuf, '\n')

		if outWriter != nil {
			fmt.Fprintf(outWriter, "data: %s\n\n", line)
			if flusher, ok := outWriter.(interface{ Flush() }); ok {
				flusher.Flush()
			}
		}
	}

	// Capture any error output
	errBuf, _ := io.ReadAll(stderr)

	if err := cmd.Wait(); err != nil {
		errMsg := string(errBuf)
		if errMsg == "" {
			errMsg = err.Error()
		}
		if outWriter != nil {
			fmt.Fprintf(outWriter, "data: [Error: %s]\n\n", errMsg)
		}
		// Record error attempt if failed
		_ = db.RecordQASession(repo.ID, userID, question, "Error: "+errMsg)
		return fmt.Errorf("chat command failed: %s", errMsg)
	}

	fullAnswer := string(answerBuf)

	// Record QA session to database
	if err := db.RecordQASession(repo.ID, userID, question, fullAnswer); err != nil {
		// Log DB error but don't fail the chat stream
		fmt.Printf("Warning: failed to record QA session: %v\n", err)
	}

	return nil
}

package scheduler

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBuildTimeoutSec is the default hard cap for a single openwiki CLI invocation (init/update).
	// Default: 3 hours (180 minutes); 0 means unconstrained / unlimited.
	DefaultBuildTimeoutSec = 180 * 60
	// maxCLILogLineBytes truncates pathological single lines (e.g. a whole wiki page JSON-encoded
	// into one tool-call log line) so they never stall the pipe reader or blow up the log.
	maxCLILogLineBytes = 16 * 1024
	// cliWaitDelay bounds how long Wait keeps waiting for stdout/stderr copying after the CLI exits
	// or is killed (e.g. orphaned grandchildren still holding the pipe open).
	cliWaitDelay = 10 * time.Second
	// cliProgressInterval throttles progress snapshots pushed to the pipeline / DB.
	cliProgressInterval = 2 * time.Second
)

// resolveBuildTimeoutSecFromEnv returns the OPENWIKI_BUILD_TIMEOUT_SEC override, or -1 when unset/invalid.
// 0 means "no hard cap".
func resolveBuildTimeoutSecFromEnv() int {
	v := strings.TrimSpace(os.Getenv("OPENWIKI_BUILD_TIMEOUT_SEC"))
	if v == "" {
		return -1
	}
	sec, err := strconv.Atoi(v)
	if err != nil || sec < 0 {
		return -1
	}
	return sec
}

// SetBuildTimeoutSec configures the hard cap for a single openwiki CLI invocation. 0 disables the cap.
func (b *Builder) SetBuildTimeoutSec(sec int) {
	if sec < 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buildTimeoutSec = sec
	b.buildTimeoutSet = true
}

// GetBuildTimeoutSec returns the effective CLI hard cap in seconds (0 = unlimited).
// Precedence: OPENWIKI_BUILD_TIMEOUT_SEC env > SetBuildTimeoutSec (system_settings) > default.
func (b *Builder) GetBuildTimeoutSec() int {
	if env := resolveBuildTimeoutSecFromEnv(); env >= 0 {
		return env
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.buildTimeoutSet {
		return b.buildTimeoutSec
	}
	return DefaultBuildTimeoutSec
}

// runCommandWithStreaming runs an openwiki CLI command, streaming stdout/stderr line by line into the
// orchestrator log and logBuf, with a configurable hard timeout and context cancellation.
//
// Guarantees:
//   - pipes are always fully drained (overlong lines are truncated, never stop the reader);
//   - the whole process group is killed on timeout / cancel, so grandchildren cannot pin the pipes;
//   - all goroutines writing logBuf have exited before this function returns (no data race with caller).
func (b *Builder) runCommandWithStreaming(ctx context.Context, cmd *exec.Cmd, repoID string, logBuf *strings.Builder, onProgress ...func(string)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	timeoutSec := b.GetBuildTimeoutSec()

	prOut, pwOut := io.Pipe()
	prErr, pwErr := io.Pipe()
	cmd.Stdout = pwOut
	cmd.Stderr = pwErr
	cmd.WaitDelay = cliWaitDelay
	setProcessGroup(cmd)

	var streamMu sync.Mutex // guards logBuf and lastProgress
	var progressMu sync.Mutex
	lastProgress := time.Now()

	emitProgress := func(force bool) {
		if len(onProgress) == 0 || onProgress[0] == nil {
			return
		}
		streamMu.Lock()
		if !force && time.Since(lastProgress) < cliProgressInterval {
			streamMu.Unlock()
			return
		}
		lastProgress = time.Now()
		snapshot := logBuf.String()
		streamMu.Unlock()
		progressMu.Lock()
		onProgress[0](snapshot)
		progressMu.Unlock()
	}

	appendLine := func(line string, isStderr bool) {
		streamMu.Lock()
		if isStderr {
			log.Printf("[build-cli][%s][stderr] %s", repoID, line)
		} else {
			log.Printf("[build-cli][%s] %s", repoID, line)
		}
		logBuf.WriteString(line)
		logBuf.WriteByte('\n')
		streamMu.Unlock()
	}

	var readWg sync.WaitGroup
	readLines := func(r io.Reader, isStderr bool) {
		defer readWg.Done()
		br := bufio.NewReaderSize(r, 64*1024)
		var cur []byte
		dropped := 0
		for {
			chunk, isPrefix, err := br.ReadLine()
			if len(chunk) > 0 {
				room := maxCLILogLineBytes - len(cur)
				if room > 0 {
					if len(chunk) > room {
						cur = append(cur, chunk[:room]...)
						dropped += len(chunk) - room
					} else {
						cur = append(cur, chunk...)
					}
				} else {
					dropped += len(chunk)
				}
			}
			if err == nil && isPrefix {
				continue
			}
			if len(cur) > 0 || dropped > 0 || err == nil {
				line := string(cur)
				if dropped > 0 {
					line += fmt.Sprintf(" ...(truncated %d bytes)", dropped)
				}
				appendLine(line, isStderr)
				emitProgress(false)
			}
			cur = cur[:0]
			dropped = 0
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					log.Printf("[build-cli][%s] pipe read error (stderr=%v): %v", repoID, isStderr, err)
				}
				// Keep draining so the writer side never blocks.
				_, _ = io.Copy(io.Discard, r)
				return
			}
		}
	}

	if err := cmd.Start(); err != nil {
		_ = pwOut.Close()
		_ = pwErr.Close()
		return fmt.Errorf("cmd start: %w", err)
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	log.Printf("[build-cli][%s] started pid=%d timeout=%s args=%v", repoID, pid, formatBuildTimeout(timeoutSec), cmd.Args[1:])

	readWg.Add(2)
	go readLines(prOut, false)
	go readLines(prErr, true)

	waitCh := make(chan error, 1)
	go func() {
		err := cmd.Wait() // returns once process exited and copying finished (bounded by WaitDelay)
		_ = pwOut.Close()
		_ = pwErr.Close()
		waitCh <- err
	}()

	var timeoutC <-chan time.Time
	if timeoutSec > 0 {
		timer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
		defer timer.Stop()
		timeoutC = timer.C
	}

	started := time.Now()
	var runErr error
	var abortMsg string
	select {
	case runErr = <-waitCh:
	case <-timeoutC:
		abortMsg = fmt.Sprintf("openwiki CLI timed out after %s (process group killed)", formatBuildTimeout(timeoutSec))
	case <-ctx.Done():
		abortMsg = fmt.Sprintf("openwiki CLI cancelled after %s: %v (process group killed)", time.Since(started).Round(time.Second), ctx.Err())
	}

	if abortMsg != "" {
		killProcessGroup(cmd)
		<-waitCh // bounded by WaitDelay
		runErr = errors.New(abortMsg)
	}

	// Ensure readers have drained everything before touching logBuf from this goroutine / returning.
	readWg.Wait()

	if abortMsg != "" {
		log.Printf("[build-cli][%s] ERROR: %s", repoID, abortMsg)
		streamMu.Lock()
		logBuf.WriteString(abortMsg + "\n")
		streamMu.Unlock()
	} else {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		log.Printf("[build-cli][%s] exited pid=%d code=%d after %s (err=%v)", repoID, pid, exitCode, time.Since(started).Round(time.Second), runErr)
	}
	emitProgress(true)
	return runErr
}

func formatBuildTimeout(sec int) string {
	if sec <= 0 {
		return "unlimited"
	}
	return (time.Duration(sec) * time.Second).String()
}


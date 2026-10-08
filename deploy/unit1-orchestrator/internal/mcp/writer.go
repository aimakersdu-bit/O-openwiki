package mcp

import (
	"encoding/json"
	"strings"
	"sync"
)

// MCPStreamWriter intercepts SSE output from qa.Manager.StreamChat,
// extracts streaming tokens to notify the client, and accumulates the full answer.
type MCPStreamWriter struct {
	mu         sync.Mutex
	onToken    func(token string)
	lineBuf    string
	fullAnswer strings.Builder
}

// NewMCPStreamWriter creates a new writer adapter for QA stream output.
func NewMCPStreamWriter(onToken func(token string)) *MCPStreamWriter {
	return &MCPStreamWriter{
		onToken: onToken,
	}
}

// Write handles incoming bytes from the stream, buffering until full lines are formed.
func (w *MCPStreamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	data := w.lineBuf + string(p)
	lines := strings.Split(data, "\n")
	// The last item is the remaining partial line
	w.lineBuf = lines[len(lines)-1]

	for i := 0; i < len(lines)-1; i++ {
		w.processLine(strings.TrimSpace(lines[i]))
	}

	return len(p), nil
}

func (w *MCPStreamWriter) processLine(line string) {
	if !strings.HasPrefix(line, "data: ") {
		return
	}

	raw := strings.TrimPrefix(line, "data: ")
	var parsed struct {
		Text       string `json:"text"`
		FullAnswer string `json:"fullAnswer"`
		Error      string `json:"error"`
	}

	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		if parsed.FullAnswer != "" {
			w.fullAnswer.Reset()
			w.fullAnswer.WriteString(parsed.FullAnswer)
		} else if parsed.Text != "" {
			w.fullAnswer.WriteString(parsed.Text)
			if w.onToken != nil {
				w.onToken(parsed.Text)
			}
		} else if parsed.Error != "" && w.fullAnswer.Len() == 0 {
			w.fullAnswer.WriteString("Error: " + parsed.Error)
		}
	} else {
		// Non-JSON fallback chunk
		if raw != "" && raw != "[DONE]" {
			w.fullAnswer.WriteString(raw)
			if w.onToken != nil {
				w.onToken(raw)
			}
		}
	}
}

// Flush flushes any remaining buffered text.
func (w *MCPStreamWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.lineBuf != "" {
		w.processLine(strings.TrimSpace(w.lineBuf))
		w.lineBuf = ""
	}
}

// FullAnswer returns the entire accumulated markdown answer.
func (w *MCPStreamWriter) FullAnswer() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	ans := w.fullAnswer.String()
	if ans == "" {
		return "[Completed]"
	}
	return ans
}

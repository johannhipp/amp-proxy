package main

import (
	"bytes"
	"log/slog"
	"net/http"
)

// ampToolNames maps lowercase tool names to the casing Amp expects.
// Amp's agent modes define includeTools with specific casing (e.g. "Bash", not "bash").
// The isToolAllowed check is case-sensitive, so if the LLM returns a tool name
// with different casing, it gets rejected with "tool X is not allowed for Y mode".
var ampToolNames = map[string]string{
	"bash": "Bash",
	"read": "Read",
	"grep": "Grep",
	"task": "Task",
}

// toolNameReplacements are precomputed byte patterns for efficient replacement
// in SSE stream chunks. Tool names appear in JSON as "name":"toolname" in both
// Anthropic and OpenAI streaming formats.
var toolNameReplacements []struct{ old, new []byte }

func init() {
	for wrong, correct := range ampToolNames {
		// Match "name":"bash" patterns (Anthropic SSE content_block_start)
		toolNameReplacements = append(toolNameReplacements,
			struct{ old, new []byte }{
				old: []byte(`"name":"` + wrong + `"`),
				new: []byte(`"name":"` + correct + `"`),
			},
			// Also match with space after colon: "name": "bash"
			struct{ old, new []byte }{
				old: []byte(`"name": "` + wrong + `"`),
				new: []byte(`"name": "` + correct + `"`),
			},
		)
	}
}

// toolNameRewriter wraps an http.ResponseWriter and rewrites tool names
// in the response stream to match Amp's expected casing.
type toolNameRewriter struct {
	http.ResponseWriter
	rewrites int
}

func newToolNameRewriter(w http.ResponseWriter) *toolNameRewriter {
	return &toolNameRewriter{ResponseWriter: w}
}

func (rw *toolNameRewriter) Write(b []byte) (int, error) {
	out := b
	for _, r := range toolNameReplacements {
		if bytes.Contains(out, r.old) {
			if &out[0] == &b[0] {
				// First modification — copy to avoid mutating the original buffer
				out = make([]byte, len(b))
				copy(out, b)
			}
			out = bytes.ReplaceAll(out, r.old, r.new)
			rw.rewrites++
		}
	}
	if rw.rewrites > 0 && &out[0] != &b[0] {
		slog.Debug("tool name rewrite", "rewrites", rw.rewrites)
	}
	// Write the (possibly modified) data, but report original length
	// to keep the caller's byte accounting correct.
	_, err := rw.ResponseWriter.Write(out)
	return len(b), err
}

func (rw *toolNameRewriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

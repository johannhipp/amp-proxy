package main

import (
	"net/http/httptest"
	"testing"
)

func TestToolNameRewriter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "rewrites bash to Bash",
			input: `data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"bash","input":{}}}`,
			want:  `data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"Bash","input":{}}}`,
		},
		{
			name:  "rewrites with space after colon",
			input: `{"name": "bash", "input": {}}`,
			want:  `{"name": "Bash", "input": {}}`,
		},
		{
			name:  "rewrites read to Read",
			input: `{"name":"read"}`,
			want:  `{"name":"Read"}`,
		},
		{
			name:  "rewrites grep to Grep",
			input: `{"name":"grep"}`,
			want:  `{"name":"Grep"}`,
		},
		{
			name:  "rewrites task to Task",
			input: `{"name":"task"}`,
			want:  `{"name":"Task"}`,
		},
		{
			name:  "does not rewrite already correct casing",
			input: `{"name":"Bash"}`,
			want:  `{"name":"Bash"}`,
		},
		{
			name:  "does not rewrite other tools",
			input: `{"name":"edit_file"}`,
			want:  `{"name":"edit_file"}`,
		},
		{
			name:  "does not rewrite bash in non-name context",
			input: `{"cmd":"bash -c echo hello"}`,
			want:  `{"cmd":"bash -c echo hello"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rw := newToolNameRewriter(rec)

			n, err := rw.Write([]byte(tt.input))
			if err != nil {
				t.Fatalf("Write error: %v", err)
			}
			if n != len(tt.input) {
				t.Errorf("Write returned %d, want %d", n, len(tt.input))
			}

			got := rec.Body.String()
			if got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

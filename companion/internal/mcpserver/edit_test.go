package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyEdits(t *testing.T) {
	base := "alpha\nbeta\ngamma\n"
	cases := []struct {
		name  string
		text  string
		edits []editOp
		want  string
		// wantErr is a substring of the expected error; empty means success.
		wantErr string
	}{
		{
			name:  "replace_exact",
			text:  base,
			edits: []editOp{{Type: "replace_exact", Match: "beta", Content: "BETA"}},
			want:  "alpha\nBETA\ngamma\n",
		},
		{
			name:  "replace_exact empty content deletes",
			text:  base,
			edits: []editOp{{Type: "replace_exact", Match: "beta\n", Content: ""}},
			want:  "alpha\ngamma\n",
		},
		{
			name:  "delete_exact",
			text:  base,
			edits: []editOp{{Type: "delete_exact", Match: "beta\n"}},
			want:  "alpha\ngamma\n",
		},
		{
			name:  "insert_before",
			text:  base,
			edits: []editOp{{Type: "insert_before", Match: "beta\n", Content: "pre\n"}},
			want:  "alpha\npre\nbeta\ngamma\n",
		},
		{
			name:  "insert_after",
			text:  base,
			edits: []editOp{{Type: "insert_after", Match: "beta\n", Content: "post\n"}},
			want:  "alpha\nbeta\npost\ngamma\n",
		},
		{
			name: "sequential edits see prior results",
			text: base,
			edits: []editOp{
				{Type: "replace_exact", Match: "beta", Content: "delta"},
				{Type: "insert_after", Match: "delta\n", Content: "epsilon\n"},
			},
			want: "alpha\ndelta\nepsilon\ngamma\n",
		},
		{
			name:  "replace_range middle",
			text:  base,
			edits: []editOp{{Type: "replace_range", StartLine: 2, EndLine: 2, Content: "two"}},
			want:  "alpha\ntwo\ngamma\n",
		},
		{
			name:  "replace_range multi-line to fewer lines",
			text:  base,
			edits: []editOp{{Type: "replace_range", StartLine: 1, EndLine: 2, Content: "one\n"}},
			want:  "one\ngamma\n",
		},
		{
			name:  "replace_range empty content deletes lines",
			text:  base,
			edits: []editOp{{Type: "replace_range", StartLine: 1, EndLine: 2, Content: ""}},
			want:  "gamma\n",
		},
		{
			name:  "replace_range last line keeps trailing newline",
			text:  base,
			edits: []editOp{{Type: "replace_range", StartLine: 3, EndLine: 3, Content: "GAMMA"}},
			want:  "alpha\nbeta\nGAMMA\n",
		},
		{
			name:  "replace_range last line without trailing newline stays bare",
			text:  "alpha\nbeta",
			edits: []editOp{{Type: "replace_range", StartLine: 2, EndLine: 2, Content: "BETA"}},
			want:  "alpha\nBETA",
		},
		{
			name:    "match not found",
			text:    base,
			edits:   []editOp{{Type: "replace_exact", Match: "nope", Content: "x"}},
			wantErr: "match not found",
		},
		{
			name:    "ambiguous match",
			text:    "x\nx\n",
			edits:   []editOp{{Type: "delete_exact", Match: "x\n"}},
			wantErr: "occurs 2 times",
		},
		{
			name:    "empty match",
			text:    base,
			edits:   []editOp{{Type: "replace_exact", Match: "", Content: "x"}},
			wantErr: "match is required",
		},
		{
			name:    "insert requires content",
			text:    base,
			edits:   []editOp{{Type: "insert_before", Match: "beta"}},
			wantErr: "content is required",
		},
		{
			name:    "range out of bounds",
			text:    base,
			edits:   []editOp{{Type: "replace_range", StartLine: 2, EndLine: 9, Content: "x"}},
			wantErr: "out of bounds",
		},
		{
			name:    "range inverted",
			text:    base,
			edits:   []editOp{{Type: "replace_range", StartLine: 3, EndLine: 1, Content: "x"}},
			wantErr: "invalid line range",
		},
		{
			name:    "range on empty file",
			text:    "",
			edits:   []editOp{{Type: "replace_range", StartLine: 1, EndLine: 1, Content: "x"}},
			wantErr: "out of bounds",
		},
		{
			name:    "unknown type",
			text:    base,
			edits:   []editOp{{Type: "regex_replace", Match: "a", Content: "b"}},
			wantErr: "unknown edit type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyEdits(tc.text, tc.edits)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyEdits: %v", err)
			}
			if got != tc.want {
				t.Fatalf("result = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditFileTool(t *testing.T) {
	session, root := startSession(t)
	old := "line one\nline two\nline three\n"
	writeTree(t, root, map[string]string{"a.txt": old})

	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(old),
		"edits": []map[string]any{
			{"type": "replace_exact", "match": "line two", "content": "line 2"},
			{"type": "insert_after", "match": "line three\n", "content": "line four\n"},
		},
	}), &out)
	if out.Status != "applied" || out.Operations[0].Status != "updated" {
		t.Fatalf("edit_file = %+v", out)
	}
	want := "line one\nline 2\nline three\nline four\n"
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != want {
		t.Fatalf("edited content = %q, want %q", data, want)
	}

	// Stale base hash → conflict before any edit is computed.
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(old),
		"edits": []map[string]any{{"type": "delete_exact", "match": "line 2\n"}},
	}), &out)
	if out.Status != "conflict" || out.Conflict.Reason != "base_hash_mismatch" {
		t.Fatalf("stale edit = %+v", out)
	}

	// A failing edit is a clear error and leaves the file untouched.
	res := callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(want),
		"edits": []map[string]any{{"type": "replace_exact", "match": "absent", "content": "x"}},
	})
	if !res.IsError || !strings.Contains(resultText(res), "match not found") {
		t.Fatalf("unappliable edit = %v", resultText(res))
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != want {
		t.Fatalf("failed edit changed the file: %q", data)
	}

	// Missing edits and missing base hash are rejected.
	if res := callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(want), "edits": []map[string]any{},
	}); !res.IsError {
		t.Error("edit_file with no edits must fail")
	}
	if res := callTool(t, session, "edit_file", map[string]any{
		"path":  "a.txt",
		"edits": []map[string]any{{"type": "delete_exact", "match": "line 2\n"}},
	}); !res.IsError {
		t.Error("edit_file without expected_sha256 must fail")
	}
}

func TestEditFileRejectsBinary(t *testing.T) {
	session, root := startSession(t)
	bin := "PK\x03\x04\x00data"
	writeTree(t, root, map[string]string{"blob.zip": bin})

	res := callTool(t, session, "edit_file", map[string]any{
		"path": "blob.zip", "expected_sha256": shaOf(bin),
		"edits": []map[string]any{{"type": "replace_exact", "match": "data", "content": "x"}},
	})
	if !res.IsError || !strings.Contains(resultText(res), "not an editable text file") {
		t.Fatalf("binary edit = %v", resultText(res))
	}
}

func TestEditFilePendingApprovalAndRollback(t *testing.T) {
	session, svc, root := startApprovalSession(t)
	old := "v1\n"
	writeTree(t, root, map[string]string{"a.txt": old})

	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(old),
		"edits": []map[string]any{{"type": "replace_exact", "match": "v1", "content": "v2"}},
	}), &out)
	if out.Status != "pending_approval" || out.ChangeSetID == "" {
		t.Fatalf("first call = %+v", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != old {
		t.Fatal("pending edit touched the disk")
	}

	if !svc.Resolve(out.ChangeSetID, true, "") {
		t.Fatal("Resolve failed")
	}
	var retry changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(old), "change_set_id": out.ChangeSetID,
		"edits": []map[string]any{{"type": "replace_exact", "match": "v1", "content": "v2"}},
	}), &retry)
	if retry.Status != "applied" {
		t.Fatalf("retry after approval = %+v", retry)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "v2\n" {
		t.Fatalf("content = %q", data)
	}

	// Edits roll back like any other change set: pending first, then approved.
	var rb changeOutput
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action": "rollback", "change_set_id": retry.ChangeSetID,
	}), &rb)
	if rb.Status != "pending_approval" {
		t.Fatalf("rollback first call = %+v", rb)
	}
	if !svc.Resolve("rollback:"+retry.ChangeSetID, true, "") {
		t.Fatal("Resolve rollback failed")
	}
	structured(t, callTool(t, session, "change_manage", map[string]any{
		"action": "rollback", "change_set_id": retry.ChangeSetID,
	}), &rb)
	if rb.Status != "rolled_back" {
		t.Fatalf("rollback = %+v", rb)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != old {
		t.Fatalf("content after rollback = %q", data)
	}
}

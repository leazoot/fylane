package mcpserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

func gbkBytes(t *testing.T, s string) string {
	t.Helper()
	out, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func utf16Bytes(t *testing.T, s string, big bool) string {
	t.Helper()
	endian := unicode.LittleEndian
	if big {
		endian = unicode.BigEndian
	}
	out, err := unicode.UTF16(endian, unicode.UseBOM).NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestDecodeEncodeProfile(t *testing.T) {
	t.Run("uniform crlf", func(t *testing.T) {
		text, prof, ok := decodeTextFile([]byte("a\r\nb\r\n"))
		if !ok || text != "a\nb\n" || !prof.uniform || !prof.crlf {
			t.Fatalf("decode = %q %+v %v", text, prof, ok)
		}
		got, err := prof.encode("a\nB\n")
		if err != nil || got != "a\r\nB\r\n" {
			t.Fatalf("encode = %q, %v", got, err)
		}
	})
	t.Run("mixed endings untouched", func(t *testing.T) {
		raw := "a\r\nb\nc\r\n"
		text, prof, ok := decodeTextFile([]byte(raw))
		if !ok || text != raw || prof.uniform {
			t.Fatalf("decode = %q %+v %v", text, prof, ok)
		}
		if got, err := prof.encode(text); err != nil || got != raw {
			t.Fatalf("encode = %q, %v", got, err)
		}
	})
	t.Run("gbk roundtrip", func(t *testing.T) {
		raw := gbkBytes(t, "你好\n世界\n")
		text, prof, ok := decodeTextFile([]byte(raw))
		if !ok || text != "你好\n世界\n" || prof.encoding != "gbk" {
			t.Fatalf("decode = %q %+v %v", text, prof, ok)
		}
		if got, err := prof.encode(text); err != nil || got != raw {
			t.Fatalf("encode = %x, %v; want %x", got, err, raw)
		}
	})
	t.Run("utf-16 roundtrip", func(t *testing.T) {
		for _, big := range []bool{false, true} {
			raw := utf16Bytes(t, "one\ntwo\n", big)
			text, prof, ok := decodeTextFile([]byte(raw))
			if !ok || text != "one\ntwo\n" || !strings.HasPrefix(prof.encoding, "utf-16") {
				t.Fatalf("decode(big=%v) = %q %+v %v", big, text, prof, ok)
			}
			if got, err := prof.encode(text); err != nil || got != raw {
				t.Fatalf("encode(big=%v) = %x, %v; want %x", big, got, err, raw)
			}
		}
	})
	t.Run("gbk unrepresentable", func(t *testing.T) {
		prof := textProfile{encoding: "gbk", uniform: true}
		if _, err := prof.encode("emoji \U0001F600\n"); err == nil || !strings.Contains(err.Error(), "gbk") {
			t.Fatalf("err = %v, want gbk encoding error", err)
		}
	})
}

func TestEditFilePreservesCRLF(t *testing.T) {
	session, root := startSession(t)
	raw := "line one\r\nline two\r\nline three\r\n"
	writeTree(t, root, map[string]string{"a.txt": raw})

	// Match and content use bare LF; the CRLF convention must survive.
	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "a.txt", "expected_sha256": shaOf(raw),
		"edits": []map[string]any{
			{"type": "replace_exact", "match": "line two\n", "content": "line 2\n"},
			{"type": "insert_after", "match": "line three\n", "content": "line four\n"},
		},
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("edit_file = %+v", out)
	}
	want := "line one\r\nline 2\r\nline three\r\nline four\r\n"
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}
}

func TestEditFileMixedEndingsUntouched(t *testing.T) {
	session, root := startSession(t)
	raw := "a\r\nb\nc\r\n"
	writeTree(t, root, map[string]string{"m.txt": raw})

	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "m.txt", "expected_sha256": shaOf(raw),
		"edits": []map[string]any{{"type": "replace_exact", "match": "b\n", "content": "B\n"}},
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("edit_file = %+v", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "m.txt")); string(data) != "a\r\nB\nc\r\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestEditFilePreservesGBK(t *testing.T) {
	session, root := startSession(t)
	raw := gbkBytes(t, "你好\n世界\n")
	writeTree(t, root, map[string]string{"cn.txt": raw})

	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "cn.txt", "expected_sha256": shaOf(raw),
		"edits": []map[string]any{{"type": "replace_exact", "match": "世界", "content": "朋友"}},
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("edit_file = %+v", out)
	}
	want := gbkBytes(t, "你好\n朋友\n")
	if data, _ := os.ReadFile(filepath.Join(root, "cn.txt")); string(data) != want {
		t.Fatalf("content = %x, want %x", data, want)
	}

	// Content outside the GBK repertoire is a clear error, not silent
	// re-encoding of the whole file.
	res := callTool(t, session, "edit_file", map[string]any{
		"path": "cn.txt", "expected_sha256": shaOf(want),
		"edits": []map[string]any{{"type": "insert_after", "match": "朋友", "content": "\U0001F600"}},
	})
	if !res.IsError || !strings.Contains(resultText(res), "gbk") {
		t.Fatalf("gbk unrepresentable edit = %v", resultText(res))
	}
	if data, _ := os.ReadFile(filepath.Join(root, "cn.txt")); string(data) != want {
		t.Fatalf("failed edit changed the file: %x", data)
	}
}

func TestEditFilePreservesUTF16(t *testing.T) {
	session, root := startSession(t)
	raw := utf16Bytes(t, "one\ntwo\n", false)
	writeTree(t, root, map[string]string{"u.txt": raw})

	var out changeOutput
	structured(t, callTool(t, session, "edit_file", map[string]any{
		"path": "u.txt", "expected_sha256": shaOf(raw),
		"edits": []map[string]any{{"type": "replace_exact", "match": "two", "content": "2"}},
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("edit_file = %+v", out)
	}
	want := utf16Bytes(t, "one\n2\n", false)
	if data, _ := os.ReadFile(filepath.Join(root, "u.txt")); string(data) != want {
		t.Fatalf("content = %x, want %x", data, want)
	}
}

func TestApplyPatchPreservesCRLF(t *testing.T) {
	session, root := startSession(t)
	raw := "line one\r\nline two\r\nline three\r\n"
	writeTree(t, root, map[string]string{"p.txt": raw})

	patch := "--- a/p.txt\n+++ b/p.txt\n@@ -1,3 +1,3 @@\n line one\n-line two\n+line 2\n line three\n"
	var out changeOutput
	structured(t, callTool(t, session, "apply_patch", map[string]any{
		"path": "p.txt", "patch": patch, "expected_sha256": shaOf(raw),
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("apply_patch = %+v", out)
	}
	want := "line one\r\nline 2\r\nline three\r\n"
	if data, _ := os.ReadFile(filepath.Join(root, "p.txt")); string(data) != want {
		t.Fatalf("content = %q, want %q", data, want)
	}
}

func TestApplyPatchRejectsBinary(t *testing.T) {
	session, root := startSession(t)
	bin := "PK\x03\x04\x00data"
	writeTree(t, root, map[string]string{"blob.zip": bin})

	res := callTool(t, session, "apply_patch", map[string]any{
		"path": "blob.zip", "expected_sha256": shaOf(bin),
		"patch": "--- a\n+++ b\n@@ -1,1 +1,1 @@\n-data\n+x\n",
	})
	if !res.IsError || !strings.Contains(resultText(res), "not a patchable text file") {
		t.Fatalf("binary patch = %v", resultText(res))
	}
}

func TestWriteFileMatchesTargetConvention(t *testing.T) {
	session, root := startSession(t)
	raw := gbkBytes(t, "你好\r\n")
	writeTree(t, root, map[string]string{"cn.txt": raw})

	// Full replacement in UTF-8 with LF comes back out as GBK with CRLF.
	var out changeOutput
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "cn.txt", "content": "世界\n", "expected_sha256": shaOf(raw),
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("write_file = %+v", out)
	}
	want := gbkBytes(t, "世界\r\n")
	if data, _ := os.ReadFile(filepath.Join(root, "cn.txt")); string(data) != want {
		t.Fatalf("content = %x, want %x", data, want)
	}

	// Content the target encoding cannot represent falls back to UTF-8:
	// a full replacement is authoritative and must not fail.
	structured(t, callTool(t, session, "write_file", map[string]any{
		"path": "cn.txt", "content": "\U0001F600\n", "expected_sha256": shaOf(want),
	}), &out)
	if out.Status != "applied" {
		t.Fatalf("write_file fallback = %+v", out)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "cn.txt")); string(data) != "\U0001F600\n" {
		t.Fatalf("fallback content = %q", data)
	}
}

package mcpserver

import (
	"fmt"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"

	"github.com/leazoot/fylane/companion/internal/textenc"
)

// textProfile captures how an existing file stores its text so that edits
// can be written back in the same form: same encoding, same line-ending
// convention (read side; this is the write-side counterpart).
type textProfile struct {
	encoding string
	// uniform is true when the file uses a single line-break convention or
	// has no line breaks at all. Mixed files are left exactly as they are.
	uniform bool
	// crlf is true when every line break in the file is CRLF.
	crlf bool
}

// decodeTextFile decodes raw file bytes and derives the profile needed to
// write equivalent bytes back. For files with a uniform CRLF convention the
// returned text is LF-normalized, so string matching and patching work the
// same way regardless of which convention the caller's text uses.
func decodeTextFile(data []byte) (string, textProfile, bool) {
	text, enc, ok := textenc.DetectDecode(data)
	if !ok {
		return "", textProfile{}, false
	}
	crlf := strings.Count(text, "\r\n")
	nl := strings.Count(text, "\n")
	prof := textProfile{
		encoding: enc,
		uniform:  crlf == 0 || crlf == nl,
		crlf:     crlf > 0 && crlf == nl,
	}
	if prof.crlf {
		text = toLF(text)
	}
	return text, prof, true
}

// normalizeInput folds caller-provided text (match strings, patch bodies,
// replacement content) into the convention decodeTextFile produced.
func (p textProfile) normalizeInput(s string) string {
	if p.uniform {
		return toLF(s)
	}
	return s
}

// encode re-applies the file's line-ending convention and encodes the text
// back into the file's original encoding.
func (p textProfile) encode(text string) (string, error) {
	if p.uniform {
		text = toLF(text)
		if p.crlf {
			text = toCRLF(text)
		}
	}
	switch p.encoding {
	case "", "utf-8":
		return text, nil
	case "utf-16le":
		return encodeWith(unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder(), text, p.encoding)
	case "utf-16be":
		return encodeWith(unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewEncoder(), text, p.encoding)
	case "gbk":
		return encodeWith(simplifiedchinese.GBK.NewEncoder(), text, p.encoding)
	default:
		return "", fmt.Errorf("unsupported encoding %s", p.encoding)
	}
}

func encodeWith(enc *encoding.Encoder, text, name string) (string, error) {
	out, err := enc.Bytes([]byte(text))
	if err != nil {
		return "", fmt.Errorf("the new content cannot be represented in the file's %s encoding: %v; use write_file with the full content to rewrite the file as UTF-8", name, err)
	}
	return string(out), nil
}

func toLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func toCRLF(s string) string { return strings.ReplaceAll(toLF(s), "\n", "\r\n") }

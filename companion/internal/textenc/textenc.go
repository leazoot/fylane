// Package textenc detects and decodes the text encodings Fylane supports
// (UTF-8 first, detect UTF-16/GBK). It is a leaf package shared by
// the MCP tool layer (read/edit paths) and the transaction engine (approval
// diff previews).
package textenc

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// DetectDecode identifies the text encoding of data and returns its content
// decoded to UTF-8. ok is false when the data is binary — raw binary content
// is never returned to the model or the approval UI.
func DetectDecode(data []byte) (text string, encoding string, ok bool) {
	if len(data) == 0 {
		return "", "utf-8", true
	}

	// UTF-16 is only trusted with an explicit BOM; BOM-less UTF-16 detection
	// is too error-prone against real binary files. Checked before the NUL
	// test below because UTF-16 content is full of NUL bytes.
	if len(data) >= 2 {
		var name string
		switch {
		case data[0] == 0xff && data[1] == 0xfe:
			name = "utf-16le"
		case data[0] == 0xfe && data[1] == 0xff:
			name = "utf-16be"
		}
		if name != "" {
			dec := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder()
			if decoded, err := dec.Bytes(data); err == nil && utf8.Valid(decoded) {
				return string(decoded), name, true
			}
			return "", "", false
		}
	}

	// NUL is a valid UTF-8 code point but never appears in real text files;
	// treat it as binary before the UTF-8 check. GBK never contains NUL in
	// multi-byte sequences either.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", "", false
	}
	if utf8.Valid(data) {
		return string(data), "utf-8", true
	}

	// GBK: decode and reject when the decoder had to substitute U+FFFD —
	// GBK itself cannot encode U+FFFD, so its presence means invalid input.
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(data); err == nil {
		s := string(decoded)
		if utf8.ValidString(s) && !strings.ContainsRune(s, utf8.RuneError) {
			return s, "gbk", true
		}
	}
	return "", "", false
}

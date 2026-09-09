package cmdexec

import "bytes"

// capWriter keeps the first limit bytes written to it and counts the rest.
//
// The important part is that Write always reports a full write. A capped
// writer that returned a short write or an error would make os/exec's copier
// stop draining the pipe, the child would block on its next write, and a
// chatty build would look like a hang until the timeout killed it. Discarding
// the overflow here keeps the child running to its natural exit.
type capWriter struct {
	limit int
	buf   bytes.Buffer
	seen  int
}

func newCapWriter(limit int) *capWriter {
	return &capWriter{limit: limit}
}

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.seen += n
	if room := w.limit - w.buf.Len(); room > 0 {
		if n > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

func (w *capWriter) String() string { return w.buf.String() }

func (w *capWriter) truncated() bool { return w.seen > w.limit }

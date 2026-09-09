package tasks

import "sync"

// buffer accumulates one output stream of a running task and hands out slices
// of it by cursor, so a poller can ask for "everything since byte N" instead
// of re-reading megabytes it already has.
//
// It is written to by os/exec's copier goroutines and read by whoever is
// polling, so every access takes the lock.
type buffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	seen  int
}

func newBuffer(limit int) *buffer { return &buffer{limit: limit} }

// Write keeps the first limit bytes and counts the rest. Like cmdexec's
// capped writer, it always reports a full write: a short write here would
// stop the pipe being drained and hang the child.
func (b *buffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen += n
	if room := b.limit - len(b.buf); room > 0 {
		if n > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil
}

// since returns the retained bytes after cursor, the cursor to pass next
// time, and whether anything was dropped for exceeding the limit. A cursor
// past the end returns nothing rather than an error: a poller that raced a
// purge should get an empty answer, not a failure.
func (b *buffer) since(cursor int) (string, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(b.buf) {
		cursor = len(b.buf)
	}
	return string(b.buf[cursor:]), len(b.buf), b.seen > b.limit
}

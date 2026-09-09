package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// The wire format: JSON-RPC 2.0 in messages framed by a Content-Length header,
// which is all of LSP's transport. It is written here rather than taken from a
// dependency because it is this much code and `backend.md` asks for the
// standard library first.

// maxMessageBytes bounds one incoming message. A documentSymbol answer for a
// generated file is genuinely large; a server that has lost its mind is
// larger still, and this process must not follow it.
const maxMessageBytes = 8 << 20

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("language server: %s (%d)", e.Message, e.Code) }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// conn is one JSON-RPC connection to a language server over its stdio pipes.
// It is safe for concurrent use: one reader goroutine owns the input side and
// hands each response to the call waiting for it.
type conn struct {
	w  io.WriteCloser
	r  *bufio.Reader
	wg sync.WaitGroup

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int
	pending map[int]chan message
	closed  error
}

func newConn(w io.WriteCloser, r io.Reader) *conn {
	c := &conn{w: w, r: bufio.NewReaderSize(r, 64<<10), pending: map[int]chan message{}}
	c.wg.Add(1)
	go c.read()
	return c
}

// call sends a request and waits for its answer, or for ctx to end.
func (c *conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encoding %s params: %w", method, err)
	}
	c.mu.Lock()
	if c.closed != nil {
		err := c.closed
		c.mu.Unlock()
		return nil, err
	}
	c.nextID++
	id := c.nextID
	reply := make(chan message, 1)
	c.pending[id] = reply
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(message{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case m := <-reply:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	}
}

// notify sends a message the server will not answer.
func (c *conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encoding %s params: %w", method, err)
	}
	return c.write(message{JSONRPC: "2.0", Method: method, Params: raw})
}

func (c *conn) write(m message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return fmt.Errorf("writing to the language server: %w", err)
	}
	if _, err := c.w.Write(body); err != nil {
		return fmt.Errorf("writing to the language server: %w", err)
	}
	return nil
}

// read owns the input side for the life of the connection. It ends when the
// pipe closes, which is what happens when the server exits, and it fails every
// call still waiting rather than leaving them to their contexts.
func (c *conn) read() {
	defer c.wg.Done()
	for {
		m, err := c.next()
		if err != nil {
			c.fail(err)
			return
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			// A request from the server. Answering is not optional: gopls
			// asks for configuration during initialization and waits, so a
			// client that ignores requests deadlocks on the handshake.
			c.answer(m)
		case m.Method != "":
			// A notification — diagnostics, progress, log lines. Nothing here
			// wants them, and they carry file contents, which must not be
			// logged (backend.md).
		default:
			c.deliver(m)
		}
	}
}

func (c *conn) next() (message, error) {
	length := -1
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "content-length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return message{}, fmt.Errorf("language server sent an unreadable Content-Length")
			}
			length = n
		}
	}
	if length < 0 {
		return message{}, errors.New("language server sent a message with no Content-Length")
	}
	if length > maxMessageBytes {
		return message{}, fmt.Errorf("language server sent a %d byte message, over the %d byte limit", length, maxMessageBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.r, body); err != nil {
		return message{}, err
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		return message{}, fmt.Errorf("language server sent unreadable JSON: %w", err)
	}
	return m, nil
}

// answer replies to a server-initiated request. Fylane configures nothing and
// registers nothing, so every answer is the empty one — but it is sent, because
// silence would hang the server.
func (c *conn) answer(m message) {
	var result any
	if m.Method == "workspace/configuration" {
		// The reply must have one entry per item asked about.
		var req struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(m.Params, &req)
		items := make([]map[string]any, len(req.Items))
		for i := range items {
			items[i] = map[string]any{}
		}
		result = items
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = c.write(message{JSONRPC: "2.0", ID: m.ID, Result: raw})
}

func (c *conn) deliver(m message) {
	var id int
	if err := json.Unmarshal(m.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	reply, ok := c.pending[id]
	c.mu.Unlock()
	if ok {
		reply <- m
	}
}

// fail records why the connection ended and releases everyone waiting on it.
func (c *conn) fail(err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		err = errors.New("the language server stopped")
	}
	c.mu.Lock()
	if c.closed == nil {
		c.closed = err
	}
	pending := c.pending
	c.pending = map[int]chan message{}
	c.mu.Unlock()
	for _, reply := range pending {
		reply <- message{Error: &rpcError{Message: err.Error()}}
	}
}

// close ends the connection's write side and waits for the reader to finish.
func (c *conn) close() {
	c.writeMu.Lock()
	_ = c.w.Close()
	c.writeMu.Unlock()
	c.wg.Wait()
}

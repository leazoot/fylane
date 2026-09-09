package tunnel

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// forwardTimeout caps how long the relay waits for the Companion to answer a
// tunneled request. Long by design: write approvals block on a human
// (validated further downstream).
const forwardTimeout = 15 * time.Minute

// Server is the relay side of the tunnel. It terminates the Companion's
// outbound WebSocket and forwards public HTTP requests through it. At most
// one Companion is active at a time (Phase 0: single device).
type Server struct {
	auth func(*http.Request) (string, error)

	// OnConnect/OnDisconnect, when set, observe device tunnel lifecycle
	// (online-status reporting). Called outside the registry lock.
	OnConnect    func(deviceID string)
	OnDisconnect func(deviceID string)

	mu    sync.Mutex
	conns map[string]*serverConn
}

type serverConn struct {
	ws      *websocket.Conn
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan *Frame
	closed  bool
}

// legacyIdentity is the registry key used by shared-token Companions, which
// carry no device identity.
const legacyIdentity = "legacy"

// NewServer returns a Server that only accepts Companions presenting token
// (the Phase 0 shared-token scheme, still used for simple self-hosting).
func NewServer(token string) (*Server, error) {
	if token == "" {
		return nil, errors.New("tunnel token must not be empty")
	}
	want := "Bearer " + token
	return NewServerAuth(func(r *http.Request) (string, error) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(AuthHeader)), []byte(want)) != 1 {
			return "", errors.New("invalid tunnel token")
		}
		return legacyIdentity, nil
	}), nil
}

// NewServerAuth returns a Server that authenticates Companion registrations
// with auth, which returns the connecting device's identity (the routing
// key). auth must not be nil — there is no unauthenticated mode.
func NewServerAuth(auth func(*http.Request) (string, error)) *Server {
	if auth == nil {
		panic("tunnel: nil auth")
	}
	return &Server{auth: auth, conns: map[string]*serverConn{}}
}

// CompanionConnected reports whether any Companion tunnel is active.
func (s *Server) CompanionConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns) > 0
}

// DeviceConnected reports whether the given device's tunnel is active.
func (s *Server) DeviceConnected(deviceID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[deviceID] != nil
}

// HandleTunnel upgrades the Companion's registration request. It blocks
// until the tunnel closes.
func (s *Server) HandleTunnel(w http.ResponseWriter, r *http.Request) {
	identity, err := s.auth(r)
	if err != nil || identity == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(MaxFrameBytes)

	conn := &serverConn{ws: ws, pending: make(map[uint64]chan *Frame)}
	s.mu.Lock()
	old := s.conns[identity]
	s.conns[identity] = conn
	s.mu.Unlock()
	if old != nil {
		old.close(websocket.StatusGoingAway, "replaced by a new companion connection")
	}
	log.Printf("tunnel: companion connected")
	if s.OnConnect != nil {
		s.OnConnect(identity)
	}

	conn.readLoop(r.Context())

	s.mu.Lock()
	stillCurrent := s.conns[identity] == conn
	if stillCurrent {
		delete(s.conns, identity)
	}
	s.mu.Unlock()
	conn.close(websocket.StatusNormalClosure, "")
	log.Printf("tunnel: companion disconnected")
	// A replaced connection must not report the replacement as offline.
	if stillCurrent && s.OnDisconnect != nil {
		s.OnDisconnect(identity)
	}
}

// ServeHTTP forwards a public HTTP request to the shared-token Companion
// (legacy single-device mode). OAuth mode routes per device via ForwardTo.
// It performs no caller authentication itself — the relay must gate it
// (legacy mode presents the shared token; see relay/cmd/relay).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.ForwardTo(w, r, legacyIdentity)
}

// ForwardTo forwards a public HTTP request to the given device's tunnel,
// answering 503 immediately when it is offline — offline writes are never
// queued. It returns the upstream status code.
func (s *Server) ForwardTo(w http.ResponseWriter, r *http.Request, deviceID string) int {
	start := time.Now()
	status := s.forward(w, r, deviceID)
	// Log request type, duration, and status only — never payloads or paths
	// beyond the fixed public endpoint.
	log.Printf("relay: %s %s -> %d (%s)", r.Method, r.URL.Path, status, time.Since(start).Round(time.Millisecond))
	return status
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, deviceID string) int {
	// The buffered tunnel cannot carry a never-ending standalone SSE stream;
	// MCP clients treat 405 on GET as "stream not supported" and continue.
	if r.Method == http.MethodGet {
		return writeJSONError(w, http.StatusMethodNotAllowed, "standalone SSE streams are not supported by this tunnel")
	}

	s.mu.Lock()
	conn := s.conns[deviceID]
	s.mu.Unlock()
	if conn == nil {
		return writeJSONError(w, http.StatusServiceUnavailable, "companion is offline; start it and retry")
	}

	body, err := readBody(r)
	if err != nil {
		return writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
	}

	ctx, cancel := context.WithTimeout(r.Context(), forwardTimeout)
	defer cancel()

	ch, id, err := conn.startRequest(ctx, &Frame{
		Type:   FrameRequest,
		Method: r.Method,
		URL:    r.URL.RequestURI(),
		Header: r.Header,
		Body:   body,
	})
	if err != nil {
		return writeJSONError(w, http.StatusBadGateway, "companion did not answer: "+err.Error())
	}
	defer conn.finishRequest(id)

	// Stream response frames to the caller as they arrive so long-running
	// handlers (SSE, pending approvals) are not buffered behind proxies.
	flusher, _ := w.(http.Flusher)
	status := 0
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				if status == 0 {
					return writeJSONError(w, http.StatusBadGateway, "tunnel closed while waiting for the response")
				}
				return status
			}
			switch f.Type {
			case FrameResponseHeader:
				if status != 0 {
					continue
				}
				h := w.Header()
				for k, vs := range f.Header {
					for _, v := range vs {
						h.Add(k, v)
					}
				}
				status = f.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				if flusher != nil {
					flusher.Flush()
				}
			case FrameResponseChunk:
				if status == 0 || len(f.Body) == 0 {
					continue
				}
				if _, err := w.Write(f.Body); err != nil {
					return status
				}
				if flusher != nil {
					flusher.Flush()
				}
			case FrameResponseEnd:
				if status == 0 {
					return writeJSONError(w, http.StatusBadGateway, "companion sent no response header")
				}
				return status
			}
		case <-ctx.Done():
			if status == 0 {
				return writeJSONError(w, http.StatusGatewayTimeout, "companion did not answer in time")
			}
			return status
		}
	}
}

func readBody(r *http.Request) ([]byte, error) {
	body := http.MaxBytesReader(nil, r.Body, MaxFrameBytes)
	defer body.Close()
	return io.ReadAll(body)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
	return status
}

// startRequest registers a pending response stream and sends the request
// frame. The caller must call finishRequest with the returned ID when done.
func (c *serverConn) startRequest(ctx context.Context, f *Frame) (chan *Frame, uint64, error) {
	ch := make(chan *Frame, 256)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, 0, errors.New("tunnel closed")
	}
	c.nextID++
	f.ID = c.nextID
	c.pending[f.ID] = ch
	c.mu.Unlock()

	if err := c.writeFrame(ctx, f); err != nil {
		c.finishRequest(f.ID)
		return nil, 0, fmt.Errorf("sending to companion: %w", err)
	}
	return ch, f.ID, nil
}

func (c *serverConn) finishRequest(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *serverConn) writeFrame(ctx context.Context, f *Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

// readLoop dispatches response frames to their waiting round trips. It
// returns when the connection dies, failing every pending request.
func (c *serverConn) readLoop(ctx context.Context) {
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			break
		}
		var f Frame
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		switch f.Type {
		case FrameResponseHeader, FrameResponseChunk, FrameResponseEnd:
		default:
			continue
		}
		c.mu.Lock()
		ch := c.pending[f.ID]
		c.mu.Unlock()
		if ch == nil {
			continue // caller gave up; drop the tail of the stream
		}
		select {
		case ch <- &f:
		default:
			// The consumer stalled far behind the companion; dropping
			// would corrupt the stream, so drop the whole request.
			c.mu.Lock()
			delete(c.pending, f.ID)
			c.mu.Unlock()
			close(ch)
		}
	}
	c.close(websocket.StatusAbnormalClosure, "read failed")
}

func (c *serverConn) close(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[uint64]chan *Frame{}
	c.mu.Unlock()

	for _, ch := range pending {
		close(ch)
	}
	c.ws.Close(code, reason)
}

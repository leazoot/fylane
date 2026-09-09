package pairclaim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const origin = "https://relay.example"

func newService(t *testing.T) (*Service, *atomic.Int32) {
	t.Helper()
	var infoCalls atomic.Int32
	s := New()
	s.AllowedOrigin = origin
	s.Info = func(_ context.Context, requestID string) (string, string, error) {
		infoCalls.Add(1)
		if requestID == "ar_unknown" {
			return "", "", errors.New("404")
		}
		return "ChatGPT", "K7-P2", nil
	}
	s.Approve = func(_ context.Context, requestID string) (string, error) {
		return "pn_test_nonce", nil
	}
	return s, &infoCalls
}

func claim(t *testing.T, h http.Handler, reqOrigin, requestID string) (*http.Response, map[string]string) {
	t.Helper()
	body := `{"request_id":"` + requestID + `"}`
	req := httptest.NewRequest("POST", "/pair/claim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if reqOrigin != "" {
		req.Header.Set("Origin", reqOrigin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]string{}
	json.NewDecoder(rec.Body).Decode(&out)
	return rec.Result(), out
}

// The loopback surface answers only the relay's own origin: a hostile local
// page (or any other site) cannot even reach the prompt machinery.
func TestClaimOriginGate(t *testing.T) {
	s, _ := newService(t)
	h := s.Handler()

	for _, o := range []string{"", "https://evil.example", "http://relay.example"} {
		resp, _ := claim(t, h, o, "ar_1")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q accepted: %d", o, resp.StatusCode)
		}
	}
	if len(s.Pending()) != 0 {
		t.Fatal("foreign origin created a prompt")
	}

	// Preflight for the allowed origin answers CORS + PNA headers.
	req := httptest.NewRequest("OPTIONS", "/pair/claim", nil)
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent ||
		rec.Header().Get("Access-Control-Allow-Origin") != origin ||
		rec.Header().Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("preflight = %d %v", rec.Code, rec.Header())
	}
}

func TestClaimApproveRejectLifecycle(t *testing.T) {
	s, infoCalls := newService(t)
	h := s.Handler()

	// Unknown request: relay lookup fails, nothing pends.
	if _, out := claim(t, h, origin, "ar_unknown"); out["status"] != "unknown" {
		t.Fatalf("unknown claim = %v", out)
	}

	// Approve path: claim blocks, desktop resolves, page gets the nonce.
	type result struct{ out map[string]string }
	done := make(chan result, 1)
	go func() {
		_, out := claim(t, h, origin, "ar_ok")
		done <- result{out}
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })
	p := s.Pending()[0]
	if p.ClientName != "ChatGPT" || p.VerifyCode != "K7-P2" {
		t.Fatalf("pending = %+v", p)
	}
	if !s.Resolve(context.Background(), "ar_ok", true) {
		t.Fatal("Resolve failed")
	}
	r := <-done
	if r.out["status"] != "approved" || r.out["nonce"] != "pn_test_nonce" {
		t.Fatalf("approved claim = %v", r.out)
	}
	if s.Resolve(context.Background(), "ar_ok", true) {
		t.Fatal("second Resolve accepted")
	}

	// Reject path: no nonce, prompt gone.
	go func() {
		_, out := claim(t, h, origin, "ar_no")
		done <- result{out}
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })
	s.Resolve(context.Background(), "ar_no", false)
	r = <-done
	if r.out["status"] != "rejected" || r.out["nonce"] != "" {
		t.Fatalf("rejected claim = %v", r.out)
	}

	// Re-claims of the same request attach to one prompt (one Info lookup,
	// one desktop prompt — the page polls, the user sees a single question).
	before := infoCalls.Load()
	go claim(t, h, origin, "ar_multi")
	waitFor(t, func() bool { return len(s.Pending()) == 1 })
	go claim(t, h, origin, "ar_multi")
	time.Sleep(50 * time.Millisecond)
	if len(s.Pending()) != 1 || infoCalls.Load() != before+1 {
		t.Fatalf("pending = %d, info calls = %d", len(s.Pending()), infoCalls.Load()-before)
	}
	s.Resolve(context.Background(), "ar_multi", false)
}

// The prompt must not outlive the request it was raised for. The
// page raised it, the user then finished the flow by typing a pairing code,
// and nothing told this side — so the prompt sat on the desktop for its full
// ten minutes, and answering it failed because the authorization request had
// already been spent.
func TestCancelWithdrawsThePromptWhenTheCodePathWins(t *testing.T) {
	s, _ := newService(t)
	h := s.Handler()

	done := make(chan map[string]string, 1)
	go func() {
		_, out := claim(t, h, origin, "ar_race")
		done <- out
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })

	body := `{"request_id":"ar_race","cancel":true}`
	req := httptest.NewRequest("POST", "/pair/claim", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel = %d", rec.Code)
	}

	if out := <-done; out["status"] != "rejected" || out["nonce"] != "" {
		t.Fatalf("the withdrawn claim was not released: %v", out)
	}
	if len(s.Pending()) != 0 {
		t.Fatalf("the prompt is still on screen: %+v", s.Pending())
	}
}

// Cancel can only withdraw a question, never answer one.
func TestCancelCannotApprove(t *testing.T) {
	s, _ := newService(t)
	h := s.Handler()

	done := make(chan map[string]string, 1)
	go func() {
		_, out := claim(t, h, origin, "ar_race2")
		done <- out
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })

	req := httptest.NewRequest("POST", "/pair/claim",
		strings.NewReader(`{"request_id":"ar_race2","cancel":true}`))
	req.Header.Set("Origin", origin)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if out := <-done; out["nonce"] != "" || out["status"] == "approved" {
		t.Fatalf("cancel produced an authorization: %v", out)
	}
}

// A failing relay approval degrades to a rejection — the page keeps its
// manual fallback, nothing hangs.
func TestApproveFailureFallsBack(t *testing.T) {
	s, _ := newService(t)
	s.Approve = func(context.Context, string) (string, error) { return "", errors.New("relay down") }
	h := s.Handler()

	done := make(chan map[string]string, 1)
	go func() {
		_, out := claim(t, h, origin, "ar_fail")
		done <- out
	}()
	waitFor(t, func() bool { return len(s.Pending()) == 1 })
	s.Resolve(context.Background(), "ar_fail", true)
	out := <-done
	if out["status"] != "rejected" || out["nonce"] != "" {
		t.Fatalf("failed approve = %v", out)
	}
}

// OnApproved is what puts a platform on the lane's "connected" list, so it
// has to describe a grant that exists: approved and bound, never merely asked
// for. Each case below drives the whole handler rather than Resolve alone —
// the hook has to survive the path the browser actually takes.
func TestOnApprovedReportsOnlyRealGrants(t *testing.T) {
	for _, tc := range []struct {
		name       string
		requestID  string
		approve    bool
		approveErr error
		want       string
	}{
		{"approved", "ar_yes", true, nil, "ChatGPT"},
		{"rejected", "ar_no", false, nil, ""},
		{"relay refused the binding", "ar_fail", true, errors.New("relay down"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newService(t)
			if tc.approveErr != nil {
				s.Approve = func(context.Context, string) (string, error) { return "", tc.approveErr }
			}
			reported := make(chan string, 1)
			s.OnApproved = func(clientName string) { reported <- clientName }
			h := s.Handler()

			done := make(chan struct{})
			go func() { claim(t, h, origin, tc.requestID); close(done) }()
			waitFor(t, func() bool { return len(s.Pending()) == 1 })
			s.Resolve(context.Background(), tc.requestID, tc.approve)
			<-done

			var got string
			select {
			case got = <-reported:
			case <-time.After(time.Second):
			}
			if got != tc.want {
				t.Errorf("reported %q, want %q", got, tc.want)
			}
		})
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

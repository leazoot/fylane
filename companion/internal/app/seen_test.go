package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leazoot/fylane/companion/internal/store"
)

// The lane's "connected" list is read from the connectors table. Before this
// existed nothing in the product ever wrote to it, so the list said "not
// connected" for a platform that was, at that moment, calling.

func TestSeenPutsThePlatformOnTheConnectedList(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	seen := seenRecorder(ctx, st, testLogger())

	seen("chatgpt")
	waitForConnector(t, st, "chatgpt")

	list, err := st.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("connectors = %d, want 1", len(list))
	}
	if list[0].Provider != "chatgpt" {
		t.Errorf("provider = %q", list[0].Provider)
	}
	// The screen reads this exact value to decide the dot.
	if list[0].Status != "active" {
		t.Errorf("status = %q; the sources screen only counts \"active\"", list[0].Status)
	}
	if list[0].LastToolCallAt.IsZero() {
		t.Error("no last-call time, so the row cannot age")
	}
}

func TestSeenDoesNotWriteOncePerCall(t *testing.T) {
	// A busy session must not turn into a database write per tool call.
	ctx := context.Background()
	st := testStore(t)
	seen := seenRecorder(ctx, st, testLogger())

	for i := 0; i < 50; i++ {
		seen("claude")
	}
	waitForConnector(t, st, "claude")

	list, _ := st.ListConnectors(ctx)
	if len(list) != 1 {
		t.Fatalf("connectors = %d, want the 50 calls to collapse into 1 row", len(list))
	}
}

func TestSeenKeepsPlatformsApart(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	seen := seenRecorder(ctx, st, testLogger())

	seen("chatgpt")
	seen("claude")
	waitForConnector(t, st, "chatgpt")
	waitForConnector(t, st, "claude")

	list, _ := st.ListConnectors(ctx)
	if len(list) != 2 {
		t.Fatalf("connectors = %d, want one per platform", len(list))
	}
}

// Pairing is what the user did; a tool call is only evidence the grant is in
// use. The list reads the first of those, so approval has to write the row.

func TestPairingPutsThePlatformOnTheConnectedList(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	pairedRecorder(ctx, st, testLogger())("ChatGPT")

	list, err := st.ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("connectors = %d, want the approval to land a row", len(list))
	}
	// The prompt shows the platform's own name; the list is keyed on the id.
	if list[0].Provider != "chatgpt" {
		t.Errorf("provider = %q, want the client name mapped to an id", list[0].Provider)
	}
	if list[0].Status != "active" {
		t.Errorf("status = %q; the sources screen only counts \"active\"", list[0].Status)
	}
	if list[0].LastConnectedAt.IsZero() {
		t.Error("no grant time, so nothing records when the user approved")
	}
	if !list[0].LastToolCallAt.IsZero() {
		t.Error("a pairing claimed the platform had called, which it has not")
	}
}

func TestPairingWithAnUnknownClientGetsNoRow(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	pairedRecorder(ctx, st, testLogger())("Some Other Tool")

	list, _ := st.ListConnectors(ctx)
	if len(list) != 0 {
		t.Fatalf("connectors = %d, want an unrecognized caller kept off the list", len(list))
	}
}

func TestPairingAndCallingDoNotEraseEachOther(t *testing.T) {
	// The two events write the same row from different sides. Before the
	// upsert kept absent timestamps, whichever happened last blanked the
	// other's — so a platform in daily use would forget it was ever paired.
	ctx := context.Background()
	st := testStore(t)

	pairedRecorder(ctx, st, testLogger())("Claude")
	seenRecorder(ctx, st, testLogger())("claude")
	if !waitFor(func() bool {
		list, _ := st.ListConnectors(ctx)
		return len(list) == 1 && !list[0].LastToolCallAt.IsZero()
	}) {
		t.Fatal("the tool call never reached the row")
	}

	list, _ := st.ListConnectors(ctx)
	if list[0].LastConnectedAt.IsZero() {
		t.Error("the tool call erased when the user approved")
	}

	pairedRecorder(ctx, st, testLogger())("Claude")
	list, _ = st.ListConnectors(ctx)
	if list[0].LastToolCallAt.IsZero() {
		t.Error("re-pairing erased the record of the platform having called")
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fylane.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Both modes build their pairing service here, so this is where the recorder
// has to be attached. Without this the hooks could be tested in isolation and
// still never be wired to anything — which is exactly how the connectors
// table stayed empty for as long as it did.
func TestPairClaimsRecordTheGrant(t *testing.T) {
	const origin = "https://relay.example"
	recorded := make(chan string, 1)
	// The announce hook is a no-op here; a real one would put a notification
	// on the screen of whoever is running the tests.
	claims := newPairClaims(testLogger(), func(name string) { recorded <- name }, func(string) {})
	claims.AllowedOrigin = origin
	claims.Info = func(context.Context, string) (string, string, error) { return "Claude", "K7-P2", nil }
	claims.Approve = func(context.Context, string) (string, error) { return "pn_nonce", nil }

	body := strings.NewReader(`{"request_id":"ar_1"}`)
	req := httptest.NewRequest("POST", "/pair/claim", body)
	req.Header.Set("Origin", origin)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { claims.Handler().ServeHTTP(httptest.NewRecorder(), req); close(done) }()

	if !waitFor(func() bool { return len(claims.Pending()) == 1 }) {
		t.Fatal("the claim never raised a prompt")
	}
	claims.Resolve(context.Background(), "ar_1", true)
	<-done

	select {
	case name := <-recorded:
		if name != "Claude" {
			t.Errorf("recorded %q", name)
		}
	case <-time.After(time.Second):
		t.Fatal("an approved pairing was not recorded, so the list stays empty")
	}
}

// The tool-call write is deliberately off the request path, so a test cannot
// read the row straight after asking for it.
func waitForConnector(t *testing.T, st *store.Store, provider string) {
	t.Helper()
	ok := waitFor(func() bool {
		list, err := st.ListConnectors(context.Background())
		if err != nil {
			return false
		}
		for _, c := range list {
			if strings.EqualFold(c.Provider, provider) {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("no connector row for %s", provider)
	}
}

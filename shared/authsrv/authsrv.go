// Package authsrv implements Fylane's OAuth 2.1 authorization server with
// PKCE and anonymous device pairing : platform connectors run
// a standard OAuth code flow, but the grant binds to a *device* — the user
// proves ownership by typing the pairing code their Companion displays. No
// product accounts exist.
//
// Security properties: S256 PKCE only, public clients only (no client
// secrets), one-time authorization codes, short-lived access tokens bound to
// one device, rotating refresh tokens with reuse detection (family
// revocation). There is no unauthenticated mode.
//
// It lives in shared/ because both deployments need it: a hosted or
// self-hosted Relay, and a Companion serving its own endpoints behind a
// tunnel. Whoever serves it, the grant only decides which device a platform
// may reach — local approval remains the only authority over file operations.
package authsrv

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Lifetimes follow current OAuth 2.1 / RFC 9700 practice.
const (
	pairingCodeTTL = 10 * time.Minute
	authRequestTTL = 10 * time.Minute
	authCodeTTL    = 5 * time.Minute
	// accessTokenTTL was 15 minutes once. Real platforms turned out
	// not to refresh on their own — three-platform testing produced a 401
	// twenty-four seconds after expiry and not one refresh_token grant in
	// the whole session — so the short lifetime did not buy security, it
	// bought a connector that appeared to break every quarter hour. Two
	// hours covers a working session; rotation still does the real work.
	accessTokenTTL  = 2 * time.Hour
	refreshTokenTTL = 30 * 24 * time.Hour
)

// Server is the authorization server. Issuer is the public base URL
// (https://relay.example); Store is required.
type Server struct {
	Issuer string
	// IssuerFn, when set, supplies the public base URL per request. A
	// Companion serving its own endpoints only learns its URL once the tunnel
	// is up, and a quick tunnel hands out a new one on every restart.
	IssuerFn func() string
	Store    Store
	// CompanionOrigin is the loopback origin the pairing page claims through.
	// Empty means DefaultCompanionOrigin. Only a Companion serving its own
	// endpoints actually knows this — a hosted relay is on another machine and
	// can do no better than the documented default, which is why the page
	// treats an unanswered claim as a normal outcome and offers the code
	// instead of failing silently.
	CompanionOrigin string
}

// DefaultCompanionOrigin is where a Companion listens unless told otherwise
// (`serve -addr`).
const DefaultCompanionOrigin = "http://127.0.0.1:8787"

func (s *Server) companionOrigin() string {
	if s.CompanionOrigin != "" {
		return s.CompanionOrigin
	}
	return DefaultCompanionOrigin
}

// issuer returns the base URL to advertise right now.
func (s *Server) issuer() string {
	if s.IssuerFn != nil {
		if u := s.IssuerFn(); u != "" {
			return u
		}
	}
	return s.Issuer
}

// Routes registers all auth endpoints on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleASMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleResourceMetadata)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("GET /authorize", s.handleAuthorizeGet)
	mux.HandleFunc("POST /authorize", s.handleAuthorizePost)
	mux.HandleFunc("POST /authorize/continue", s.handleAuthorizeContinue)
	mux.HandleFunc("GET /v1/pair/request", s.handlePairRequestInfo)
	mux.HandleFunc("POST /v1/pair/approve", s.handlePairApprove)
	mux.HandleFunc("POST /token", s.handleToken)
	mux.HandleFunc("POST /v1/devices", s.handleDeviceRegister)
	mux.HandleFunc("POST /v1/pair", s.handlePairingCode)
}

func randomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("authsrv: reading random bytes: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// verifyCode returns a short comparison word ("K7-P2") from an alphabet
// without lookalike characters. Display-only: it never authorizes anything.
func verifyCode() string {
	const alphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("authsrv: reading random bytes: %v", err))
	}
	out := make([]byte, 4)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out[:2]) + "-" + string(out[2:])
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// --- Discovery ---

func (s *Server) handleASMetadata(w http.ResponseWriter, _ *http.Request) {
	issuer := s.issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/token",
		"registration_endpoint":                 issuer + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (s *Server) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	issuer := s.issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              issuer + "/mcp",
		"authorization_servers": []string{issuer},
	})
}

// --- Dynamic client registration (RFC 7591, public clients only) ---

type registerRequest struct {
	RedirectURIs []string `json:"redirect_uris"`
	ClientName   string   `json:"client_name"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uris must be absolute https URLs")
			return
		}
	}
	client := &Client{
		ID:           "cl_" + randomToken(12),
		Name:         req.ClientName,
		RedirectURIs: req.RedirectURIs,
		CreatedAt:    time.Now(),
	}
	if err := s.Store.CreateClient(client); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "storing client failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ID,
		"client_name":                client.Name,
		"redirect_uris":              client.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
}

// --- Authorization endpoint ---

// pairingPage follows the workbench design language (the workbench boards
// tokens, lane motif, light+dark). It is fully self-contained: no scripts, no
// webfonts, no external requests of any kind.
// The connect page (the connect and confirm boards). One
// document, two layouts, because they are two paths through one flow:
//
//	Confirm — the short code is already on this machine's desktop app and the
//	          user only compares it. Single column, the code is the subject.
//	Connect — no Companion answered, so the user types a pairing code
//	          instead. Two columns: the narrative on the left, the one panel
//	          that takes input on the right.
//
// The page grants nothing. Approval for every file operation still happens in
// the desktop app; this only links a platform to a device, and the copy says
// so on both layouts.
//
// The manual form is in the markup and hidden by script, so a browser running
// no script at all still has a way through. Only one path is ever shown
// : running both at once let a user spend the request by typing a code
// while a prompt was already waiting on the desktop, which then failed.
//
// The countdown shows the request's real remaining time, which is minutes
// rather than the handoff's 120 seconds. The store expires the request on its
// own clock; a page counting to a different zero would either refuse a code
// that still worked or accept one that no longer did.
var pairingPage = template.Must(template.New("pair").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Fylane Connect</title>
<style>
:root{
 --fy-bg:#F4F1E9;--fy-bg2:#EFEBE1;--fy-tint:rgba(31,33,28,.035);--fy-inset:#E8E3D7;--fy-raised:#FCFBF7;
 --fy-ink:#1F211C;--fy-ink2:#33362F;--fy-muted:#6A6E63;--fy-faint:#7B7F71;
 --fy-rule:rgba(31,33,28,.12);--fy-rule2:rgba(31,33,28,.06);--fy-surf:rgba(31,33,28,.045);
 --fy-sage:#63805A;--fy-amber:#A87A33;--fy-brick:#9C4E36;
 --sans:-apple-system,"Segoe UI Variable Text","Segoe UI",system-ui,"Noto Sans SC",sans-serif;
 --serif:ui-serif,"Iowan Old Style","Songti SC","Noto Serif SC",Georgia,serif;
 --mono:ui-monospace,"SF Mono","Cascadia Mono",Menlo,monospace;
}
:root[data-fy="dark"]{
 --fy-bg:#171915;--fy-bg2:#1C1E1A;--fy-tint:rgba(234,236,228,.045);--fy-inset:#212420;--fy-raised:#262922;
 --fy-ink:#ECEEE6;--fy-ink2:#D3D6CC;--fy-muted:#979B8E;--fy-faint:#8D9184;
 --fy-rule:rgba(234,236,228,.14);--fy-rule2:rgba(234,236,228,.07);--fy-surf:rgba(234,236,228,.05);
 --fy-sage:#9EB98F;--fy-amber:#D0A45F;--fy-brick:#CE8768;
}
*{box-sizing:border-box}
/* An author display value beats the UA sheet's [hidden]{display:none}, and
   #connect sets display:flex — without this both paths render at once, which
   is the one thing this page must never do. */
[hidden]{display:none !important}
html,body{margin:0;min-height:100%}
body{background:var(--fy-bg);color:var(--fy-ink);font-family:var(--sans);-webkit-font-smoothing:antialiased;
 display:flex;flex-direction:column;min-height:100vh}
/* A very light depth at the top, and nothing else behind the content. */
body::before{content:"";position:fixed;inset:0 0 auto 0;height:340px;pointer-events:none;
 background:linear-gradient(var(--fy-bg2),transparent)}
.wrap{position:relative;flex:1;display:flex;flex-direction:column;padding:0 28px}
.bar{display:flex;align-items:center;gap:14px;height:44px;margin-top:26px;flex:none}
.brand{display:flex;align-items:center;gap:9px;font:500 13px/1 var(--sans)}
.gate{display:flex;gap:3px;align-items:center}
.gate i{width:2px;height:13px;border-radius:2px;background:var(--fy-ink)}
.gate i:last-child{height:9px}
.eyebrow{font:400 11.5px/1 var(--sans);letter-spacing:.13em;text-transform:uppercase;color:var(--fy-faint)}
.bar .sp{flex:1}
.lang{display:flex;gap:2px}
.lang button,.theme{border:0;background:transparent;cursor:pointer;font:400 12px/1 var(--sans);
 color:var(--fy-faint);padding:6px 8px;border-radius:6px;transition:color .16s,background .16s}
.lang button[aria-pressed="true"]{color:var(--fy-ink)}
.lang button:hover,.theme:hover{background:var(--fy-surf);color:var(--fy-ink)}
.sep{width:1px;height:14px;background:var(--fy-rule)}
main{flex:1;display:flex;align-items:center;justify-content:center;padding:18px 0 30px}
.foot{flex:none;display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:18px 0 26px;
 font:400 11.5px/1.5 var(--sans);color:var(--fy-faint);border-top:1px solid var(--fy-rule2)}
.foot .sp{flex:1}
h1{margin:0;font-family:var(--serif);font-weight:400;letter-spacing:-.015em;color:var(--fy-ink);text-wrap:balance}
.lede{font:400 15px/1.68 var(--sans);color:var(--fy-muted);text-wrap:pretty}
.tag{display:flex;align-items:center;gap:9px;font:400 12.5px/1 var(--sans);color:var(--fy-ink2)}
.tag i{width:7px;height:7px;border-radius:5px;background:var(--fy-amber);flex:none}
.tag[data-tone="sage"] i{background:var(--fy-sage)}
.tag[data-tone="brick"] i{background:var(--fy-brick)}
.tag[data-pulse="1"] i{animation:fyPulse 2.2s ease-in-out infinite}
@keyframes fyPulse{0%,100%{opacity:1}50%{opacity:.45}}
.trust{display:flex;gap:26px;flex-wrap:wrap;margin-top:28px;padding-top:20px;border-top:1px solid var(--fy-rule2)}
.trust div{flex:1 1 190px;max-width:250px}
.trust b{display:block;font:400 10.5px/1 var(--mono);letter-spacing:.1em;color:var(--fy-faint);margin-bottom:7px}
.trust p{margin:0;font:400 12.5px/1.6 var(--sans);color:var(--fy-muted);text-wrap:pretty}
.primary{height:48px;border:0;border-radius:10px;background:var(--fy-ink);color:var(--fy-bg);cursor:pointer;
 font:500 14.5px/1 var(--sans);width:100%;display:flex;align-items:center;justify-content:center;gap:9px;
 transition:transform .14s,opacity .16s}
.primary:hover{transform:translateY(-1px)}
.primary[disabled]{background:var(--fy-surf);color:var(--fy-faint);pointer-events:none;transform:none}
/* Connecting: the gate closes on itself. No spinner anywhere on this page. */
.primary .g{display:flex;gap:7px;align-items:center}
.primary .g i{width:2px;height:15px;border-radius:2px;background:currentColor}
.primary[data-busy="1"] .g{animation:fyReach .9s ease-in-out infinite}
@keyframes fyReach{0%,100%{gap:7px}50%{gap:2px}}
/* The confirm layout's own primary (Fylane-Confirm §3): not the panel button,
   which is full width because it closes a form. */
.solid{height:44px;padding:0 22px;border:0;border-radius:10px;background:var(--fy-ink);color:var(--fy-bg);
 cursor:pointer;font:500 14px/1 var(--sans);letter-spacing:.01em;transition:transform .15s ease-out}
.solid:hover{transform:translateY(-1px)}
.solid:active{transform:translateY(0)}
.linkish{border:0;background:transparent;padding:0;cursor:pointer;font:400 13px/1 var(--sans);
 color:var(--fy-ink2);border-bottom:1px solid var(--fy-rule);transition:border-color .16s}
.linkish:hover{border-bottom-color:var(--fy-ink)}
.quiet{border:0;background:transparent;padding:0;cursor:pointer;font:400 12.5px/1 var(--sans);
 color:var(--fy-faint);border-bottom:1px solid transparent}
.quiet:hover{border-bottom-color:var(--fy-faint)}
.err{margin:14px 0 0;font:400 12.5px/1.55 var(--sans);color:var(--fy-brick)}

/* ── Confirm: one column, the short code is the subject ───────────────── */
#confirm{width:100%;max-width:660px;animation:fyUp .42s cubic-bezier(.2,.8,.24,1) both}
@keyframes fyUp{from{opacity:0;transform:translateY(9px)}to{opacity:1;transform:none}}
#confirm h1{font-size:40px;margin-top:14px}
#confirm .lede{margin:14px 0 0;max-width:512px;font-size:14.5px;line-height:1.7}
.stage{margin-top:26px;background:var(--fy-tint);border-radius:16px;padding:22px 26px}
.stage .top{display:flex;align-items:baseline;justify-content:space-between;gap:16px}
.stage .top span{font:400 11.5px/1 var(--sans);letter-spacing:.13em;text-transform:uppercase;color:var(--fy-faint)}
.codewrap{display:flex;align-items:center;justify-content:center;gap:22px;padding:24px 0 22px}
.codewrap i{width:4px;height:58px;border-radius:3px;background:var(--fy-ink);flex:none;transition:height .3s,background .3s}
.codewrap[data-wait="1"] i{height:62px;animation:fySway 2.4s ease-in-out infinite}
@keyframes fySway{0%,100%{transform:translateY(0)}50%{transform:translateY(-3px)}}
#shortcode{font:450 68px/1 var(--mono);letter-spacing:.02em;color:var(--fy-ink);transition:font-size .3s,color .3s}
#shortcode .d{color:var(--fy-faint);opacity:.55;display:inline-block;transform:translateY(-.12em);margin:0 .08em}
.stage[data-tone="brick"] #shortcode{color:var(--fy-brick)}
.stage[data-tone="brick"] .codewrap i{background:var(--fy-brick)}
.stage[data-tone="faint"] #shortcode{color:var(--fy-faint)}
.stage[data-tone="faint"] .codewrap i{background:var(--fy-rule)}
.stage[data-tone="sage"] #shortcode{font-size:44px;color:var(--fy-muted)}
.stage[data-tone="sage"] .codewrap i{height:38px;background:var(--fy-sage);animation:none}
.stage[data-shake="1"]{animation:fyShake .3s cubic-bezier(.36,.07,.19,.97)}
/* How long this request has left. Empty without script — the countdown is a
   warning, not the rule; the store is what actually refuses a late one. */
.stage .top .ttl,.panel .top .ttl{font-variant-numeric:tabular-nums;text-transform:none;letter-spacing:.02em;
 transition:color .3s ease-out}
.stage .top .ttl[data-tone="soon"],.panel .top .ttl[data-tone="soon"]{color:var(--fy-amber)}
.stage .top .ttl[data-tone="gone"],.panel .top .ttl[data-tone="gone"]{color:var(--fy-brick)}
@keyframes fyShake{10%,90%{transform:translateX(-1px)}20%,80%{transform:translateX(2px)}30%,50%,70%{transform:translateX(-4px)}40%,60%{transform:translateX(4px)}}
.stage .bot{display:flex;align-items:center;gap:14px;padding-top:14px;border-top:1px solid var(--fy-rule2);
 font:400 13px/1.5 var(--sans);color:var(--fy-muted)}
/* A short track that runs back and forth while the other end is being waited on. */
.track{width:34px;height:2px;flex:none;border-radius:2px;background:var(--fy-rule);overflow:hidden;position:relative}
.track::after{content:"";position:absolute;inset:0;background:var(--fy-amber);transform-origin:left;
 animation:fyDraw 1.8s ease-in-out infinite}
@keyframes fyDraw{0%{transform:scaleX(.18)}50%{transform:scaleX(1)}100%{transform:scaleX(.18)}}
.stage[data-tone="sage"] .track::after,.stage[data-still="1"] .track::after{animation:none;background:var(--fy-rule)}
.steps{display:flex;align-items:center;gap:12px;margin-top:20px;flex-wrap:wrap}
.steps div{display:flex;align-items:center;gap:8px;font:400 12px/1 var(--sans);color:var(--fy-faint)}
.steps b{width:7px;height:7px;border-radius:5px;background:var(--fy-rule);display:block}
.steps div[data-on="1"]{color:var(--fy-ink2)}
.steps div[data-on="1"] b{background:var(--fy-sage)}
.steps div[data-now="1"] b{background:var(--fy-amber);animation:fyPulse 2.2s ease-in-out infinite}
.steps s{width:26px;height:1px;background:var(--fy-rule);display:block}
.acts{display:flex;align-items:center;gap:12px;margin-top:24px;flex-wrap:wrap}
.alt{height:44px;padding:0 15px;display:flex;align-items:center;gap:11px;border:1px solid var(--fy-rule);
 border-radius:9px;background:transparent;color:var(--fy-ink2);cursor:pointer;font:400 13.5px/1 var(--sans);
 transition:background .16s}
.alt:hover{background:var(--fy-surf)}
.alt em{width:11px;height:11px;border-radius:3px;background:var(--fy-rule);display:block}
.summary{margin-top:18px;padding-top:16px;border-top:1px solid var(--fy-rule2);display:grid;gap:8px}
.summary div{display:flex;justify-content:space-between;gap:16px;font:400 12.5px/1 var(--sans)}
.summary span:first-child{color:var(--fy-faint)}

/* ── Connect: two columns, the panel is the only surface ──────────────── */
#connect{width:100%;max-width:1060px;display:flex;gap:56px;flex-wrap:wrap;
 animation:fyUp .42s cubic-bezier(.2,.8,.24,1) both}
#connect .left{flex:1 1 400px;min-width:300px}
#connect h1{font-size:46px;margin-top:14px}
#connect .lede{margin:16px 0 0;max-width:460px}
.panel{flex:1 1 352px;min-width:300px;max-width:412px;align-self:flex-start;
 background:var(--fy-tint);border-radius:14px;padding:26px}
.panel h2{margin:0;font:400 22px/1.25 var(--serif);color:var(--fy-ink)}
.panel .top{display:flex;align-items:baseline;justify-content:space-between;gap:14px;margin-bottom:18px}
.panel .top span{font:400 11.5px/1 var(--sans);letter-spacing:.13em;text-transform:uppercase;color:var(--fy-faint)}
/* The link drawing: two endpoints and one trace. Not a topology diagram. */
.link{display:flex;align-items:center;gap:0;max-width:428px;margin-top:30px}
.link .a,.link .b{width:34px;height:34px;border-radius:9px;flex:none;display:flex;align-items:center;justify-content:center}
.link .a{background:var(--fy-raised);border:1px solid var(--fy-rule)}
.link .a em{width:13px;height:13px;border-radius:50%;border:1.5px solid var(--fy-muted);display:block}
.link .b{background:var(--fy-ink);gap:3px}
.link .b i{width:2px;border-radius:2px;background:var(--fy-bg)}
.link .b i:first-child{height:15px}
.link .b i:last-child{height:10px}
.trace{flex:1;height:1px;background:var(--fy-rule);position:relative}
.trace::after{content:"";position:absolute;inset:0;background:var(--fy-amber);transform-origin:left;opacity:0}
.link[data-state="wait"] .trace::after{opacity:1;animation:fyDraw 1.8s ease-in-out infinite}
.link[data-state="done"] .trace::after{opacity:1;background:var(--fy-sage);animation:fySeal .52s ease-out both}
@keyframes fySeal{from{transform:scaleX(0)}to{transform:scaleX(1)}}
.dot{width:7px;height:7px;border-radius:5px;background:var(--fy-rule);flex:none;margin:0 -3.5px;position:relative;z-index:1}
.link[data-state="wait"] .dot{background:var(--fy-amber)}
.link[data-state="done"] .dot{background:var(--fy-sage)}

/* The pairing code: eight slots and a hyphen, not a text box. */
.slots{display:flex;align-items:flex-end;gap:7px;margin:4px 0 0;cursor:text;position:relative}
.slots input{position:absolute;inset:0;width:100%;height:100%;opacity:0;border:0;padding:0;margin:0;
 font-size:16px;cursor:text}
.slots input:focus{outline:none}
.slot{flex:1;min-width:0}
.slot u{display:flex;align-items:center;justify-content:center;height:38px;border-radius:6px 6px 0 0;
 font:400 23px/1 var(--mono);color:var(--fy-ink);text-decoration:none;transition:background .18s}
.slot s{display:block;height:1.5px;background:var(--fy-rule);text-decoration:none;transition:all .2s}
.slot[data-filled="1"] u{background:var(--fy-surf)}
.slot[data-filled="1"] s{height:2px;background:var(--fy-ink2)}
.slots[data-focus="1"] .slot[data-at="1"] u{background:var(--fy-surf)}
.slots[data-focus="1"] .slot[data-at="1"] s{height:2.5px;background:var(--fy-ink)}
.slot[data-at="1"] u::after{content:"";width:1.5px;height:23px;background:var(--fy-ink);display:none}
.slots[data-focus="1"] .slot[data-at="1"][data-filled="0"] u::after{display:block;animation:fyCaret 1.05s steps(1) infinite}
@keyframes fyCaret{50%{opacity:0}}
.dash{width:11px;height:1.5px;background:var(--fy-rule);flex:none;margin-bottom:0}
.slots[data-bad="1"] .slot u{color:var(--fy-brick)}
.slots[data-bad="1"] .slot s{background:var(--fy-brick)}
.slots[data-bad="1"]{animation:fyShake .3s cubic-bezier(.36,.07,.19,.97)}
.slots[data-dead="1"] .slot u{color:var(--fy-faint)}
.hint{margin:12px 0 0;font:400 12.5px/1.6 var(--sans);color:var(--fy-faint);min-height:20px}
.or{display:flex;align-items:center;gap:12px;margin:20px 0 16px;font:400 11.5px/1 var(--sans);color:var(--fy-faint)}
.or s{flex:1;height:1px;background:var(--fy-rule2);display:block}
.panel .acts{margin-top:0;gap:16px}
@media (max-width:860px){
 #connect{gap:32px}
 #confirm h1{font-size:32px}
 #connect h1{font-size:34px}
 #shortcode{font-size:52px}
}
</style>
</head><body>
<div class="wrap">
 <div class="bar">
  <span class="brand"><span class="gate" aria-hidden="true"><i></i><i></i></span>Fylane</span>
  <span class="eyebrow">Connect</span>
  <span class="sp"></span>
  <span class="lang">
   <button type="button" id="zh" aria-pressed="false">中文</button>
   <button type="button" id="en" aria-pressed="true">English</button>
  </span>
  <span class="sep"></span>
  <button type="button" class="theme" id="theme" aria-label="Appearance">◐</button>
 </div>

 <main>
  <!-- Confirm: the desktop app already has the code; the user only compares. -->
  <section id="confirm" hidden>
   <div class="tag" id="ctag" data-pulse="1"><i></i><span data-k="cTag">Waiting for confirmation in the app</span></div>
   <h1 data-k="cTitle">Check that the code matches</h1>
   <p class="lede" data-k="cLede">A short code is shown in the Fylane Companion app on this computer. Compare it with the one below, then approve it there.</p>

   <div class="stage" id="stage">
    <div class="top"><span data-k="cCode">Code for this connection</span><span class="ttl" id="cttl"></span></div>
    <div class="codewrap" id="codewrap" data-wait="1">
     <i></i><span id="shortcode"></span><i></i>
    </div>
    <div class="bot"><span class="track" aria-hidden="true"></span><span id="cstate" data-k="cFinding">Contacting the Fylane Companion app…</span></div>
   </div>

   <div class="steps" aria-hidden="true">
    <div id="st1" data-on="1"><b></b><span data-k="cStep1">Contact the app</span></div><s></s>
    <div id="st2"><b></b><span data-k="cStep2">Device found</span></div><s></s>
    <div id="st3"><b></b><span data-k="cStep3">Confirm in the app</span></div>
   </div>

   <div class="acts">
    <button type="button" class="solid" id="cnew" data-k="cNew" hidden>Generate a new code</button>
    <button type="button" class="alt" id="usecode"><em></em><span data-k="cAlt">Use a pairing code instead</span></button>
   </div>

   <div class="trust">
    <div><b>01</b><p data-k="t1">This page only links the platform to this device. It grants no access to files by itself.</p></div>
    <div><b>02</b><p data-k="t2">Every file operation is still approved on this computer, one at a time.</p></div>
   </div>
  </section>

  <!-- Connect: nothing answered on this machine, so a code is typed instead. -->
  <!-- Visible in the markup on purpose: the script is what puts the page
       on one path, so a browser running none still has a way through. -->
  <section id="connect">
   <div class="left">
    <div class="tag" data-tone="" id="ntag"><i></i><span data-k="nTag">Link this browser to your computer</span></div>
    <h1 data-k="nTitle">Connect this device</h1>
    <p class="lede" id="nlede"></p>

    <div class="link" id="link" aria-hidden="true">
     <span class="a"><em></em></span><span class="trace"></span><span class="dot"></span><span class="trace"></span>
     <span class="b"><i></i><i></i></span>
    </div>

    <div class="trust">
     <div><b>01</b><p data-k="t1">This page only links the platform to this device. It grants no access to files by itself.</p></div>
     <div><b>02</b><p data-k="t2">Every file operation is still approved on this computer, one at a time.</p></div>
     <div><b>03</b><p data-k="t3">The code works once, on this computer, and expires in minutes.</p></div>
    </div>
   </div>

   <form id="manual" class="panel" method="POST" action="/authorize">
    <input type="hidden" name="request_id" value="{{.RequestID}}">
    <div class="top"><h2 data-k="nPanel">Pairing code</h2><span class="ttl" id="nttl"></span></div>
    <div class="slots" id="slots" data-focus="0">
     <input id="pairing_code" name="pairing_code" autocomplete="one-time-code" spellcheck="false"
            autocapitalize="characters" maxlength="9" aria-label="Pairing code" required>
    </div>
    <p class="hint" id="hint"></p>
    {{if .Error}}<p class="err" role="alert">{{.Error}}</p>{{end}}
    <button type="submit" class="primary" id="go" disabled>
     <span id="golabel" data-k="nGo">Connect</span>
    </button>
    <div class="or"><s></s><span data-k="nOr">or</span><s></s></div>
    <div class="acts">
     <button type="button" class="linkish" id="useapp" data-k="nApp">Approve in the Companion app</button>
     <button type="button" class="quiet" id="newcode" data-k="nNew">Get a new code</button>
    </div>
   </form>
  </section>
 </main>

 <div class="foot">
  <span data-k="fLocal">Fylane · runs on this machine, stays on this machine</span>
  <span class="sp"></span>
  <span data-k="fHelp">Trouble connecting</span><span class="sep"></span><span data-k="fPriv">Privacy</span>
 </div>
</div>

<form id="cont" method="POST" action="/authorize/continue" hidden>
 <input type="hidden" name="request_id" value="{{.RequestID}}">
 <input type="hidden" name="nonce" id="nonce" value="">
</form>
<script>
// Push pairing: the Companion listens on this machine's loopback. Only a
// browser on the same machine can reach it — that is the whole binding. No
// external requests are made from this page.
(function () {
 var VERIFY = {{.Verify}}, WHO = {{.ClientName}}, RID = {{.RequestID}}, CLAIM = {{.ClaimURL}};
 // Seconds left on this authorization request, from the server. Not a
 // constant here: the page must not invent a deadline the store disagrees
 // with, and after a rejected code the server re-arms the request.
 var SECS = {{.ExpiresIn}};
 var D = {
  en: {
   cTag:"Waiting for confirmation in the app", cTitle:"Check that the code matches",
   cLede:"A short code is shown in the Fylane Companion app on this computer. Compare it with the one below, then approve it there.",
   cCode:"Code for this connection", cFinding:"Contacting the Fylane Companion app…",
   cFound:"Companion found · waiting for you to approve in the app",
   cStep1:"Contact the app", cStep2:"Device found", cStep3:"Confirm in the app",
   cAlt:"Use a pairing code instead",
   cNoAnswer:"The Companion app is not answering on this computer",
   cNoAnswerLede:"Nothing on this machine responded. Open the Fylane Companion app, or connect with a pairing code instead.",
   cRejected:"That request was declined in the app",
   cRejectedLede:"Nothing was connected. You can start again from the platform.",
   nTag:"Link this browser to your computer", nTitle:"Connect this device",
   nPanel:"Pairing code", nGo:"Connect", nOr:"or",
   nApp:"Approve in the Companion app", nNew:"Get a new code",
   nHint:"Open the Fylane Companion app and enter the code it shows.",
   nBad:"That code was not accepted. Check it and try again.",
   xTtl:"Expires in", xGone:"Expired",
   xExpired:"This request has expired. Generate a new code and try again.",
   cNew:"Generate a new code", cExpTag:"Code expired", cExpTitle:"This code has expired",
   cExpLede:"A request lasts a few minutes. Generate a new one and the Companion app will show a new code too.",
   cExpStatus:"Code void · nothing was linked",
   t1:"This page only links the platform to this device. It grants no access to files by itself.",
   t2:"Every file operation is still approved on this computer, one at a time.",
   t3:"The code works once, on this computer, and expires in minutes.",
   fLocal:"Fylane · runs on this machine, stays on this machine",
   fHelp:"Trouble connecting", fPriv:"Privacy",
   lede:function(w){return w + " wants to reach a Fylane workspace on your computer. Approving links the two; it hands over no files."}
  },
  zh: {
   cTag:"等待在 App 中确认", cTitle:"核对短码是否一致",
   cLede:"本机的 Fylane Companion 中会显示一组短码。核对无误后，在那边确认。",
   cCode:"本次连接短码", cFinding:"正在联系本机的 Fylane Companion…",
   cFound:"已找到 Companion · 等待你在 App 中确认",
   cStep1:"联系 Companion", cStep2:"找到设备", cStep3:"在 App 中确认",
   cAlt:"改为输入配对码",
   cNoAnswer:"Companion 没有回应",
   cNoAnswerLede:"本机没有任何程序应答。请打开 Fylane Companion，或改用配对码连接。",
   cRejected:"这次请求已在 App 中被拒绝",
   cRejectedLede:"什么都没有连接。可以从平台那边重新开始。",
   nTag:"把这个浏览器连接到你的电脑", nTitle:"连接这台设备",
   nPanel:"配对码", nGo:"连接", nOr:"或",
   nApp:"在 Companion 中批准", nNew:"获取新配对码",
   nHint:"打开本机的 Fylane Companion，输入它显示的配对码。",
   nBad:"配对码不正确。核对后重试。",
   xTtl:"有效期", xGone:"已过期",
   xExpired:"这次请求已经过期。生成一组新的短码再试。",
   cNew:"生成新短码", cExpTag:"短码已过期", cExpTitle:"这组短码已经过期",
   cExpLede:"一次请求只在几分钟内有效。重新生成一次，Companion 里的短码也会跟着更新。",
   cExpStatus:"短码失效 · 未建立连接",
   t1:"这一页只把平台与这台设备相连，它本身不授予任何文件权限。",
   t2:"每一次文件操作仍然在这台电脑上逐条审批。",
   t3:"配对码只能用一次，只在这台电脑上有效，几分钟后过期。",
   fLocal:"Fylane · 本机执行，本地留存",
   fHelp:"遇到问题", fPriv:"隐私说明",
   lede:function(w){return w + " 想要访问你电脑上的 Fylane 工作区。批准只是把两边连起来，不会交出任何文件。"}
  }
 };
 var lang = (navigator.language || "en").toLowerCase().indexOf("zh") === 0 ? "zh" : "en";
 var root = document.documentElement;

 function paint() {
  var d = D[lang];
  root.lang = lang === "zh" ? "zh" : "en";
  var nodes = document.querySelectorAll("[data-k]");
  for (var i = 0; i < nodes.length; i++) {
   var k = nodes[i].getAttribute("data-k");
   if (d[k]) nodes[i].textContent = d[k];
  }
  document.getElementById("nlede").textContent = d.lede(WHO);
  document.getElementById("zh").setAttribute("aria-pressed", lang === "zh" ? "true" : "false");
  document.getElementById("en").setAttribute("aria-pressed", lang === "en" ? "true" : "false");
  if (expired) document.getElementById("hint").textContent = d.xExpired;
  else if (!badShown) document.getElementById("hint").textContent = d.nHint;
  clock();
 }
 document.getElementById("zh").onclick = function () { lang = "zh"; paint(); };
 document.getElementById("en").onclick = function () { lang = "en"; paint(); };

 // Appearance follows the system and can be overridden here, the way the
 // desktop window does it.
 var mq = window.matchMedia("(prefers-color-scheme: dark)");
 var forced = null;
 function theme() { root.setAttribute("data-fy", (forced === null ? mq.matches : forced) ? "dark" : "light"); }
 mq.addEventListener("change", function () { if (forced === null) theme(); });
 document.getElementById("theme").onclick = function () {
  forced = !(forced === null ? mq.matches : forced); theme();
 };
 theme();

 // The short code, with the hyphen set apart.
 var sc = document.getElementById("shortcode");
 var parts = String(VERIFY).split("-");
 if (parts.length === 2) {
  sc.appendChild(document.createTextNode(parts[0]));
  var d0 = document.createElement("span"); d0.className = "d"; d0.textContent = "–";
  sc.appendChild(d0); sc.appendChild(document.createTextNode(parts[1]));
 } else { sc.textContent = VERIFY; }

 var confirm = document.getElementById("confirm");
 var connect = document.getElementById("connect");
 var manual = document.getElementById("manual");
 var stage = document.getElementById("stage");
 var badShown = false;

 function showConfirm() {
  confirm.hidden = false;
  connect.hidden = true;
  manual.hidden = true;
 }
 function showConnect(reason) {
  connect.hidden = false; manual.hidden = false; confirm.hidden = true;
  if (reason) { document.getElementById("hint").textContent = reason; badShown = true; }
  setTimeout(function () { input.focus(); }, 60);
 }

 // ── the eight slots ────────────────────────────────────────────────────
 var slots = document.getElementById("slots");
 var input = document.getElementById("pairing_code");
 var go = document.getElementById("go");
 var cells = [];
 for (var s = 0; s < 8; s++) {
  if (s === 4) { var dash = document.createElement("span"); dash.className = "dash"; slots.appendChild(dash); }
  var cell = document.createElement("span");
  cell.className = "slot"; cell.setAttribute("data-filled", "0");
  var u = document.createElement("u"), line = document.createElement("s");
  cell.appendChild(u); cell.appendChild(line); slots.appendChild(cell); cells.push(cell);
 }
 function value() { return input.value.replace(/[^A-Z0-9]/g, "").slice(0, 8); }
 function render() {
  var v = value();
  for (var i = 0; i < 8; i++) {
   cells[i].firstChild.textContent = v[i] || "";
   cells[i].setAttribute("data-filled", v[i] ? "1" : "0");
   cells[i].setAttribute("data-at", i === Math.min(v.length, 7) ? "1" : "0");
  }
  go.disabled = v.length !== 8;
 }
 input.addEventListener("input", function () {
  var v = input.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 8);
  input.value = v;
  slots.setAttribute("data-bad", "0");
  render();
 });
 input.addEventListener("focus", function () { slots.setAttribute("data-focus", "1"); });
 input.addEventListener("blur", function () { slots.setAttribute("data-focus", "0"); });
 slots.addEventListener("click", function () { input.focus(); });
 manual.addEventListener("submit", function () {
  cancelClaim();
  go.setAttribute("data-busy", "1");
  var label = document.getElementById("golabel");
  label.textContent = "";
  var g = document.createElement("span"); g.className = "g";
  g.appendChild(document.createElement("i")); g.appendChild(document.createElement("i"));
  label.appendChild(g);
 });
 render();

 document.getElementById("usecode").onclick = function () { cancelClaim(); showConnect(D[lang].nHint); badShown = false; };
 document.getElementById("useapp").onclick = function () { location.reload(); };
 document.getElementById("newcode").onclick = function () { location.reload(); };
 // Reloading /authorize with the same query mints a fresh request, so this
 // really does hand back a new code rather than redisplay the dead one.
 document.getElementById("cnew").onclick = function () { location.reload(); };

 // ── how long this request has left ─────────────────────────────────────
 // The design asks for a 1:58-style countdown. The number is the request's
 // real remaining time, not the design's 120s: the store expires the request
 // on its own clock, and a page counting to a different zero would either
 // refuse a code that still worked or accept one that no longer did.
 var claimed = false, stopped = false, expired = false, declined = false;
 var deadline = Date.now() + SECS * 1000;

 function clock() {
  // A declined request already has its final answer. Letting the clock run
  // on would replace "declined in the app" with "expired" a few minutes
  // later, which is not what happened.
  if (declined) return;
  var left = Math.max(0, Math.round((deadline - Date.now()) / 1000));
  var d = D[lang], tone = "", text;
  if (left === 0) {
   tone = "gone"; text = d.xGone;
  } else {
   if (left < 30) tone = "soon";
   text = d.xTtl + " " + Math.floor(left / 60) + ":" + ("0" + (left % 60)).slice(-2);
  }
  var spans = [document.getElementById("cttl"), document.getElementById("nttl")];
  for (var i = 0; i < spans.length; i++) {
   spans[i].textContent = text;
   spans[i].setAttribute("data-tone", tone);
  }
  if (expired) expire(d);
  if (left === 0 && !expired) {
   expired = true;
   stopped = true;
   cancelClaim();
   // Nothing here can still succeed, so nothing here still offers to try.
   go.disabled = true;
   input.disabled = true;
   badShown = true;
   stage.setAttribute("data-tone", "faint");
   stage.setAttribute("data-still", "1");
   document.getElementById("codewrap").setAttribute("data-wait", "0");
   document.getElementById("ctag").setAttribute("data-pulse", "0");
   document.getElementById("cnew").hidden = false;
   step(1);
   expire(d);
  }
 }

 // The expired wording, applied on the way in and again on a language switch.
 function expire(d) {
  document.getElementById("hint").textContent = d.xExpired;
  document.querySelector("#ctag span").textContent = d.cExpTag;
  document.querySelector("#confirm h1").textContent = d.cExpTitle;
  document.querySelector("#confirm .lede").textContent = d.cExpLede;
  document.getElementById("cstate").textContent = d.cExpStatus;
 }

 // ── the claim ──────────────────────────────────────────────────────────

 function cancelClaim() {
  if (!claimed) return;
  claimed = false;
  try {
   fetch(CLAIM, {
    method: "POST", keepalive: true,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ request_id: RID, cancel: true })
   }).catch(function () {});
  } catch (e) {}
 }

 function step(n) {
  for (var i = 1; i <= 3; i++) {
   var el = document.getElementById("st" + i);
   el.setAttribute("data-on", i <= n ? "1" : "0");
   el.setAttribute("data-now", i === n ? "1" : "0");
  }
 }

 function attempt() {
  if (stopped) return;
  if (Date.now() > deadline) { showConnect(D[lang].nHint); return; }
  // Marked claimed before the answer, not after: the Companion raises the
  // desktop prompt the moment this request lands, and the first response may
  // be half a minute away. A user who types a code in that window still has
  // to cancel a prompt that already exists.
  claimed = true;
  fetch(CLAIM, {
   method: "POST",
   headers: { "Content-Type": "application/json" },
   body: JSON.stringify({ request_id: RID, verify_code: VERIFY })
  }).then(function (r) { return r.json(); }).then(function (d) {
   if (stopped) return;
   if (d && d.nonce) {
    stage.setAttribute("data-tone", "sage");
    stage.setAttribute("data-still", "1");
    document.getElementById("codewrap").setAttribute("data-wait", "0");
    step(3);
    // The nonce is what proves the approval happened on this machine. The
    // continue handler rejects the request without it, so it goes into the
    // form before the form is submitted, not after.
    document.getElementById("nonce").value = d.nonce;
    document.getElementById("cont").submit();
    return;
   }
   if (d && d.status === "rejected") {
    claimed = false;
    stopped = true;
    declined = true;
    stage.setAttribute("data-tone", "faint");
    stage.setAttribute("data-still", "1");
    document.getElementById("ctag").setAttribute("data-tone", "brick");
    document.getElementById("ctag").setAttribute("data-pulse", "0");
    document.querySelector("#confirm h1").textContent = D[lang].cRejected;
    document.querySelector("#confirm .lede").textContent = D[lang].cRejectedLede;
    document.getElementById("cstate").textContent = D[lang].cRejected;
    return;
   }
   step(3);
   document.getElementById("cstate").textContent = D[lang].cFound;
   setTimeout(attempt, 1500);
  }).catch(function () {
   // No Companion answering on this machine — a different port, or the app
   // is not running. Nothing was raised, so there is nothing to cancel. The
   // code is the way through, and saying so beats a box that never resolves.
   claimed = false;
   showConnect(D[lang].nHint);
  });
 }

 paint();
 setInterval(clock, 1000);
 showConfirm();
 step(1);
 attempt();
})();
</script>
</body></html>`))

func (s *Server) handleAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	client, err := s.Store.GetClient(q.Get("client_id"))
	if errors.Is(err, ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "unknown client_id")
		return
	}
	if err != nil {
		storeFailed(w)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		// Never redirect to an unregistered URI.
		oauthError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered")
		return
	}
	if q.Get("response_type") != "code" {
		s.redirectError(w, r, redirectURI, q.Get("state"), "unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		s.redirectError(w, r, redirectURI, q.Get("state"), "invalid_request")
		return
	}
	req := &AuthRequest{
		ID:            "ar_" + randomToken(16),
		ClientID:      client.ID,
		RedirectURI:   redirectURI,
		CodeChallenge: q.Get("code_challenge"),
		State:         q.Get("state"),
		VerifyCode:    verifyCode(),
		ExpiresAt:     time.Now().Add(authRequestTTL),
	}
	if err := s.Store.PutAuthRequest(req); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "storing request failed")
		return
	}
	s.renderPairingPage(w, client.Name, req.ID, req.VerifyCode, "", time.Until(req.ExpiresAt))
}

// renderPairingPage draws the connect page for one authorization request.
// expiresIn is how long that request has left, which is the page's countdown
// and also the moment it stops offering to submit: past it the store refuses
// the request, so a form that still looked usable would only produce an error
// the user could have been told about a minute earlier.
func (s *Server) renderPairingPage(w http.ResponseWriter, clientName, requestID, verify, errMsg string, expiresIn time.Duration) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	seconds := int(expiresIn.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	pairingPage.Execute(w, map[string]any{
		"ClientName": clientName, "RequestID": requestID, "Verify": verify, "Error": errMsg,
		"ClaimURL": s.companionOrigin() + "/pair/claim", "ExpiresIn": seconds,
	})
}

func (s *Server) handleAuthorizePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	req, err := s.Store.TakeAuthRequest(r.PostFormValue("request_id"), time.Now())
	if errors.Is(err, ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "authorization request expired; restart the connection")
		return
	}
	if err != nil {
		storeFailed(w)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PostFormValue("pairing_code")))
	pairing, err := s.Store.TakePairingCode(code, time.Now())
	if err != nil && !errors.Is(err, ErrNotFound) {
		storeFailed(w)
		return
	}
	if err != nil {
		// Re-arm the request so the user can retry with a fresh code.
		req.ExpiresAt = time.Now().Add(authRequestTTL)
		s.Store.PutAuthRequest(req)
		client, _ := s.Store.GetClient(req.ClientID)
		name := ""
		if client != nil {
			name = client.Name
		}
		s.renderPairingPage(w, name, req.ID, req.VerifyCode,
			"That pairing code is not valid or has expired. Generate a new code in Fylane Companion and try again.",
			time.Until(req.ExpiresAt))
		return
	}

	s.issueAuthCode(w, r, req, pairing.DeviceID)
}

// issueAuthCode mints the one-time authorization code (stored hashed) and
// sends the standard redirect. Shared by the typed-code and push paths.
func (s *Server) issueAuthCode(w http.ResponseWriter, r *http.Request, req *AuthRequest, deviceID string) {
	rawCode := "ac_" + randomToken(24)
	authCode := &AuthCode{
		Code:          hashSecret(rawCode),
		ClientID:      req.ClientID,
		DeviceID:      deviceID,
		RedirectURI:   req.RedirectURI,
		CodeChallenge: req.CodeChallenge,
		ExpiresAt:     time.Now().Add(authCodeTTL),
	}
	if err := s.Store.PutAuthCode(authCode); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "storing code failed")
		return
	}
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	q.Set("code", rawCode)
	if req.State != "" {
		q.Set("state", req.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// --- Push pairing: the browser page claims the flow through the
// Companion's loopback listener; the Companion fetches authoritative request
// details here, approves with its device credentials, and hands the one-time
// continuation nonce back to the page. ---

// ErrAlreadyApproved reports that an authorization request has already been
// bound to a device; a second approval is not a retry.
var ErrAlreadyApproved = errors.New("authorization request already approved")

// RequestInfo returns the authoritative client name and verify code of a
// pending, unbound authorization request. Callers must have established the
// caller's right to see it: over HTTP that is device credentials, in process
// it is the Companion asking about its own pending prompt.
func (s *Server) RequestInfo(requestID string) (clientName, verify string, err error) {
	req, err := s.Store.GetAuthRequest(requestID, time.Now())
	if err != nil {
		return "", "", err
	}
	if req.DeviceID != "" {
		return "", "", ErrAlreadyApproved
	}
	if client, cerr := s.Store.GetClient(req.ClientID); cerr == nil && client != nil {
		clientName = client.Name
	}
	return clientName, req.VerifyCode, nil
}

// ApproveRequest binds deviceID to a pending request and returns the one-time
// continuation nonce (stored hashed, like every credential).
func (s *Server) ApproveRequest(requestID, deviceID string) (nonce string, err error) {
	rawNonce := "pn_" + randomToken(24)
	if err := s.Store.BindAuthRequest(requestID, deviceID, hashSecret(rawNonce), time.Now()); err != nil {
		return "", err
	}
	return rawNonce, nil
}

// IssuePairingCode mints a short-lived, single-use pairing code for deviceID.
func (s *Server) IssuePairingCode(deviceID string) (string, time.Duration, error) {
	code := &PairingCode{
		Code:      humanCode(),
		DeviceID:  deviceID,
		ExpiresAt: time.Now().Add(pairingCodeTTL),
	}
	if err := s.Store.PutPairingCode(code); err != nil {
		return "", 0, err
	}
	return code.Code, pairingCodeTTL, nil
}

// handlePairRequestInfo returns the authoritative client name and verify
// code for a pending, unbound request. Device credentials required — random
// web pages cannot enumerate authorization requests.
func (s *Server) handlePairRequestInfo(w http.ResponseWriter, r *http.Request) {
	if _, err := s.AuthenticateDevice(r); err != nil {
		if errors.Is(err, ErrStoreUnavailable) {
			storeFailed(w)
			return
		}
		oauthError(w, http.StatusUnauthorized, "invalid_client", "device credentials required")
		return
	}
	name, verify, err := s.RequestInfo(r.URL.Query().Get("request_id"))
	switch {
	case errors.Is(err, ErrNotFound):
		oauthError(w, http.StatusNotFound, "invalid_request", "unknown or expired authorization request")
		return
	case errors.Is(err, ErrAlreadyApproved):
		oauthError(w, http.StatusConflict, "invalid_request", "request already approved")
		return
	case err != nil:
		storeFailed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"client_name": name,
		"verify_code": verify,
	})
}

// handlePairApprove binds the authenticated device to the request and mints
// the single-use continuation nonce (stored hashed, like every credential).
func (s *Server) handlePairApprove(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.AuthenticateDevice(r)
	if err != nil {
		if errors.Is(err, ErrStoreUnavailable) {
			storeFailed(w)
			return
		}
		oauthError(w, http.StatusUnauthorized, "invalid_client", "device credentials required")
		return
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RequestID == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "request_id is required")
		return
	}
	nonce, err := s.ApproveRequest(body.RequestID, deviceID)
	if errors.Is(err, ErrNotFound) {
		oauthError(w, http.StatusConflict, "invalid_request", "request unknown, expired, or already approved")
		return
	}
	if err != nil {
		storeFailed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"nonce": nonce})
}

// handleAuthorizeContinue completes a push-paired authorization: the page
// presents the one-time nonce and receives the standard code redirect.
func (s *Server) handleAuthorizeContinue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	req, err := s.Store.TakeAuthRequest(r.PostFormValue("request_id"), time.Now())
	if errors.Is(err, ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "authorization request expired; restart the connection")
		return
	}
	if err != nil {
		storeFailed(w)
		return
	}
	nonce := r.PostFormValue("nonce")
	if req.DeviceID == "" || req.NonceHash == "" || nonce == "" ||
		subtle.ConstantTimeCompare([]byte(hashSecret(nonce)), []byte(req.NonceHash)) != 1 {
		// Consuming the request on a bad nonce is deliberate: a guessed or
		// replayed nonce kills the flow instead of leaving it retryable.
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid continuation")
		return
	}
	s.issueAuthCode(w, r, req, req.DeviceID)
}

func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		oauthError(w, http.StatusBadRequest, code, "")
		return
	}
	q := u.Query()
	q.Set("error", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// --- Token endpoint ---

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	code, err := s.Store.TakeAuthCode(hashSecret(r.PostFormValue("code")), time.Now())
	if errors.Is(err, ErrNotFound) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
		return
	}
	if err != nil {
		storeFailed(w)
		return
	}
	if r.PostFormValue("client_id") != code.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client mismatch")
		return
	}
	if uri := r.PostFormValue("redirect_uri"); uri != "" && uri != code.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	verifier := r.PostFormValue("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if verifier == "" || subtle.ConstantTimeCompare([]byte(challenge), []byte(code.CodeChallenge)) != 1 {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.issueTokens(w, code.DeviceID, code.ClientID, "fam_"+randomToken(12))
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	token, err := s.Store.GetRefreshToken(hashSecret(r.PostFormValue("refresh_token")))
	if err != nil && !errors.Is(err, ErrNotFound) {
		storeFailed(w)
		return
	}
	if err != nil || token.Revoked || time.Now().After(token.ExpiresAt) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid or expired")
		return
	}
	if r.PostFormValue("client_id") != token.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client mismatch")
		return
	}
	if token.Used {
		// Rotation reuse: someone replayed an old refresh token. Revoke the
		// whole family (RFC 9700). This is a security event worth a trace —
		// no identifiers or token material in the line.
		//
		// The refusal stands either way: a used token is invalid whether or
		// not the family could be revoked, and answering 503 here would
		// invite the replayer to try again. What must not happen is telling
		// the caller that sessions were revoked when the store was down and
		// none of them were — an outage would then read, in the log and in
		// the response, exactly like a handled incident.
		if _, err := s.Store.RevokeRefreshFamily(token.Family); err != nil {
			log.Printf("authsrv: refresh token reuse detected; REVOCATION FAILED, family still live: %v", err)
			oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected")
			return
		}
		log.Printf("authsrv: refresh token reuse detected; token family revoked")
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token reuse detected; all sessions revoked")
		return
	}
	if err := s.Store.MarkRefreshTokenUsed(token.Token); err != nil {
		// A store that cannot record the rotation is a store outage like any
		// other, and it gets the same retryable answer. Answering 500 here
		// was not harmful — a platform only discards credentials on
		// invalid_grant — but it was the one store failure in this file that
		// did not say "come back shortly".
		storeFailed(w)
		return
	}
	s.issueTokens(w, token.DeviceID, token.ClientID, token.Family)
}

func (s *Server) issueTokens(w http.ResponseWriter, deviceID, clientID, family string) {
	// Only hashes reach the store; the raw tokens exist in this response
	// alone, so a compromised metadata store yields no live credentials.
	rawAccess := "at_" + randomToken(32)
	rawRefresh := "rt_" + randomToken(32)
	access := &AccessToken{
		Token:     hashSecret(rawAccess),
		DeviceID:  deviceID,
		ClientID:  clientID,
		ExpiresAt: time.Now().Add(accessTokenTTL),
	}
	refresh := &RefreshToken{
		Token:     hashSecret(rawRefresh),
		Family:    family,
		DeviceID:  deviceID,
		ClientID:  clientID,
		ExpiresAt: time.Now().Add(refreshTokenTTL),
	}
	if err := s.Store.PutAccessToken(access); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if err := s.Store.PutRefreshToken(refresh); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  rawAccess,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"refresh_token": rawRefresh,
	})
}

// --- Device pairing ---

type deviceRegisterResponse struct {
	DeviceID     string `json:"device_id"`
	DeviceSecret string `json:"device_secret"`
}

// handleDeviceRegister creates device credentials for a Companion. The
// secret is returned exactly once and stored only as a hash.
func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	secret := randomToken(32)
	device := &Device{
		ID:         "dev_" + randomToken(12),
		SecretHash: hashSecret(secret),
		Name:       req.Name,
		CreatedAt:  time.Now(),
	}
	if err := s.Store.CreateDevice(device); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, deviceRegisterResponse{DeviceID: device.ID, DeviceSecret: secret})
}

// handlePairingCode issues a short-lived pairing code for the authenticated
// device (the Companion displays it to the user).
func (s *Server) handlePairingCode(w http.ResponseWriter, r *http.Request) {
	deviceID, err := s.AuthenticateDevice(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		oauthError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	code, ttl, err := s.IssuePairingCode(deviceID)
	if err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairing_code": code,
		"expires_in":   int(ttl.Seconds()),
	})
}

// humanCode returns an 8-character code from an unambiguous alphabet
// (no 0/O/1/I), grouped for reading, e.g. "K3ZM-7PWQ".
func humanCode() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("authsrv: reading random bytes: %v", err))
	}
	out := make([]byte, 0, 9)
	for i, b := range buf {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(b)%len(alphabet)])
	}
	return string(out)
}

// AuthenticateDevice validates "Authorization: Bearer <device_id>:<secret>"
// device credentials (used by the pairing endpoint and the tunnel).
func (s *Server) AuthenticateDevice(r *http.Request) (string, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", errors.New("missing device credentials")
	}
	id, secret, ok := strings.Cut(raw, ":")
	if !ok {
		return "", errors.New("malformed device credentials")
	}
	device, err := s.Store.GetDevice(id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	if err != nil {
		// Compare against a dummy hash anyway to keep timing uniform.
		subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(hashSecret("x")))
		return "", errors.New("unknown device")
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(device.SecretHash)) != 1 {
		return "", errors.New("invalid device credentials")
	}
	return device.ID, nil
}

// ValidateBearer checks the platform-side access token on an MCP request and
// returns the bound device ID. On failure the caller must send 401 with the
// resource-metadata pointer so clients can discover the auth server.
func (s *Server) ValidateBearer(r *http.Request) (string, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return "", errors.New("missing bearer token")
	}
	token, err := s.Store.GetAccessToken(hashSecret(raw), time.Now())
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	if err != nil {
		return "", errors.New("invalid or expired access token")
	}
	return token.DeviceID, nil
}

// ValidateBearerProvider is ValidateBearer plus the platform inferred from
// the OAuth client the token was issued to (DCR client_name/redirect_uris).
// The inference selects an approval budget downstream; it is a hint, never
// an authorization signal.
func (s *Server) ValidateBearerProvider(r *http.Request) (deviceID, provider string, err error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return "", "", errors.New("missing bearer token")
	}
	token, terr := s.Store.GetAccessToken(hashSecret(raw), time.Now())
	if terr != nil && !errors.Is(terr, ErrNotFound) {
		return "", "", fmt.Errorf("%w: %v", ErrStoreUnavailable, terr)
	}
	if terr != nil {
		return "", "", errors.New("invalid or expired access token")
	}
	provider = "unknown"
	if client, cerr := s.Store.GetClient(token.ClientID); cerr == nil && client != nil {
		provider = inferProvider(client.Name, client.RedirectURIs)
	}
	return token.DeviceID, provider, nil
}

// inferProvider maps DCR client metadata to a known platform name. Values
// match approval.DefaultBudgets keys.
func inferProvider(name string, redirectURIs []string) string {
	return ProviderFromClient(name, redirectURIs)
}

// ProviderFromClient is inferProvider for callers outside this package. The
// Companion needs the same answer when it records who it just paired with,
// and a second copy of this table would drift from the one the relay stamps
// requests with — the two would then disagree about who is connected.
func ProviderFromClient(name string, redirectURIs []string) string {
	blob := strings.ToLower(name)
	for _, u := range redirectURIs {
		blob += " " + strings.ToLower(u)
	}
	switch {
	case strings.Contains(blob, "claude") || strings.Contains(blob, "anthropic"):
		return "claude"
	case strings.Contains(blob, "chatgpt") || strings.Contains(blob, "openai"):
		return "chatgpt"
	case strings.Contains(blob, "grok") || strings.Contains(blob, "x.ai"):
		return "grok"
	}
	return "unknown"
}

// ErrStoreUnavailable signals that a credential could not be checked
// because the store itself failed. Callers answer a retryable 503 — never
// an OAuth invalid_* code, which would make platforms discard good tokens.
var ErrStoreUnavailable = errors.New("credential store unavailable")

// storeFailed answers a retryable 503 for credential-store outages.
func storeFailed(w http.ResponseWriter) {
	oauthError(w, http.StatusServiceUnavailable, "temporarily_unavailable",
		"credential store unavailable; retry shortly")
}

// WWWAuthenticate is the challenge header value for 401 responses on the
// protected MCP resource (RFC 9728 discovery).
func (s *Server) WWWAuthenticate() string {
	return fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, s.issuer())
}

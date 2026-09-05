package relay

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed web/gate.html
var gatePageTemplateSource string

//go:embed web/dashboard.html
var dashboardHTML []byte

// maxSnapshotBodyBytes bounds the size of an accepted POST /api/v1/snapshot
// body, protecting the Relay from unbounded memory use on a malformed or
// hostile request.
const maxSnapshotBodyBytes = 2 << 20 // 2 MiB

// gateContentSecurityPolicy locks the unauthenticated gate page down to the
// bare minimum it needs: an inline verification script and a same-origin
// fetch to POST /api/v1/session. Nothing else is allowed to load or render.
const gateContentSecurityPolicy = "default-src 'none'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Handler serves the Relay's HTTP API and embedded pages.
type Handler struct {
	store       *SnapshotStore
	token       string
	redirectURL string
	sessionKey  []byte
	gatePage    []byte
	logger      *slog.Logger
	now         func() time.Time
}

// NewHandler constructs a Handler backed by store, authenticating
// Connector/session requests against token and sending unauthenticated or
// failed-verification browser requests to redirectURL. It returns an error
// if the embedded gate page template fails to render.
func NewHandler(store *SnapshotStore, token, redirectURL string, logger *slog.Logger) (*Handler, error) {
	gatePage, err := renderGatePage(redirectURL)
	if err != nil {
		return nil, fmt.Errorf("relay: construct handler: %w", err)
	}
	return &Handler{
		store:       store,
		token:       token,
		redirectURL: redirectURL,
		sessionKey:  deriveSessionKey(token),
		gatePage:    gatePage,
		logger:      logger,
		now:         time.Now,
	}, nil
}

// renderGatePage renders the generic, product-agnostic gate page once at
// startup, substituting redirectURL into the embedded template.
func renderGatePage(redirectURL string) ([]byte, error) {
	tmpl, err := template.New("gate").Parse(gatePageTemplateSource)
	if err != nil {
		return nil, fmt.Errorf("parse gate page template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ RedirectURL string }{RedirectURL: redirectURL}); err != nil {
		return nil, fmt.Errorf("render gate page template: %w", err)
	}
	return buf.Bytes(), nil
}

// Routes returns the Relay's HTTP route table.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.handleGate)
	mux.HandleFunc("POST /api/v1/session", h.handlePostSession)
	mux.HandleFunc("GET /dashboard", h.requireSession(h.handleDashboardPage))
	mux.HandleFunc("GET /api/v1/dashboard", h.requireSessionOrBearer(h.handleGetDashboard))
	mux.HandleFunc("POST /api/v1/snapshot", h.requireAuth(h.handlePostSnapshot))
	return withNoStore(mux)
}

// withNoStore marks every Relay response uncacheable: the gate page, the
// dashboard page and both APIs all serve access-sensitive content that must
// never be replayed from a shared or browser cache.
func withNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// requireAuth guards Connector-only routes: a valid shared Bearer token,
// nothing else. A browser session cookie must never grant write access.
func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthorized(r) {
			h.rejectUnauthorized(w, r)
			return
		}
		next(w, r)
	}
}

// requireSessionOrBearer guards read APIs the dashboard's own JavaScript
// calls: either a valid session cookie (browser) or the shared Bearer token
// (Connector/scripts) is accepted.
func (h *Handler) requireSessionOrBearer(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.hasValidSession(r) || h.isAuthorized(r) {
			next(w, r)
			return
		}
		h.rejectUnauthorized(w, r)
	}
}

// requireSession guards the human-facing dashboard page: only a valid
// session cookie is accepted. Anything else is sent to the configured
// external redirect rather than shown a 401, matching the gate page's own
// failure path.
func (h *Handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.hasValidSession(r) {
			http.Redirect(w, r, h.redirectURL, http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (h *Handler) rejectUnauthorized(w http.ResponseWriter, r *http.Request) {
	h.logger.Warn("rejected unauthorized request",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr),
	)
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
}

func (h *Handler) hasValidSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySessionValue(h.sessionKey, cookie.Value, h.now())
}

func (h *Handler) isAuthorized(r *http.Request) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	got := strings.TrimPrefix(auth, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

// handleGate serves the generic, unauthenticated entry page. It never
// requires auth and never reveals what product it is guarding; its inline
// script collects a token, exchanges it for a session via
// POST /api/v1/session, and sends any cancellation or failure to the
// configured external redirect. A request that already carries a valid
// session cookie is sent straight to /dashboard instead of being prompted
// again.
func (h *Handler) handleGate(w http.ResponseWriter, r *http.Request) {
	if h.hasValidSession(r) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", gateContentSecurityPolicy)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(h.gatePage); err != nil {
		h.logger.Error("write gate page response failed", slog.String("error", err.Error()))
	}
}

// handlePostSession exchanges a valid shared Bearer token for a time-limited
// session cookie. The cookie value is an HMAC-signed expiry, never the
// shared token itself, so a leaked cookie cannot be replayed as the
// Connector credential.
func (h *Handler) handlePostSession(w http.ResponseWriter, r *http.Request) {
	if !h.isAuthorized(r) {
		h.rejectUnauthorized(w, r)
		return
	}
	expiresAt := h.now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSessionValue(h.sessionKey, expiresAt),
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleDashboardPage serves the real dashboard page. Reaching this handler
// already implies requireSession accepted a valid cookie.
func (h *Handler) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(dashboardHTML); err != nil {
		h.logger.Error("write dashboard page response failed", slog.String("error", err.Error()))
	}
}

func (h *Handler) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	deviceID, snap, receivedAt, has := h.store.Get()
	view := BuildDashboard(deviceID, snap, receivedAt, has, h.now())
	h.writeJSON(w, http.StatusOK, view)
}

func (h *Handler) handlePostSnapshot(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBodyBytes)
	defer r.Body.Close()

	var payload SnapshotPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.logger.Warn("rejected malformed snapshot payload", slog.String("error", err.Error()))
		http.Error(w, `{"error":"invalid snapshot payload"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(payload.DeviceID) == "" {
		http.Error(w, `{"error":"device_id is required"}`, http.StatusBadRequest)
		return
	}
	if payload.Snapshot.TakenAt.IsZero() {
		http.Error(w, `{"error":"snapshot.taken_at is required"}`, http.StatusBadRequest)
		return
	}

	h.store.Set(payload.DeviceID, payload.Snapshot, h.now())
	h.logger.Info("snapshot received",
		slog.String("device_id", payload.DeviceID),
		slog.Int("tasks", len(payload.Snapshot.Tasks)),
		slog.Int("runs", len(payload.Snapshot.Runs)),
	)

	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("write json response failed", slog.String("error", err.Error()))
	}
}

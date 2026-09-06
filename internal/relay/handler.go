package relay

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

//go:embed web/gate.html
var gatePageTemplateSource string

//go:embed web/dashboard.html
var dashboardHTML []byte

//go:embed web/workbench.html
var workbenchHTML []byte

// maxSnapshotBodyBytes bounds the size of an accepted POST /api/v1/snapshot
// body, protecting the Relay from unbounded memory use on a malformed or
// hostile request.
const maxSnapshotBodyBytes = 2 << 20 // 2 MiB

// gateContentSecurityPolicy locks the unauthenticated gate page down to the
// bare minimum it needs: an inline verification script and a same-origin
// fetch to POST /api/v1/session. Nothing else is allowed to load or render.
// frame-ancestors 仅限定为 Lark 国际版（larksuite.com）和飞书中国版
// （feishu.cn）的 AppLink 来源，因为 gate 页面是嵌在 Lark/飞书
// 应用内浏览器 frame 中打开的；不允许其他任何来源对其进行 frame 嵌套。
const gateContentSecurityPolicy = "default-src 'none'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors https://*.larksuite.com https://*.feishu.cn"

// Handler serves the Relay's HTTP API and embedded pages.
type Handler struct {
	store          *SnapshotStore
	token          string
	dashboardToken string
	handoffToken   string
	redirectURL    string
	sessionKey     []byte
	gatePage       []byte
	logger         *slog.Logger
	now            func() time.Time
	handoffStore   *HandoffStore
}

// NewHandler constructs a Handler backed by store, authenticating
// Connector/snapshot requests against token, additionally accepting
// dashboardToken (which may be empty to disable it) for browser session
// exchange only, and sending unauthenticated or failed-verification browser
// requests to redirectURL. It returns an error if the embedded gate page
// template fails to render.
func NewHandler(store *SnapshotStore, token, dashboardToken, handoffToken, redirectURL string, logger *slog.Logger, dataDir string) (*Handler, error) {
	gatePage, err := renderGatePage(redirectURL)
	if err != nil {
		return nil, fmt.Errorf("relay: construct handler: %w", err)
	}
	var handoffStore *HandoffStore
	if handoffToken != "" && dataDir != "" {
		handoffStore, err = NewHandoffStore(dataDir, token)
		if err != nil {
			return nil, fmt.Errorf("construct handoff store: %w", err)
		}
	}
	return &Handler{
		store:          store,
		token:          token,
		dashboardToken: dashboardToken,
		handoffToken:   handoffToken,
		redirectURL:    redirectURL,
		sessionKey:     deriveSessionKey(token),
		gatePage:       gatePage,
		logger:         logger,
		now:            time.Now,
		handoffStore:   handoffStore,
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
	mux.HandleFunc("GET /workbench", h.requireSession(h.handleWorkbenchPage))
	mux.HandleFunc("GET /api/v1/dashboard", h.requireSessionOrBearer(h.handleGetDashboard))
	mux.HandleFunc("POST /api/v1/sessions/{session_id}/lark-handoff", h.requireHandoffSession(h.handlePostLarkHandoff))
	mux.HandleFunc("POST /api/v1/handoff/claim", h.requireAuth(h.handlePostHandoffClaim))
	mux.HandleFunc("POST /api/v1/handoff/result", h.requireAuth(h.handlePostHandoffResult))
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

func (h *Handler) requireHandoffSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.hasValidSessionScope(r, sessionScopeHandoff) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
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
	return h.hasValidSessionScope(r, sessionScopeRead)
}

func (h *Handler) hasValidSessionScope(r *http.Request, want string) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	scope, ok := verifySessionValueScope(h.sessionKey, cookie.Value, h.now())
	if !ok {
		return false
	}
	if want == sessionScopeRead {
		return scope == sessionScopeRead || scope == sessionScopeHandoff
	}
	return scope == want
}

// isAuthorized 只认共享 Relay token，用于 POST /api/v1/snapshot 等写操作
// 和 dashboard 只读 API 的 Bearer 分支；Dashboard token 绝不能通过
// 这条路径获得授权，否则一旦泄漏就等同于拿到了写权限。
func (h *Handler) isAuthorized(r *http.Request) bool {
	return matchesBearer(r, h.token)
}

// isSessionExchangeAuthorized 仅供 POST /api/v1/session 使用：除共享
// Relay token 外，还接受专用的 Dashboard token 换取会话 Cookie。
// Dashboard token 的唯一用途就是这里；它不会被 isAuthorized 接受，
// 因此永远不能用来直接调用 POST /api/v1/snapshot 或其他 Bearer 保护
// 的 API。
func (h *Handler) isSessionExchangeAuthorized(r *http.Request) bool {
	return h.sessionExchangeScope(r) != ""
}

func (h *Handler) sessionExchangeScope(r *http.Request) string {
	if h.isAuthorized(r) {
		return sessionScopeRead
	}
	// 空字符串表示未配置 Dashboard token；不能让它匹配到同样为空的
	// Authorization header，否则未配置时反而放行了空 Bearer。
	if h.dashboardToken == "" {
		if h.handoffToken != "" && matchesBearer(r, h.handoffToken) {
			return sessionScopeHandoff
		}
		return ""
	}
	if matchesBearer(r, h.dashboardToken) {
		return sessionScopeRead
	}
	if h.handoffToken != "" && matchesBearer(r, h.handoffToken) {
		return sessionScopeHandoff
	}
	return ""
}

func matchesBearer(r *http.Request, want string) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	got := strings.TrimPrefix(auth, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// handleGate serves the generic, unauthenticated entry page. It never
// requires auth and never reveals what product it is guarding; its inline
// script collects a token, exchanges it for a session via
// POST /api/v1/session, and sends any cancellation or failure to the
// configured external redirect. A request that already carries a valid
// session cookie is sent straight to /workbench instead of being prompted
// again.
func (h *Handler) handleGate(w http.ResponseWriter, r *http.Request) {
	if h.hasValidSession(r) {
		http.Redirect(w, r, "/workbench", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", gateContentSecurityPolicy)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(h.gatePage); err != nil {
		h.logger.Error("write gate page response failed", slog.String("error", err.Error()))
	}
}

// handlePostSession exchanges a valid shared Relay token or dashboard token
// for a time-limited session cookie. The cookie value is an HMAC-signed
// expiry, never the presented token itself, so a leaked cookie cannot be
// replayed as either credential.
func (h *Handler) handlePostSession(w http.ResponseWriter, r *http.Request) {
	scope := h.sessionExchangeScope(r)
	if scope == "" {
		h.rejectUnauthorized(w, r)
		return
	}
	expiresAt := h.now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSessionValueScope(h.sessionKey, expiresAt, scope),
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handlePostLarkHandoff(w http.ResponseWriter, r *http.Request) {
	if h.handoffStore == nil {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	if !sameOriginHost(r) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	if r.Header.Get("X-Hermes-Action") != "lark-handoff" {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	sessionID := r.PathValue("session_id")
	if !validSessionID(sessionID) {
		http.Error(w, `{"error":"invalid session id"}`, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	defer r.Body.Close()
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if !validUUID(body.IdempotencyKey) {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	_, snap, _, has := h.store.Get()
	var profile string
	var retryFailed bool
	found := false
	for _, s := range snap.Sessions {
		if s.ID == sessionID {
			found = true
			profile = s.ProfileName
			retryFailed = s.HandoffPlatform == handoffPlatformFeishu && s.HandoffState == handoffCommandStateFailed
			break
		}
	}
	if !has || !found {
		http.Error(w, `{"error":"unknown session"}`, http.StatusNotFound)
		return
	}
	result, err := h.handoffStore.Create(sessionID, profile, body.IdempotencyKey, retryFailed)
	if err != nil {
		h.logger.Error("handoff enqueue failed", slog.String("session_id", sessionID), slog.String("error_kind", "store"))
		http.Error(w, `{"error":"handoff failed"}`, http.StatusInternalServerError)
		return
	}
	h.logger.Info("handoff command enqueued", slog.String("session_id", sessionID), slog.String("command_id", result.Command.ID), slog.Bool("reused", result.Reused))
	h.writeJSON(w, http.StatusOK, sanitizeHandoffCommand(result.Command))
}

func (h *Handler) handlePostHandoffClaim(w http.ResponseWriter, r *http.Request) {
	if h.handoffStore == nil {
		h.writeJSON(w, http.StatusOK, map[string]any{"command": nil})
		return
	}
	cmd, ok, err := h.handoffStore.Claim()
	if err != nil {
		h.logger.Error("handoff claim failed", slog.String("error_kind", "store"))
		http.Error(w, `{"error":"claim failed"}`, http.StatusInternalServerError)
		return
	}
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"command": nil})
		return
	}
	h.logger.Info("handoff command claimed", slog.String("session_id", cmd.SessionID), slog.String("command_id", cmd.ID))
	h.writeJSON(w, http.StatusOK, map[string]any{"command": sanitizeHandoffCommand(cmd)})
}

func (h *Handler) handlePostHandoffResult(w http.ResponseWriter, r *http.Request) {
	if h.handoffStore == nil {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	defer r.Body.Close()
	var body struct {
		CommandID string `json:"command_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.CommandID == "" {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	state := handoffCommandStateFailed
	if body.Status == handoffCommandStateCompleted {
		state = handoffCommandStateCompleted
	}
	cmd, err := h.handoffStore.Complete(body.CommandID, state)
	if err != nil {
		http.Error(w, `{"error":"unknown command"}`, http.StatusNotFound)
		return
	}
	h.logger.Info("handoff command result", slog.String("session_id", cmd.SessionID), slog.String("command_id", cmd.ID), slog.String("status", state))
	h.writeJSON(w, http.StatusOK, sanitizeHandoffCommand(cmd))
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func validSessionID(s string) bool { return sessionIDPattern.MatchString(s) }
func validUUID(s string) bool      { return uuidPattern.MatchString(s) }

func sameOriginHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "https" || u.Scheme == "http")
}

func sanitizeHandoffCommand(cmd HandoffCommand) map[string]string {
	return map[string]string{
		"command_id":       cmd.ID,
		"session_id":       cmd.SessionID,
		"handoff_state":    cmd.State,
		"handoff_platform": cmd.Platform,
		"result_status":    cmd.ResultStatus,
	}
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

// handleWorkbenchPage serves the live-data workbench at GET /workbench. Its
// script fetches GET /api/v1/dashboard using the same session cookie that
// gated this page, so it shows the real Connector snapshot rather than mock
// data. Reaching this handler already implies requireSession accepted a
// valid cookie, matching the real dashboard's access boundary.
func (h *Handler) handleWorkbenchPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(workbenchHTML); err != nil {
		h.logger.Error("write workbench page response failed", slog.String("error", err.Error()))
	}
}

func (h *Handler) handleGetDashboard(w http.ResponseWriter, r *http.Request) {
	deviceID, snap, receivedAt, has := h.store.Get()
	view := BuildDashboardWithHandoff(deviceID, snap, receivedAt, has, h.now(), h.handoffToken != "")
	if h.handoffStore != nil {
		for i := range view.Sessions {
			// state.db 一旦返回状态即为权威来源；在此之前保留 Relay 队列的
			// 进行中状态，避免按钮短暂恢复可点击并诱发重复操作。
			if view.Sessions[i].HandoffState != "" {
				continue
			}
			cmd, ok, err := h.handoffStore.LatestForSession(view.Sessions[i].ID)
			if err != nil {
				h.logger.Error("handoff dashboard lookup failed", slog.String("error_kind", "store"))
				break
			}
			if !ok {
				continue
			}
			view.Sessions[i].HandoffPlatform = handoffPlatformFeishu
			if cmd.State == handoffCommandStateFailed {
				view.Sessions[i].HandoffState = handoffCommandStateFailed
			} else {
				view.Sessions[i].HandoffState = "pending"
			}
		}
	}
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

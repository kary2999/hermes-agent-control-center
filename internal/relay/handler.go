package relay

import (
	_ "embed"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed web/dashboard.html
var dashboardHTML []byte

// maxSnapshotBodyBytes bounds the size of an accepted POST /api/v1/snapshot
// body, protecting the Relay from unbounded memory use on a malformed or
// hostile request.
const maxSnapshotBodyBytes = 2 << 20 // 2 MiB

// Handler serves the Relay's HTTP API and embedded dashboard page.
type Handler struct {
	store  *SnapshotStore
	token  string
	logger *slog.Logger
	now    func() time.Time
}

// NewHandler constructs a Handler backed by store, authenticating requests
// against token.
func NewHandler(store *SnapshotStore, token string, logger *slog.Logger) *Handler {
	return &Handler{store: store, token: token, logger: logger, now: time.Now}
}

// Routes returns the Relay's HTTP route table.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.handleIndex)
	mux.HandleFunc("GET /api/v1/dashboard", h.requireAuth(h.handleGetDashboard))
	mux.HandleFunc("POST /api/v1/snapshot", h.requireAuth(h.handlePostSnapshot))
	return mux
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthorized(r) {
			h.logger.Warn("rejected unauthorized request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)
			w.Header().Set("WWW-Authenticate", `Bearer realm="hermes-relay"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
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

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(dashboardHTML)
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

package openmeter

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Server is the console's API for one tenant, read from OpenMeter.
type Server struct {
	Client *Client
	Tenant string
	Now    func() time.Time
	// Poll is how often the live stream reads OpenMeter; 0 is 5 seconds.
	Poll time.Duration
	// Identity says who is calling; every API request needs one.
	Identity Identity

	hubOnce sync.Once
	hub     *hub

	pricerMu     sync.Mutex
	cachedPricer *pricer
}

type handlerFunc func(r *http.Request) (any, error)

func (s *Server) Handler() http.Handler {
	if s.Identity == nil {
		panic("openmeter: Server.Identity is required")
	}
	mux := http.NewServeMux()
	p := "/api/v1/" + s.Tenant
	h := func(pattern string, fn handlerFunc) {
		mux.HandleFunc("GET "+p+pattern, s.authenticated(func(w http.ResponseWriter, r *http.Request) {
			v, err := fn(r)
			if err != nil {
				writeOpenMeterError(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, v)
		}))
	}
	mux.HandleFunc("GET "+p+"/receipts", s.authenticated(s.receipts))
	mux.HandleFunc("GET "+p+"/stream/traffic", s.authenticated(s.streamTraffic))
	h("/receipts/count", s.receiptCount)
	h("/spend", s.spend)
	h("/session", s.session)
	h("/summary", s.summary)
	h("/teams", s.teams)
	h("/projects", s.projects)
	h("/models", s.models)
	h("/keys", s.keys)
	h("/series/traffic", s.trafficSeries)
	h("/series/spend", s.spendSeries)
	return mux
}

// receipts is Traffic's list. When the read stopped before finding limit
// matches, X-Receipts-Continue-Before is the before (epoch ms) to ask with
// for the older ones.
func (s *Server) receipts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 240
	}
	rs, continueBefore, err := s.listReceipts(r.Context(), limit, filterOf(r.URL.Query()))
	if err != nil {
		writeOpenMeterError(w, r, err)
		return
	}
	if continueBefore > 0 {
		w.Header().Set("X-Receipts-Continue-Before", strconv.FormatInt(continueBefore, 10))
	}
	writeJSON(w, http.StatusOK, rs)
}

func writeOpenMeterError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]string{"code": "openmeter_unavailable", "message": err.Error()}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

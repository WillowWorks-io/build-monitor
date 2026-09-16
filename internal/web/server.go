// Package web serves the radiator page and its status feed.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/willowworks-io/build-monitor/internal/monitor"
)

//go:embed static
var static embed.FS

// Server serves the radiator over HTTP.
type Server struct {
	mon *monitor.Monitor
	log *slog.Logger
	mux *http.ServeMux
}

// New builds a Server with its routes registered.
func New(mon *monitor.Monitor, log *slog.Logger) *Server {
	s := &Server{mon: mon, log: log, mux: http.NewServeMux()}

	sub, err := fs.Sub(static, "static")
	if err != nil {
		// Only reachable if the embed directive and the tree disagree,
		// which the compiler would already have caught.
		panic(err)
	}

	s.mux.Handle("GET /", http.FileServer(http.FS(sub)))
	s.mux.HandleFunc("GET /api/status", s.status)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	board := s.mon.Board()

	w.Header().Set("Content-Type", "application/json")
	// The board changes on the poll interval, not per request, and a
	// cached copy would show a stale build as current.
	w.Header().Set("Cache-Control", "no-store")

	if err := json.NewEncoder(w).Encode(board); err != nil {
		s.log.Error("encode status", "err", err)
	}
}

// ListenAndServe runs the HTTP server on addr.
func (s *Server) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

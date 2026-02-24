package proxy

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
)

// Server is an HTTP CONNECT proxy that logs runner-to-destination traffic.
type Server struct {
	store  *state.Store
	logger *slog.Logger
}

// NewServer creates a new proxy server.
func NewServer(store *state.Store, logger *slog.Logger) *Server {
	return &Server{
		store:  store,
		logger: logger,
	}
}

// Handler returns the HTTP handler for the proxy.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.handleRequest)
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
	} else {
		s.handleHTTP(w, r)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	sourceIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	// Look up runner info by source IP
	runnerInfo, found := s.store.GetByContainerIP(sourceIP)
	if found {
		s.logger.Info("CONNECT tunnel",
			"runner_name", runnerInfo.RunnerName,
			"profile", runnerInfo.Profile,
			"job_name", runnerInfo.JobName,
			"target", r.Host,
			"source_ip", sourceIP,
		)
	} else {
		s.logger.Info("CONNECT tunnel (unknown runner)",
			"target", r.Host,
			"source_ip", sourceIP,
		)
	}

	// Connect to the target
	targetConn, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		s.logger.Error("failed to connect to target", "target", r.Host, "error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// Send 200 Connection Established
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.logger.Error("hijacking not supported")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		targetConn.Close()
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		s.logger.Error("failed to hijack connection", "error", err)
		targetConn.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Bidirectional copy
	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(targetConn, clientConn)
	}()
	go func() {
		defer targetConn.Close()
		defer clientConn.Close()
		_, _ = io.Copy(clientConn, targetConn)
	}()
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	sourceIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	runnerInfo, found := s.store.GetByContainerIP(sourceIP)
	if found {
		s.logger.Info("HTTP request",
			"runner_name", runnerInfo.RunnerName,
			"profile", runnerInfo.Profile,
			"method", r.Method,
			"url", r.URL.String(),
			"source_ip", sourceIP,
		)
	}

	// Forward the request
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		s.logger.Error("proxy request failed", "error", err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

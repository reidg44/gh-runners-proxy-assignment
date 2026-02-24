package proxy

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reidg44/gh-runners-proxy-assignment/internal/state"
)

func TestHandleHTTPForward(t *testing.T) {
	// Create a target server
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "passed")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello from target"))
	}))
	defer target.Close()

	store := state.NewStore()
	logger := slog.Default()
	srv := NewServer(store, logger)

	// Create a request to the target
	req := httptest.NewRequest(http.MethodGet, target.URL, nil)
	req.RequestURI = target.URL

	w := httptest.NewRecorder()
	srv.handleHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", resp.StatusCode)
	}
	if resp.Header.Get("X-Test") != "passed" {
		t.Error("expected X-Test header to be forwarded")
	}
}

func TestHandleConnectEstablishes(t *testing.T) {
	// Create a target TCP server
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("hello from target\n"))
	}()

	store := state.NewStore()
	logger := slog.Default()
	srv := NewServer(store, logger)

	// Start proxy server
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()

	proxyServer := &http.Server{Handler: srv.Handler()}
	go func() { _ = proxyServer.Serve(proxyListener) }()
	defer proxyServer.Close()

	// Connect through proxy
	proxyConn, err := net.Dial("tcp", proxyListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer proxyConn.Close()

	// Send CONNECT request manually
	targetAddr := target.Addr().String()
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", targetAddr, targetAddr)
	if _, err := proxyConn.Write([]byte(connectReq)); err != nil {
		t.Fatal(err)
	}

	// Read response
	br := bufio.NewReader(proxyConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("CONNECT response status=%d, want 200", resp.StatusCode)
	}
}

func TestRunnerIdentification(t *testing.T) {
	store := state.NewStore()
	store.AddRunner(&state.RunnerInfo{
		RunnerName:  "runner-1",
		ContainerIP: "172.18.0.2",
		Profile:     "high-cpu",
		JobName:     "high-cpu",
	})

	logger := slog.Default()
	srv := NewServer(store, logger)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	req := httptest.NewRequest(http.MethodGet, target.URL, nil)
	req.RequestURI = target.URL
	req.RemoteAddr = "172.18.0.2:54321"

	w := httptest.NewRecorder()
	srv.handleHTTP(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("status=%d, want 200", w.Result().StatusCode)
	}
}

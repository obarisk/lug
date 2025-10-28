package lug

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNewLug(t *testing.T) {
	tests := []struct {
		name            string
		connIdleTimeout time.Duration
		shutdownTimeout time.Duration
		readTimeout     time.Duration
		writeTimeout    time.Duration
		maxConn         int
	}{
		{
			name:            "default settings",
			connIdleTimeout: 30 * time.Second,
			shutdownTimeout: 5 * time.Second,
			readTimeout:     0,
			writeTimeout:    0,
			maxConn:         16,
		},
		{
			name:            "custom settings",
			connIdleTimeout: 10 * time.Second,
			shutdownTimeout: 2 * time.Second,
			readTimeout:     3 * time.Second,
			writeTimeout:    4 * time.Second,
			maxConn:         8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, close := NewLug(tt.connIdleTimeout, tt.shutdownTimeout, tt.readTimeout, tt.writeTimeout, tt.maxConn)
			defer close()

			if l == nil {
				t.Fatal("NewLug returned nil")
			}
			if l.maxc != tt.maxConn {
				t.Errorf("maxc = %d, want %d", l.maxc, tt.maxConn)
			}
			if l.connIdleTimeout != tt.connIdleTimeout {
				t.Errorf("connIdleTimeout = %v, want %v", l.connIdleTimeout, tt.connIdleTimeout)
			}
			if l.readTimeout != tt.readTimeout {
				t.Errorf("readTimeout = %v, want %v", l.readTimeout, tt.readTimeout)
			}
			if l.writeTimeout != tt.writeTimeout {
				t.Errorf("writeTimeout = %v, want %v", l.writeTimeout, tt.writeTimeout)
			}
			if l.shutdownTimeout != tt.shutdownTimeout {
				t.Errorf("shutdownTimeout = %v, want %v", l.shutdownTimeout, tt.shutdownTimeout)
			}
			if l.pool == nil {
				t.Error("pool map is nil")
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	}))
	defer server.Close()

	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
	defer close()

	req, err := http.NewRequest("GET", server.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := l.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	if string(body) != "Hello, World!" {
		t.Errorf("Body = %q, want %q", string(body), "Hello, World!")
	}

	if req.Header.Get("Connection") != "keep-alive" {
		t.Errorf("Connection header = %q, want %q", req.Header.Get("Connection"), "keep-alive")
	}
}

func TestRoundTripErros(t *testing.T) {
	t.Run("unable to get connection", func(t *testing.T) {
		l, close := NewLug(3*time.Second, 3*time.Second, 50*time.Microsecond, 50*time.Microsecond, 0)
		defer close()
		req, err := http.NewRequest("GET", "http://invalid-host", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		_, err = l.RoundTrip(req)
		if !strings.Contains(err.Error(), "no such host") {
			t.Errorf("Expected DNS error, got: %v", err)
		}
	})

	t.Run("write a closed connection", func(t *testing.T) {
		l, close := NewLug(3*time.Second, 3*time.Second, 50*time.Millisecond, 5*time.Nanosecond, 1)
		defer close()

		lp, err := net.Listen("tcp4", ":0")
		if err != nil {
			t.Fatalf("Failed to create dummy connection: %v", err)
		}
		go func() {
			con, err := lp.Accept()
			if err != nil {
				t.Fatalf("Failed to accept dummy connection: %v", err)
			}
			buf := make([]byte, 4096)
			con.Read(buf)
			con.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))
		}()

		req, err := http.NewRequest("GET", "http://"+lp.Addr().String(), nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		_, err = l.RoundTrip(req)
		if !strings.Contains(err.Error(), "i/o timeout") {
			t.Errorf("Expected write error on closed connection, got: %v", err)
		}
		lp.Close()
	})
}

func TestRoundTripMultipleRequests(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
	defer close()

	// Make multiple requests to test connection reuse
	for i := 0; i < 5; i++ {
		req, err := http.NewRequest("GET", server.URL, nil)
		if err != nil {
			t.Fatalf("Request %d: Failed to create request: %v", i, err)
		}

		resp, err := l.RoundTrip(req)
		if err != nil {
			t.Fatalf("Request %d: RoundTrip failed: %v", i, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Request %d: StatusCode = %d, want %d", i, resp.StatusCode, http.StatusOK)
		}
	}

	if requestCount != 5 {
		t.Errorf("requestCount = %d, want 5", requestCount)
	}
}

func TestRoundTripConcurrent(t *testing.T) {
	cnt := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
		cnt.Add(1)
	}))
	defer server.Close()

	l, close := NewLug(30*time.Second, 5*time.Second, 500*time.Millisecond, 500*time.Millisecond, DefaultMaxConn)
	defer close()

	wg := sync.WaitGroup{}
	nreq := 256
	for i := 0; i < nreq; i++ {
		wg.Go(func() {
			req, err := http.NewRequest("GET", server.URL, nil)
			if err != nil {
				t.Fatalf("Concurrent request: Failed to create request: %v", err)
			}
			resp, err := l.RoundTrip(req)
			if err != nil {
				t.Fatalf("Concurrent request: RoundTrip failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("Concurrent request: StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
			}
		})
	}
	wg.Wait()
	if int(cnt.Load()) != nreq {
		t.Errorf("cnt = %d, want %d", cnt.Load(), nreq)
	}
}

func TestNewPool(t *testing.T) {
	t.Run("simple", func(t *testing.T) {
		l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
		defer close()

		host := "example.com:80"
		l.newPool(host)

		l.mu.RLock()
		pool, ok := l.pool[host]
		l.mu.RUnlock()

		if !ok {
			t.Fatal("Pool not created")
		}
		if pool.host != "example.com" {
			t.Errorf("host = %q, want %q", pool.host, "example.com")
		}
		if pool.port != ":80" {
			t.Errorf("port = %q, want %q", pool.port, ":80")
		}
	})

	t.Run("default port", func(t *testing.T) {
		l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
		defer close()

		host := "example.com"
		l.newPool(host)

		l.mu.RLock()
		pool, ok := l.pool[host]
		l.mu.RUnlock()

		if !ok {
			t.Fatal("Pool not created")
		}
		if pool.host != "example.com" {
			t.Errorf("host = %q, want %q", pool.host, "example.com")
		}
		if pool.port != ":80" {
			t.Errorf("port = %q, want %q", pool.port, ":80")
		}
	})

	t.Run("no duplicate", func(t *testing.T) {
		l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
		defer close()

		host := "example.com:8080"

		l.newPool(host)
		l.newPool(host)

		poolCount := len(l.pool)

		if poolCount != 1 {
			t.Errorf("pool count = %d, want 1", poolCount)
		}
	})

	t.Run("concurrent safe", func(t *testing.T) {
		l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
		defer close()

		concurrent := 1000
		wg := sync.WaitGroup{}
		for i := 0; i < concurrent; i++ {
			wg.Go(func() {
				l.newPool(fmt.Sprintf("localhost:%d", 8000+(i%100)))
			})
		}
		wg.Wait()
		poolCount := len(l.pool)
		if poolCount != concurrent/10 {
			t.Errorf("pool count = %d, want %d", poolCount, concurrent/10)
		}
	})
}

func TestHPoolMaintainPool(t *testing.T) {
	ctx, cxl := context.WithCancel(context.Background())
	dns, err := fakeDNSServer(ctx)
	if err != nil {
		t.Fatalf("failed to start fake DNS server: %v", err)
	}
	d := &net.Dialer{
		Timeout:       100 * time.Millisecond,
		FallbackDelay: time.Duration(-1),
	}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return d.DialContext(ctx, "udp4", dns.String())
		},
	}
	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	l.resolver = resolver
	defer close()

	pool := &hPool{
		l:    l,
		mu:   sync.RWMutex{},
		pool: []*iPool{},
		host: "google.com.",
		port: ":8080",
		stop: make(chan struct{}),
	}
	go pool.probe()

	wg := sync.WaitGroup{}
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			if err := pool.maintainPool(); err != nil {
				t.Fatalf("maintainPool error: %v", err)
			}
		})
	}
	wg.Wait()
	pool.stop <- struct{}{}
	for i := 0; i < 10; i++ {
		t0 := time.Now()
		if err := pool.maintainPool(); err != nil {
			t.Fatalf("maintainPool error: %v", err)
		}
		t.Logf("maintainPool call took %v", time.Since(t0))
	}
	cxl()
	pool.close()

	t.Run("probe", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			pool := &hPool{
				l:    l,
				mu:   sync.RWMutex{},
				pool: []*iPool{},
				host: "google.com.",
				port: ":8080",
				stop: make(chan struct{}),
			}
			go pool.probe()
			time.Sleep(2 * time.Second)
			pool.close()
			time.Sleep(100 * time.Millisecond)
		})
	})
}

func TestIPoolWaitTimeout(t *testing.T) {
	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 10)
	defer close()

	pool := &iPool{
		l:    l,
		pool: make(chan cConn, l.maxc),
		addr: "127.0.0.1",
	}

	// Test with low load
	pool.n.Store(5)
	timeout := pool.waitTimout()
	if timeout != PollTimeout {
		t.Errorf("waitTimout with low load = %v, want %v", timeout, PollTimeout)
	}

	// Test with high load
	pool.n.Store(15)
	timeout = pool.waitTimout()
	expected := PollTimeout + 15*time.Millisecond
	if timeout != expected {
		t.Errorf("waitTimout with high load = %v, want %v", timeout, expected)
	}
}

func TestConnectionIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	// Create lug with very short idle timeout
	l, close := NewLug(100*time.Millisecond, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
	defer close()

	// Make first request
	req1, _ := http.NewRequest("GET", server.URL, nil)
	resp1, err := l.RoundTrip(req1)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}
	resp1.Body.Close()

	// Wait for idle timeout
	time.Sleep(200 * time.Millisecond)

	// Make second request - should create new connection
	req2, _ := http.NewRequest("GET", server.URL, nil)
	resp2, err := l.RoundTrip(req2)
	if err != nil {
		t.Fatalf("Second request failed: %v", err)
	}
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp2.StatusCode, http.StatusOK)
	}
}

func TestClose(t *testing.T) {
	l, clf := NewLug(30*time.Second, 1*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)

	// Create a pool
	l.newPool("example.com:80")

	// Close should not panic
	clf()

	// Verify pools are handled
	// Note: This is a basic test, actual cleanup verification would need more setup
}

func TestHPoolMoveLoc(t *testing.T) {
	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer close()

	// Create test pools with different load levels
	pool1 := &iPool{l: l, pool: make(chan cConn, 4), addr: "127.0.0.1"}
	pool2 := &iPool{l: l, pool: make(chan cConn, 4), addr: "127.0.0.2"}
	pool3 := &iPool{l: l, pool: make(chan cConn, 4), addr: "127.0.0.3"}

	pool1.n.Store(5)
	pool2.n.Store(2)
	pool3.n.Store(8)

	hpool := &hPool{
		l:    l,
		mu:   sync.RWMutex{},
		loc:  0,
		pool: []*iPool{pool1, pool2, pool3},
		host: "test.com",
		port: ":80",
	}

	// Move from pool1 (load=5) to pool2 (load=2)
	hpool.moveLoc()

	// Location should move to pool2 since it has less load
	if hpool.loc != 1 {
		t.Errorf("loc = %d, want 1 (should move to pool with lower load)", hpool.loc)
	}
}

func TestIPPoolPoll(t *testing.T) {
	l, close := NewLug(1*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer close()

	pool := &iPool{
		l:    l,
		pool: make(chan cConn, 4),
		addr: "127.0.0.1",
	}

	// Test poll with empty pool
	conn, ok := pool.poll()
	if ok {
		t.Error("poll should return false for empty pool")
	}
	if conn != nil {
		t.Error("poll should return nil connection for empty pool")
	}
}

func TestIPPoolPollWithExpiredConnection(t *testing.T) {
	l, close := NewLug(10*time.Millisecond, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer close()

	pool := &iPool{
		l:    l,
		pool: make(chan cConn, 4),
		addr: "127.0.0.1",
	}

	// Create a mock connection that will be expired
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Add an old connection to pool
	pool.pool <- cConn{c: client, t: time.Now().Add(-1 * time.Second)}

	// Poll should discard expired connection
	conn, ok := pool.poll()
	if ok {
		t.Error("poll should return false for expired connection")
	}
	if conn != nil {
		t.Error("poll should return nil for expired connection")
	}
}

func TestIPPoolPollWithValidConnection(t *testing.T) {
	l, close := NewLug(1*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer close()

	pool := &iPool{
		l:    l,
		pool: make(chan cConn, 4),
		addr: "127.0.0.1",
	}

	// Create a mock connection
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Add a fresh connection to pool
	pool.pool <- cConn{c: client, t: time.Now()}

	// Poll should return the valid connection
	conn, ok := pool.poll()
	if !ok {
		t.Error("poll should return true for valid connection")
	}
	if conn == nil {
		t.Error("poll should return connection for valid connection")
	}
}

func TestIPoolGetConnectionFailure(t *testing.T) {
	l, clf := NewLug(1*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer clf()
	ipool := &iPool{
		l:    l,
		n:    atomic.Int32{},
		pool: make(chan cConn, 4),
		addr: "0.0.0.0", // Invalid IP to force Dial failure
	}
	_, err, cl := ipool.getConnection(":80")
	defer cl(err)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("Expected error from getConnection, got %v", err)
	}
}

func TestIPoolConnectionCloseFunction(t *testing.T) {
	s, err := net.Listen("tcp4", ":0")
	if err != nil {
		t.Fatalf("Failed to start test server: %v", err)
	}
	sa := s.Addr().(*net.TCPAddr)
	l, clf := NewLug(1*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, 4)
	defer clf()
	ipool := &iPool{
		l:    l,
		n:    atomic.Int32{},
		pool: make(chan cConn, 4),
		addr: sa.IP.String(),
	}
	_, err, cl := ipool.getConnection(fmt.Sprintf(":%d", sa.Port))
	cl(errors.New("fake error"))
	ipool.close()
}

func TestRoundTripWithHTTPClient(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Response"))
	}))
	defer server.Close()

	l, close := NewLug(30*time.Second, 5*time.Second, 50*time.Millisecond, 50*time.Millisecond, DefaultMaxConn)
	defer close()

	client := &http.Client{
		Transport: l,
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("GET request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Response" {
		t.Errorf("Body = %q, want %q", string(body), "Response")
	}

	if requestCount != 1 {
		t.Errorf("requestCount = %d, want 1", requestCount)
	}
}

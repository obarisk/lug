package lug

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func BenchmarkLugTransport(b *testing.B) {
	mu := http.NewServeMux()
	mu.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})
	sl, err := net.Listen("tcp4", ":0")
	if err != nil {
		b.Fatal(err)
	}
	hs := &http.Server{Handler: mu, Addr: sl.Addr().String()}
	go func() {
		if err := hs.Serve(sl); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	lt, clf := NewLug(
		5*time.Second,
		5*time.Second,
		500*time.Millisecond,
		500*time.Millisecond,
		DefaultMaxIdleConn)
	defer clf()
	hc := http.Client{Transport: lt}
	okCnt := 0

	for i := 0; i < b.N; i++ {
		resp, err := hc.Get("http://" + sl.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		defer resp.Body.Close()
		io.CopyN(io.Discard, resp.Body, 1)
		if resp.StatusCode == http.StatusOK {
			okCnt++
		}
	}

	if okCnt != b.N {
		b.Fatalf("Expected %d successful responses, got %d", b.N, okCnt)
	}
	hs.Close()
}

func BenchmarkDefaultTransport(b *testing.B) {
	mu := http.NewServeMux()
	mu.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})
	sl, err := net.Listen("tcp4", ":0")
	if err != nil {
		b.Fatal(err)
	}
	hs := &http.Server{Handler: mu, Addr: sl.Addr().String()}
	go func() {
		if err := hs.Serve(sl); err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	tr := http.DefaultTransport.(*http.Transport).Clone()
	hc := http.Client{Transport: tr}
	okCnt := 0

	for i := 0; i < b.N; i++ {
		resp, err := hc.Get("http://" + sl.Addr().String())
		if err != nil {
			b.Fatal(err)
		}
		defer resp.Body.Close()
		io.CopyN(io.Discard, resp.Body, 1)
		if resp.StatusCode == http.StatusOK {
			okCnt++
		}
	}

	if okCnt != b.N {
		b.Fatalf("Expected %d successful responses, got %d", b.N, okCnt)
	}
	hs.Close()
}

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/obarisk/lug"
)

func main() {
	t0 := time.Now()
	lt, closer := lug.NewLug(
		5*time.Second,
		5*time.Second,
		500*time.Millisecond,
		500*time.Millisecond,
		lug.DefaultMaxIdleConn)
	hc := http.Client{Transport: lt}
	defer closer()

	wg := sync.WaitGroup{}
	nn := atomic.Int32{}
	for i := 0; i < 1024; i++ {
		wg.Go(func() {
			resp, err := hc.Get("http://okserver:8080")
			if err != nil {
				fmt.Println("HTTP GET error:", err.Error())
			} else {
				defer resp.Body.Close()
				var body bytes.Buffer
				io.Copy(&body, resp.Body)
			}
			nn.Add(1)
		})
	}
	ch := make(chan struct{})
	go func() {
		wg.Wait()
		ch <- struct{}{}
	}()
	x := true
	for x {
		select {
		case <-ch:
			x = false
			fmt.Println()
		default:
			fmt.Printf("Completed requests: %d\r", nn.Load())
		}
	}
	fmt.Println("Elapsed time:", time.Since(t0))
}

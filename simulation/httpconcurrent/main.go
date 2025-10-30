package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	t0 := time.Now()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	hc := http.Client{Transport: tr}

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

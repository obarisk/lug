package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

func main() {
	cntr := atomic.Int64{}
	mu := http.NewServeMux()
	mu.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			defer r.Body.Close()
			var body bytes.Buffer
			_, err := io.Copy(&body, r.Body)
			if err != nil {
				log.Printf("Error reading body: %v\n", err)
			}
		}
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintf(w, "ok")
		cntr.Add(1)
		log.Printf("Calls: %d\r", cntr.Load())
	})

	hs := &http.Server{
		Addr:         ":8080",
		Handler:      mu,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	hs.SetKeepAlivesEnabled(true)
	if err := hs.ListenAndServe(); err != nil {
		log.Printf("Error starting server: %s\n", err)
	}
}

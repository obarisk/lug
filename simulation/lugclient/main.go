package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/obarisk/lug"
)

func main() {
	lt, closer := lug.NewLug(
		5*time.Second,
		5*time.Second,
		500*time.Millisecond,
		500*time.Millisecond,
		20)
	hc := http.Client{Transport: lt}
	defer closer()

	for i := 0; i < 20; i++ {
		go func(i int) {
			time.Sleep(100 * time.Millisecond)
			body := []byte("request from goroutine " + string(i))
			for {
				resp, err := hc.Post("http://okserver:8080", "text/plain", bytes.NewReader(body))
				if err != nil {
					fmt.Println("error:", err)
					continue
				}
				var buf bytes.Buffer
				io.Copy(&buf, resp.Body)
				resp.Body.Close()
				time.Sleep(1 * time.Second)
			}
		}(i)
	}

	ctx, cxl := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cxl()
	<-ctx.Done()
}

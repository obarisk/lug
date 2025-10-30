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
)

func main() {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	hc := http.Client{Transport: tr}

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

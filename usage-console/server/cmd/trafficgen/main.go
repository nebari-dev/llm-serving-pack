// trafficgen sends demo requests to the gateway at a steady, jittered rate.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/jbouder/stargate/server/internal/traffic"
)

func main() {
	target := flag.String("gateway", "http://localhost:8081", "gateway base URL")
	rps := flag.Float64("rps", 0.8, "average requests per second")
	concurrency := flag.Int("concurrency", 16, "max requests in flight")
	duration := flag.Duration("duration", 0, "stop after this long (0 = until interrupted)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	gen := traffic.New()
	r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	sem := make(chan struct{}, *concurrency)
	client := &http.Client{Timeout: 2 * time.Minute}
	var sent, failed atomic.Int64

	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				log.Printf("sent %d · non-2xx %d", sent.Load(), failed.Load())
			}
		}
	}()

	log.Printf("trafficgen → %s at ~%.1f rps", *target, *rps)
	for ctx.Err() == nil {
		// Exponential gaps give Poisson arrivals around the target rate.
		gap := time.Duration(r.ExpFloat64() / *rps * float64(time.Second))
		select {
		case <-ctx.Done():
		case <-time.After(gap):
		}
		if ctx.Err() != nil {
			break
		}
		req := gen.Next(r)
		select {
		case sem <- struct{}{}:
		default:
			continue // saturated: skip rather than queue
		}
		go func() {
			defer func() { <-sem }()
			sent.Add(1)
			if !send(ctx, client, *target, req) {
				failed.Add(1)
			}
		}()
	}
	log.Printf("done: sent %d · non-2xx %d", sent.Load(), failed.Load())
}

func send(ctx context.Context, c *http.Client, target string, req traffic.Request) bool {
	body, _ := json.Marshal(req.Body)
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, target+"/v1/chat/completions", bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+req.Secret)
	for k, v := range map[string]string{"X-Data-Region": req.Region, "X-Session-Id": req.SessionID, "X-Actor": req.Actor} {
		if v != "" {
			hr.Header.Set(k, v)
		}
	}
	resp, err := c.Do(hr)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("request: %v", err)
		}
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain streams to completion too
	return resp.StatusCode < 300
}

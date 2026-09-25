// Command evalload sends flag evaluations to a FeatureSteward server as
// fast as it can and reports throughput and latency. The SDK key comes
// from EVALLOAD_KEY, so it stays out of shell history.
//
//	EVALLOAD_KEY=fs_sdk_… evalload -url http://localhost:8080 -flag new-checkout -c 32 -d 20s
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

func main() {
	base := flag.String("url", "http://localhost:8080", "server URL")
	flagKey := flag.String("flag", "", "flag to evaluate (required)")
	workers := flag.Int("c", 32, "concurrent requests")
	duration := flag.Duration("d", 20*time.Second, "how long to run")
	flag.Parse()
	key := os.Getenv("EVALLOAD_KEY")
	if key == "" || *flagKey == "" {
		fmt.Fprintln(os.Stderr, "usage: EVALLOAD_KEY=<sdk key> evalload -flag <key> [-url URL] [-c N] [-d 20s]")
		os.Exit(2)
	}

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: *workers, MaxConnsPerHost: *workers},
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	var mu sync.Mutex
	var latencies []time.Duration
	statuses := map[int]int{}
	var wg sync.WaitGroup
	start := time.Now()
	for w := range *workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []time.Duration
			localStatus := map[int]int{}
			for i := 0; ctx.Err() == nil; i++ {
				body := fmt.Sprintf(`{"flag":%q,"user_id":"user-%d-%d"}`, *flagKey, w, i)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(*base, "/")+"/api/v1/evaluate", bytes.NewBufferString(body))
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				t := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					if ctx.Err() == nil {
						localStatus[0]++
					}
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				local = append(local, time.Since(t))
				localStatus[resp.StatusCode]++
			}
			mu.Lock()
			latencies = append(latencies, local...)
			for k, v := range localStatus {
				statuses[k] += v
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	slices.Sort(latencies)
	pct := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[min(len(latencies)-1, int(p*float64(len(latencies))))]
	}
	fmt.Printf("requests  %d in %s with %d workers\n", len(latencies), elapsed.Round(time.Millisecond), *workers)
	fmt.Printf("rate      %.0f/s\n", float64(len(latencies))/elapsed.Seconds())
	fmt.Printf("latency   p50 %s  p95 %s  p99 %s  max %s\n", pct(0.50), pct(0.95), pct(0.99), pct(1))
	fmt.Printf("statuses  %v (0 = network error)\n", statuses)
}

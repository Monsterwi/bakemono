// hotload drives a fixed offered rate and includes client queueing in latency.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type sample struct{ queue, firstByte, firstBody, complete float64 }

func percentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	return values[int(float64(len(values)-1)*fraction)]
}
func summary(values []float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	return map[string]float64{"p50": percentile(values, .50), "p95": percentile(values, .95), "p99": percentile(values, .99)}
}
func main() {
	url := flag.String("url", "", "HTTP endpoint")
	host := flag.String("host", "bench.local", "Host header")
	rate := flag.Int("rate", 16000, "offered requests/second")
	seconds := flag.Int("seconds", 20, "offered duration")
	workers := flag.Int("workers", 100, "maximum concurrent requests")
	expected := flag.Int64("bytes", 256<<10, "expected successful body length")
	flag.Parse()
	if *url == "" || *rate < 1 || *seconds < 1 || *workers < 1 || *expected < 0 || int64(*rate)*int64(*seconds) > 10000000 {
		fmt.Fprintln(os.Stderr, "invalid load parameters")
		os.Exit(2)
	}
	transport := &http.Transport{MaxIdleConns: *workers, MaxIdleConnsPerHost: *workers, MaxConnsPerHost: *workers, DisableCompression: true}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	jobs := make(chan time.Time, *workers*64)
	var wg sync.WaitGroup
	var failures, dropped atomic.Int64
	results := make([][]sample, *workers)
	for worker := range *workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			local := make([]sample, 0, *rate**seconds / *workers)
			for planned := range jobs {
				started := time.Now()
				row := sample{queue: float64(started.Sub(planned)) / float64(time.Millisecond)}
				request, err := http.NewRequest(http.MethodGet, *url, nil)
				if err != nil {
					failures.Add(1)
					continue
				}
				request.Host = *host
				request.Header.Set("Accept-Encoding", "identity")
				trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { row.firstByte = float64(time.Since(planned)) / float64(time.Millisecond) }}
				response, err := client.Do(request.WithContext(httptrace.WithClientTrace(request.Context(), trace)))
				if err != nil {
					failures.Add(1)
					continue
				}
				var first [1]byte
				n, readErr := response.Body.Read(first[:])
				if n > 0 {
					row.firstBody = float64(time.Since(planned)) / float64(time.Millisecond)
				}
				count, copyErr := io.Copy(io.Discard, response.Body)
				closeErr := response.Body.Close()
				if response.StatusCode != http.StatusOK || int64(n)+count != *expected || (readErr != nil && readErr != io.EOF) || copyErr != nil || closeErr != nil {
					failures.Add(1)
					continue
				}
				row.complete = float64(time.Since(planned)) / float64(time.Millisecond)
				local = append(local, row)
			}
			results[index] = local
		}(worker)
	}
	started := time.Now().Add(100 * time.Millisecond)
	total := *rate * *seconds
	for index := 0; index < total; {
		planned := started.Add(time.Duration(index) * time.Second / time.Duration(*rate))
		now := time.Now()
		if now.Before(planned) {
			time.Sleep(min(planned.Sub(now), time.Millisecond))
			continue
		}
		for index < total {
			planned = started.Add(time.Duration(index) * time.Second / time.Duration(*rate))
			if planned.After(now) {
				break
			}
			select {
			case jobs <- planned:
			default:
				dropped.Add(1)
			}
			index++
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(started).Seconds()
	queue, firstByte, firstBody, complete := make([]float64, 0, total), make([]float64, 0, total), make([]float64, 0, total), make([]float64, 0, total)
	for _, local := range results {
		for _, row := range local {
			queue = append(queue, row.queue)
			firstByte = append(firstByte, row.firstByte)
			firstBody = append(firstBody, row.firstBody)
			complete = append(complete, row.complete)
		}
	}
	result := map[string]any{"offered": total, "rate": *rate, "seconds": *seconds, "workers": *workers, "successful": len(complete), "errors": failures.Load(), "dropped": dropped.Load(), "elapsed": elapsed, "qps": float64(len(complete)) / elapsed, "queue_ms": summary(queue), "ttfb_ms": summary(firstByte), "first_body_ms": summary(firstBody), "completion_ms": summary(complete)}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic(err)
	}
	if failures.Load() != 0 || dropped.Load() != 0 {
		os.Exit(1)
	}
}

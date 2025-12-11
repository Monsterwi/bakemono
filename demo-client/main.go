package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

func main() {
	// url := "http://10.10.135.73:80/ba117d2eb12e488ad09b27d6c4b49e03.265tscrc"
	// url := "http://10.72.13.32:80/ba117d2eb12e488ad09b27d6c4b49e03.265tscrc?v=1&alg=crc32&blk=16k"
	url := "http://127.0.0.1:80/ba117d2eb12e488ad09b27d6c4b49e03.265tscrc?v=1&alg=crc32&blk=16k"
	concurrency := 5 // 并发请求数
	wg := sync.WaitGroup{}
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()
			start := time.Now()
			// request with range
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				log.Printf("[Goroutine %d] New request error: %v", id, err)
				return
			}
			// req.Header.Set("Range", "bytes=100-200")
			resp, err := http.DefaultClient.Do(req)
			cost := time.Since(start)
			if err != nil {
				log.Printf("[Goroutine %d] Request error: %v", id, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			log.Printf("[Goroutine %d] Status: %d, Len: %d, Cost: %v", id, resp.StatusCode, len(body), cost)
		}(i)
	}

	wg.Wait()
	fmt.Println("All requests finished.")
}

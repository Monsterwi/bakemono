package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestLoadClientCountsBodiesAndFailures(t *testing.T) {
	if os.Getenv("RAZOR_HOTLOAD_TEST_HELPER") == "1" {
		// The child executes the real CLI, including its exit status and JSON output.
		os.Args = append([]string{os.Args[0]}, os.Args[3:]...)
		main()
		os.Exit(0)
	}
	for _, valid := range []bool{true, false} {
		name := "truncated"
		if valid {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "3")
				if r.Host != "bench.local" {
					t.Error("wrong cache identity")
				}
				if valid {
					w.Write([]byte("abc"))
				} else {
					w.Write([]byte("a"))
				}
			}))
			defer endpoint.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoadClientCountsBodiesAndFailures$", "--", "-url", endpoint.URL, "-rate", "4", "-seconds", "1", "-workers", "2", "-bytes", "3")
			cmd.Env = append(os.Environ(), "RAZOR_HOTLOAD_TEST_HELPER=1")
			output, err := cmd.Output()
			var result struct{ Offered, Successful, Errors, Dropped int }
			if decodeErr := json.Unmarshal(output, &result); decodeErr != nil {
				t.Fatalf("invalid result %q: %v", output, decodeErr)
			}
			if result.Offered != 4 || result.Dropped != 0 {
				t.Fatalf("offered load: %+v", result)
			}
			if valid && (err != nil || result.Successful != 4 || result.Errors != 0) {
				t.Fatalf("complete: %+v %v", result, err)
			}
			if !valid && (err == nil || result.Successful != 0 || result.Errors != 4) {
				t.Fatalf("truncation not rejected: %+v %v", result, err)
			}
		})
	}
}

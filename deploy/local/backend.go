// The local demo backend produces real HTTP traffic through HAProxy.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	server := &http.Server{Addr: ":8080", Handler: demoHandler(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func demoHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/healthz/fail", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Bounded knobs make response codes, bandwidth and latency testable.
		delay, _ := strconv.Atoi(r.URL.Query().Get("delay"))
		if delay > 0 {
			time.Sleep(time.Duration(min(delay, 2000)) * time.Millisecond)
		}
		status, _ := strconv.Atoi(r.URL.Query().Get("status"))
		if status < 200 || status > 599 {
			status = 200
		}
		// Opt-in fixture: keep transport and /healthz healthy while one app
		// request path fails, so direct and entrypoint checks can be distinguished.
		if path := os.Getenv("DEMO_FAILURE_PATH"); path != "" && r.URL.Path == path {
			enabled := true
			if flag := os.Getenv("DEMO_FAILURE_FILE"); flag != "" {
				_, err := os.Stat(flag)
				enabled = err == nil
			}
			if enabled {
				status = http.StatusServiceUnavailable
			}
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		name := os.Getenv("DEMO_BACKEND")
		if name == "" {
			name = "application"
		}
		w.Header().Set("X-Demo-Backend", name)
		w.WriteHeader(status)
		fmt.Fprintln(w, "Converge HAProxy demo backend")
		// Keep a real connection active across reloads without extending HAProxy
		// timeouts. Streaming chunks let the maintenance lab observe its drain.
		stream, _ := strconv.Atoi(r.URL.Query().Get("stream"))
		gate := os.Getenv("DEMO_STREAM_GATE_FILE")
		gated := gate != "" && r.URL.Query().Get("stream_gate") == "1"
		if stream > 0 || gated {
			limit := min(stream, 30)
			if gated {
				// The rollback lab releases this stream only after observing live
				// drainage. Startup latency must not finish the probe first.
				limit = 120
			}
			flush, ok := w.(http.Flusher)
			if ok {
				flush.Flush()
			}
			for range limit {
				if gated {
					if _, err := os.Stat(gate); err == nil {
						return
					}
				}
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Second):
				}
				fmt.Fprintln(w, "demo stream")
				if ok {
					flush.Flush()
				}
			}
		}
		if size > 0 {
			fmt.Fprintln(w, strings.Repeat("x", min(size, 65536)))
		}
	})
	return mux
}

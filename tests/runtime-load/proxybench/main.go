// proxy-bench supplies a tiny HTTP origin and an independent keep-alive load client.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type samples struct {
	latency  []time.Duration
	rejected int
	failed   int
}

func serve(bytes int) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	body := []byte(strings.Repeat("a", bytes))
	length := strconv.Itoa(bytes)
	server := http.Server{
		ReadHeaderTimeout: 2 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", length)
			_, _ = w.Write(body)
		}),
	}
	log.Fatal(server.Serve(listener))
}

func exchange(conn net.Conn, reader *bufio.Reader, request []byte, method *http.Request) (int, error) {
	if _, err := conn.Write(request); err != nil {
		return 0, err
	}
	response, err := http.ReadResponse(reader, method)
	if err != nil {
		return 0, err
	}
	_, err = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode, err
}

func drive(address string, requests [][]byte, start, stop time.Time, worker, clients, rate int) samples {
	var result samples
	var conn net.Conn
	var reader *bufio.Reader
	method := &http.Request{Method: http.MethodGet}
	time.Sleep(time.Until(start))
	for index := worker; time.Now().Before(stop); index += clients {
		if rate > 0 {
			scheduled := start.Add(time.Duration(index) * time.Second / time.Duration(rate))
			if !scheduled.Before(stop) {
				break
			}
			time.Sleep(time.Until(scheduled))
		}
		begin := time.Now()
		if conn == nil {
			var err error
			conn, err = net.DialTimeout("tcp4", address, time.Second)
			if err != nil {
				result.failed++
				continue
			}
			reader = bufio.NewReader(conn)
			_ = conn.SetDeadline(stop.Add(2 * time.Second))
		}
		status, err := exchange(conn, reader, requests[index%len(requests)], method)
		switch {
		case err != nil:
			result.failed++
			conn.Close()
			conn = nil
		case status == 200:
			result.latency = append(result.latency, time.Since(begin))
		case status == 503:
			result.rejected++
		default:
			result.failed++
		}
	}
	if conn != nil {
		conn.Close()
	}
	return result
}

func load(address string, duration time.Duration, clients, hosts, rate int) {
	requests := make([][]byte, hosts)
	for i := range requests {
		requests[i] = fmt.Appendf(nil, "GET /bench HTTP/1.1\r\nHost: worker-%d.test\r\n\r\n", i)
	}
	start := time.Now().Add(50 * time.Millisecond)
	stop := start.Add(duration)
	results := make(chan samples, clients)
	for client := range clients {
		go func() { results <- drive(address, requests, start, stop, client, clients, rate) }()
	}
	var all samples
	for range clients {
		result := <-results
		all.latency = append(all.latency, result.latency...)
		all.rejected += result.rejected
		all.failed += result.failed
	}
	elapsed := time.Since(start).Seconds()
	if rate > 0 {
		elapsed = max(elapsed, duration.Seconds())
	}
	slices.Sort(all.latency)
	percentile := func(percent int) float64 {
		if len(all.latency) == 0 {
			return 0
		}
		index := (len(all.latency)*percent+99)/100 - 1
		return float64(all.latency[index]) / float64(time.Millisecond)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"connections": clients, "ok": len(all.latency), "http503": all.rejected, "errors": all.failed,
		"requestedQps": rate, "hosts": hosts,
		"elapsedSeconds": elapsed, "okQps": float64(len(all.latency)) / elapsed,
		"p50Ms": percentile(50), "p95Ms": percentile(95), "p99Ms": percentile(99),
	}); err != nil {
		log.Fatal(err)
	}
}

func integer(value string, minimum int) int {
	result, err := strconv.Atoi(value)
	if err != nil || result < minimum {
		log.Fatalf("expected integer >= %d, got %q", minimum, value)
	}
	return result
}

func main() {
	switch {
	case len(os.Args) == 3 && os.Args[1] == "serve":
		serve(integer(os.Args[2], 1))
	case len(os.Args) == 7 && os.Args[1] == "load":
		load(fmt.Sprintf("127.0.0.1:%d", integer(os.Args[2], 1)),
			time.Duration(integer(os.Args[3], 1))*time.Second, integer(os.Args[4], 1),
			integer(os.Args[5], 1), integer(os.Args[6], 0))
	default:
		log.Fatal("usage: proxy-bench serve BYTES | load PORT SECONDS CONNECTIONS HOSTS QPS")
	}
}

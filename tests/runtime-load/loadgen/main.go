package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type row struct {
	pod               string
	slot              string
	code              int
	err               string
	cold              bool
	delay             time.Duration
	latency           time.Duration
	completedInWindow bool
}

func percentile(samples []time.Duration, percent int) any {
	if len(samples) == 0 {
		return nil
	}
	slices.Sort(samples)
	pos := (len(samples)*percent+99)/100 - 1
	return float64(samples[pos]) / float64(time.Millisecond)
}

func main() {
	if len(os.Args) != 4 {
		log.Fatal("usage: light-bench PORT SECONDS RPS")
	}
	seconds, errSeconds := strconv.Atoi(os.Args[2])
	rate, errRate := strconv.Atoi(os.Args[3])
	if errSeconds != nil || errRate != nil || seconds < 1 || rate < 1 {
		log.Fatal("invalid duration or rate")
	}
	const workersPerIngress = 96
	const hosts = 64
	ports := strings.Split(os.Args[1], ",")
	clients := make([]*http.Client, len(ports))
	queues := make([]chan int, len(ports))
	for i := range ports {
		transport := &http.Transport{
			MaxConnsPerHost: workersPerIngress, MaxIdleConnsPerHost: workersPerIngress,
			MaxIdleConns: workersPerIngress, IdleConnTimeout: time.Minute,
		}
		defer transport.CloseIdleConnections()
		clients[i] = &http.Client{Transport: transport, Timeout: 2 * time.Second}
		queues[i] = make(chan int, 256)
	}

	requests := make([]*http.Request, hosts)
	for i := range hosts {
		requests[i], _ = http.NewRequest("GET", fmt.Sprintf("http://127.0.0.1:%s/heap/1/%d/read", ports[i%len(ports)], i), nil)
		requests[i].Host = fmt.Sprintf("light-%d.test", i)
		resp, err := clients[i%len(ports)].Do(requests[i])
		if err != nil {
			log.Fatalf("warmup %d: %s", i, err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || resp.Header.Get("x-experiment-pod") == "" {
			log.Fatalf("warmup %d: status %d, read error %v", i, resp.StatusCode, err)
		}
	}
	epoch := time.Now().Add(75 * time.Millisecond)
	stop := epoch.Add(time.Duration(seconds) * time.Second)
	total := rate * seconds
	rows := make([]row, total)
	var pending sync.WaitGroup
	for ingress, queue := range queues {

		for range workersPerIngress {
			pending.Add(1)
			go func() {
				defer pending.Done()
				for index := range queue {
					start := time.Now()
					r := &rows[index]
					r.delay = start.Sub(epoch.Add(time.Duration(index) * time.Second / time.Duration(rate)))
					resp, err := clients[ingress].Do(requests[index%hosts].Clone(requests[index%hosts].Context()))
					if err != nil {
						r.err = "client_error"
						continue
					}
					r.code = resp.StatusCode
					r.pod = resp.Header.Get("x-experiment-pod")
					r.slot = resp.Header.Get("x-experiment-workerd-slot")
					r.cold = resp.Header.Get("x-experiment-cold") == "1"
					_, err = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					end := time.Now()
					r.latency = end.Sub(start)
					r.completedInWindow = end.Before(stop)
					if err != nil {
						r.err = "body_error"
					}
				}
			}()
		}
	}

	const dispatchers = hosts
	var sending sync.WaitGroup
	for lane := range dispatchers {
		sending.Add(1)
		go func() {
			defer sending.Done()

			for index := lane; index < total; index += dispatchers {
				scheduled := epoch.Add(time.Duration(index) * time.Second / time.Duration(rate))
				time.Sleep(time.Until(scheduled))
				if time.Now().After(stop.Add(3 * time.Millisecond)) {
					rows[index].err = "client_missed_window"
					continue
				}
				select {
				case queues[(index%hosts)%len(ports)] <- index:
				default:
					rows[index].err = "client_queue_full"
				}
			}
		}()
	}
	sending.Wait()

	for _, queue := range queues {
		close(queue)
	}
	pending.Wait()

	var latencies, schedule []time.Duration
	var cold, inWindow int
	failures, byPod, bySlot := map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range rows {
		if r.delay != 0 {
			schedule = append(schedule, r.delay)
		}
		switch {
		case r.err != "":
			failures[r.err]++
		case r.code != 200:
			failures[strconv.Itoa(r.code)]++
		case r.pod == "" || r.slot == "":
			failures["missing_route_headers"]++
		default:
			latencies = append(latencies, r.latency)
			byPod[r.pod]++
			bySlot[r.pod+"/"+r.slot]++
			if r.cold {
				cold++
			}
			if r.completedInWindow {
				inWindow++
			}
		}
	}
	empty := map[string]any{
		"attempted": 0, "succeeded": 0, "cold": 0, "byPod": map[string]int{},
		"bySlot": map[string]int{}, "failures": map[string]int{},
		"p50ms": nil, "p99ms": nil, "scheduledP99SlipMs": nil,
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"compute": empty,
		"fromMs":  epoch.UnixMilli(), "toMs": stop.UnixMilli(),
		"light": map[string]any{
			"attempted": total, "succeeded": len(latencies), "completedInWindow": inWindow,
			"cold": cold, "byPod": byPod, "bySlot": bySlot, "failures": failures,
			"p50ms": percentile(latencies, 50), "p99ms": percentile(latencies, 99),
			"scheduledP99SlipMs": percentile(schedule, 99),
		},
	}); err != nil {
		log.Fatal(err)
	}
}

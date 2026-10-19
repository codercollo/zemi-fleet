package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codercollo/zemi-fleet/internal/clock"
)

type options struct {
	baseURL     string
	apiKey      string
	duration    time.Duration
	concurrency int
}

type counters struct {
	requests atomic.Int64
	success  atomic.Int64
	errors   atomic.Int64
	latency  latencySamples
}

type latencySamples struct {
	sync.Mutex
	values []time.Duration
}

func main() {
	config := options{}
	flag.StringVar(&config.baseURL, "base-url", "http://localhost:4000/api/v2", "development API base URL")
	flag.StringVar(&config.apiKey, "api-key", "", "X-API-Key value; defaults to API_KEY")
	flag.DurationVar(&config.duration, "duration", 30*time.Second, "test duration")
	flag.IntVar(&config.concurrency, "concurrency", 10, "number of concurrent workers")
	flag.Parse()
	if config.apiKey == "" {
		config.apiKey = os.Getenv("API_KEY")
	}
	if config.apiKey == "" {
		config.apiKey = "dev-key-change-me-0123456789"
	}
	if config.duration <= 0 || config.concurrency <= 0 {
		panic("duration and concurrency must be positive")
	}

	results := run(context.Background(), config)
	latencies := results.latency.snapshot()
	fmt.Printf("requests=%d success=%d errors=%d error_rate=%.2f%% p50=%s p95=%s p99=%s\n", results.requests.Load(), results.success.Load(), results.errors.Load(), errorRate(results), percentile(latencies, 0.50), percentile(latencies, 0.95), percentile(latencies, 0.99))
}

func run(parent context.Context, config options) *counters {
	ctx, cancel := context.WithTimeout(parent, config.duration)
	defer cancel()
	results := &counters{}
	client := &http.Client{Timeout: 5 * time.Second}
	var workers sync.WaitGroup
	for worker := 0; worker < config.concurrency; worker++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			random := rand.New(rand.NewSource(int64(workerID + 1)))
			for ctx.Err() == nil {
				path, body := requestFor(random, workerID)
				started := clock.Real{}.Now()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.baseURL+path, bytes.NewReader(body))
				if err != nil {
					results.errors.Add(1)
					results.latency.add(time.Since(started))
					continue
				}
				request.Header.Set("X-API-Key", config.apiKey)
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				results.requests.Add(1)
				results.latency.add(time.Since(started))
				if err != nil {
					results.errors.Add(1)
					continue
				}
				responseBody, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 300 {
					results.success.Add(1)
					if path == "/orders/batch" {
						acceptBatch(ctx, client, config, riderID(workerID), responseBody, results)
					}
				} else {
					results.errors.Add(1)
				}
			}
		}(worker)
	}
	workers.Wait()
	return results
}

func (samples *latencySamples) add(value time.Duration) {
	samples.Lock()
	samples.values = append(samples.values, value)
	samples.Unlock()
}

func (samples *latencySamples) snapshot() []time.Duration {
	samples.Lock()
	defer samples.Unlock()
	values := make([]time.Duration, len(samples.values))
	copy(values, samples.values)
	return values
}

func errorRate(results *counters) float64 {
	requests := results.requests.Load()
	if requests == 0 {
		return 0
	}
	return float64(results.errors.Load()) / float64(requests) * 100
}

func percentile(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	if fraction <= 0 {
		return values[0]
	}
	if fraction >= 1 {
		return values[len(values)-1]
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(first, second int) bool { return ordered[first] < ordered[second] })
	index := int(math.Ceil(fraction*float64(len(ordered))) - 1)
	if index < 0 {
		index = 0
	}
	return ordered[index]
}

func requestFor(random *rand.Rand, workerID int) (string, []byte) {
	currentRiderID := riderID(workerID)
	switch random.Intn(3) {
	case 0:
		return "/riders/match-orders", jsonPayload(map[string]any{
			"rider_id":             currentRiderID,
			"current_location":     map[string]float64{"latitude": -1.285, "longitude": 36.817},
			"bearing_degrees":      90,
			"speed_kmh":            20,
			"current_orders_count": 0,
		})
	case 1:
		return "/orders/match-riders", jsonPayload(map[string]any{
			"order_details": map[string]any{
				"category":          "electronics",
				"pickup_location":   map[string]float64{"lat": -1.285, "lng": 36.817},
				"delivery_location": map[string]float64{"lat": -1.275, "lng": 36.827},
				"value_kes":         500,
			},
		})
	default:
		return "/orders/batch", jsonPayload(map[string]any{
			"rider_id":       currentRiderID,
			"rider_location": map[string]float64{"latitude": -1.285, "longitude": 36.817},
			"max_orders":     5,
			"radius_km":      2,
		})
	}
}

func riderID(workerID int) string {
	return fmt.Sprintf("load_rider_%06d", workerID+1)
}

func jsonPayload(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func acceptBatch(ctx context.Context, client *http.Client, config options, rider string, responseBody []byte, results *counters) {
	var response struct {
		Batches []struct {
			BatchID string `json:"batch_id"`
		} `json:"batches"`
	}
	if json.Unmarshal(responseBody, &response) != nil || len(response.Batches) == 0 || response.Batches[0].BatchID == "" {
		return
	}
	body := jsonPayload(map[string]string{"rider_id": rider})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.baseURL+"/batches/"+response.Batches[0].BatchID+"/accept", bytes.NewReader(body))
	if err != nil {
		results.errors.Add(1)
		return
	}
	request.Header.Set("X-API-Key", config.apiKey)
	request.Header.Set("Content-Type", "application/json")
	acceptResponse, err := client.Do(request)
	results.requests.Add(1)
	if err != nil {
		results.errors.Add(1)
		return
	}
	_, _ = io.Copy(io.Discard, acceptResponse.Body)
	_ = acceptResponse.Body.Close()
	if acceptResponse.StatusCode >= 200 && acceptResponse.StatusCode < 300 {
		results.success.Add(1)
	} else {
		results.errors.Add(1)
	}
}

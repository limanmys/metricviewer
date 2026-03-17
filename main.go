package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var staticFiles embed.FS

// MetricRecord represents a single JSONL metric line.
type MetricRecord struct {
	Timestamp string  `json:"timestamp"`
	TsUnix    float64 `json:"ts_unix"`
	Name      string  `json:"name"`
	Labels    string  `json:"labels"`
	Value     float64 `json:"value"`
	Type      string  `json:"type"`
}

// MetricStore holds parsed metrics and provides query methods.
type MetricStore struct {
	mu       sync.RWMutex
	records  []MetricRecord
	dbPath   string
	lastSize int64
}

func NewMetricStore(dbPath string) *MetricStore {
	return &MetricStore{dbPath: dbPath}
}

// Load reads the JSONL file and parses all records.
func (s *MetricStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.dbPath)
	if err != nil {
		return fmt.Errorf("open metrics file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat metrics file: %w", err)
	}

	if info.Size() == s.lastSize && len(s.records) > 0 {
		return nil
	}
	s.lastSize = info.Size()

	var records []MetricRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec MetricRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan metrics file: %w", err)
	}

	s.records = records
	return nil
}

// MetricNames returns sorted unique metric names.
func (s *MetricStore) MetricNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, r := range s.records {
		seen[r.Name] = struct{}{}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// LabelSets returns unique label sets for a given metric name.
func (s *MetricStore) LabelSets(name string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, r := range s.records {
		if r.Name == name {
			seen[r.Labels] = struct{}{}
		}
	}
	labels := make([]string, 0, len(seen))
	for l := range seen {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return labels
}

// DataPoint is a time-value pair for charting.
type DataPoint struct {
	Timestamp string  `json:"timestamp"`
	TsUnix    float64 `json:"ts_unix"`
	Value     float64 `json:"value"`
}

// Query returns data points for a metric name + labels within a time range.
func (s *MetricStore) Query(name, labels string, start, end float64) []DataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var points []DataPoint
	for _, r := range s.records {
		if r.Name != name {
			continue
		}
		if labels != "" && r.Labels != labels {
			continue
		}
		if start > 0 && r.TsUnix < start {
			continue
		}
		if end > 0 && r.TsUnix > end {
			continue
		}
		points = append(points, DataPoint{
			Timestamp: r.Timestamp,
			TsUnix:    r.TsUnix,
			Value:     r.Value,
		})
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].TsUnix < points[j].TsUnix
	})
	return points
}

// TimeRange returns min and max unix timestamps.
func (s *MetricStore) TimeRange() (float64, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.records) == 0 {
		return 0, 0
	}
	minT := s.records[0].TsUnix
	maxT := s.records[0].TsUnix
	for _, r := range s.records {
		if r.TsUnix < minT {
			minT = r.TsUnix
		}
		if r.TsUnix > maxT {
			maxT = r.TsUnix
		}
	}
	return minT, maxT
}

// CPUDataPoint represents a CPU usage percentage at a point in time.
type CPUDataPoint struct {
	Timestamp string  `json:"timestamp"`
	TsUnix    float64 `json:"ts_unix"`
	UsagePct  float64 `json:"usage_pct"`
}

// CPUResult holds CPU usage data for all cores.
type CPUResult struct {
	Cores    map[string][]CPUDataPoint `json:"cores"`
	NumCores int                       `json:"num_cores"`
}

// CPUUsage calculates CPU usage percentage from counter deltas (like Prometheus rate()).
// Formula: 100 - (rate(idle) * 100) for per-core
// For total: 100 - (rate(idle) / num_cores * 100)
func (s *MetricStore) CPUUsage() CPUResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type idleRecord struct {
		cpu       string
		tsUnix    float64
		timestamp string
		value     float64
	}

	var idles []idleRecord
	cpuSet := make(map[string]struct{})

	for _, r := range s.records {
		if r.Name != "node_cpu_seconds_total" {
			continue
		}
		if !strings.Contains(r.Labels, "mode=idle") {
			continue
		}
		cpu := ""
		for _, part := range strings.Split(r.Labels, ",") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 && kv[0] == "cpu" {
				cpu = kv[1]
			}
		}
		if cpu == "" {
			continue
		}
		idles = append(idles, idleRecord{
			cpu: cpu, tsUnix: r.TsUnix, timestamp: r.Timestamp, value: r.Value,
		})
		if cpu != "total" {
			cpuSet[cpu] = struct{}{}
		}
	}

	numCores := len(cpuSet)

	byCPU := make(map[string][]idleRecord)
	for _, ir := range idles {
		byCPU[ir.cpu] = append(byCPU[ir.cpu], ir)
	}

	result := CPUResult{
		Cores:    make(map[string][]CPUDataPoint),
		NumCores: numCores,
	}

	for cpu, records := range byCPU {
		sort.Slice(records, func(i, j int) bool {
			return records[i].tsUnix < records[j].tsUnix
		})

		var points []CPUDataPoint
		for i := 1; i < len(records); i++ {
			dt := records[i].tsUnix - records[i-1].tsUnix
			if dt <= 0 {
				continue
			}
			dv := records[i].value - records[i-1].value
			if dv < 0 {
				continue // counter reset
			}
			idleRate := dv / dt

			var usagePct float64
			if cpu == "total" && numCores > 0 {
				usagePct = 100.0 - (idleRate/float64(numCores))*100.0
			} else {
				usagePct = 100.0 - idleRate*100.0
			}

			if usagePct < 0 {
				usagePct = 0
			}
			if usagePct > 100 {
				usagePct = 100
			}

			points = append(points, CPUDataPoint{
				Timestamp: records[i].timestamp,
				TsUnix:    records[i].tsUnix,
				UsagePct:  math.Round(usagePct*100) / 100,
			})
		}
		result.Cores[cpu] = points
	}

	return result
}

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// MemoryDataPoint represents memory usage at a point in time.
type MemoryDataPoint struct {
	Timestamp    string  `json:"timestamp"`
	TsUnix       float64 `json:"ts_unix"`
	TotalBytes   float64 `json:"total_bytes"`
	UsedBytes    float64 `json:"used_bytes"`
	AvailBytes   float64 `json:"avail_bytes"`
	FreeBytes    float64 `json:"free_bytes"`
	BuffersBytes float64 `json:"buffers_bytes"`
	CachedBytes  float64 `json:"cached_bytes"`
	SwapTotal    float64 `json:"swap_total"`
	SwapFree     float64 `json:"swap_free"`
	UsedPct      float64 `json:"used_pct"`
	SwapUsedPct  float64 `json:"swap_used_pct"`
}

// MemoryUsage calculates memory usage percentages over time.
func (s *MetricStore) MemoryUsage() []MemoryDataPoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Group memory metrics by timestamp
	type tsData struct {
		timestamp string
		metrics   map[string]float64
	}
	byTs := make(map[float64]*tsData)

	for _, r := range s.records {
		if !strings.HasPrefix(r.Name, "node_memory_") {
			continue
		}
		td, ok := byTs[r.TsUnix]
		if !ok {
			td = &tsData{timestamp: r.Timestamp, metrics: make(map[string]float64)}
			byTs[r.TsUnix] = td
		}
		td.metrics[r.Name] = r.Value
	}

	// Sort timestamps
	tsList := make([]float64, 0, len(byTs))
	for ts := range byTs {
		tsList = append(tsList, ts)
	}
	sort.Float64s(tsList)

	var points []MemoryDataPoint
	for _, ts := range tsList {
		td := byTs[ts]
		m := td.metrics

		total := m["node_memory_MemTotal_bytes"]
		avail := m["node_memory_MemAvailable_bytes"]
		free := m["node_memory_MemFree_bytes"]
		buffers := m["node_memory_Buffers_bytes"]
		cached := m["node_memory_Cached_bytes"]
		swapTotal := m["node_memory_SwapTotal_bytes"]
		swapFree := m["node_memory_SwapFree_bytes"]

		used := total - avail
		if total <= 0 {
			continue
		}

		usedPct := math.Round((used/total)*10000) / 100
		var swapUsedPct float64
		if swapTotal > 0 {
			swapUsedPct = math.Round(((swapTotal-swapFree)/swapTotal)*10000) / 100
		}

		points = append(points, MemoryDataPoint{
			Timestamp:    td.timestamp,
			TsUnix:       ts,
			TotalBytes:   total,
			UsedBytes:    used,
			AvailBytes:   avail,
			FreeBytes:    free,
			BuffersBytes: buffers,
			CachedBytes:  cached,
			SwapTotal:    swapTotal,
			SwapFree:     swapFree,
			UsedPct:      usedPct,
			SwapUsedPct:  swapUsedPct,
		})
	}

	return points
}

// DiskIODataPoint represents disk I/O rates at a point in time.
type DiskIODataPoint struct {
	Timestamp        string  `json:"timestamp"`
	TsUnix           float64 `json:"ts_unix"`
	ReadBytesPerSec  float64 `json:"read_bytes_per_sec"`
	WriteBytesPerSec float64 `json:"write_bytes_per_sec"`
}

// DiskIOResult holds disk I/O data for all devices.
type DiskIOResult struct {
	Devices map[string][]DiskIODataPoint `json:"devices"`
}

// DiskIO calculates read/write bytes per second from counter deltas.
func (s *MetricStore) DiskIO() DiskIOResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type devRecord struct {
		tsUnix    float64
		timestamp string
		value     float64
	}

	readBytes := make(map[string][]devRecord)
	writeBytes := make(map[string][]devRecord)

	for _, r := range s.records {
		if r.Name != "node_disk_read_bytes_total" && r.Name != "node_disk_written_bytes_total" {
			continue
		}
		dev := ""
		for _, part := range strings.Split(r.Labels, ",") {
			kv := strings.SplitN(part, "=", 2)
			if len(kv) == 2 && kv[0] == "device" {
				dev = kv[1]
			}
		}
		if dev == "" {
			continue
		}
		rec := devRecord{tsUnix: r.TsUnix, timestamp: r.Timestamp, value: r.Value}
		if r.Name == "node_disk_read_bytes_total" {
			readBytes[dev] = append(readBytes[dev], rec)
		} else {
			writeBytes[dev] = append(writeBytes[dev], rec)
		}
	}

	result := DiskIOResult{Devices: make(map[string][]DiskIODataPoint)}

	devSet := make(map[string]struct{})
	for dev := range readBytes {
		devSet[dev] = struct{}{}
	}
	for dev := range writeBytes {
		devSet[dev] = struct{}{}
	}

	for dev := range devSet {
		recs := readBytes[dev]
		wrecs := writeBytes[dev]

		sort.Slice(recs, func(i, j int) bool { return recs[i].tsUnix < recs[j].tsUnix })
		sort.Slice(wrecs, func(i, j int) bool { return wrecs[i].tsUnix < wrecs[j].tsUnix })

		readRates := make(map[float64]float64)
		writeRates := make(map[float64]float64)
		timestamps := make(map[float64]string)

		for i := 1; i < len(recs); i++ {
			dt := recs[i].tsUnix - recs[i-1].tsUnix
			if dt <= 0 {
				continue
			}
			dv := recs[i].value - recs[i-1].value
			if dv < 0 {
				continue
			}
			readRates[recs[i].tsUnix] = dv / dt
			timestamps[recs[i].tsUnix] = recs[i].timestamp
		}

		for i := 1; i < len(wrecs); i++ {
			dt := wrecs[i].tsUnix - wrecs[i-1].tsUnix
			if dt <= 0 {
				continue
			}
			dv := wrecs[i].value - wrecs[i-1].value
			if dv < 0 {
				continue
			}
			writeRates[wrecs[i].tsUnix] = dv / dt
			timestamps[wrecs[i].tsUnix] = wrecs[i].timestamp
		}

		tsSet := make(map[float64]struct{})
		for ts := range readRates {
			tsSet[ts] = struct{}{}
		}
		for ts := range writeRates {
			tsSet[ts] = struct{}{}
		}

		tsList := make([]float64, 0, len(tsSet))
		for ts := range tsSet {
			tsList = append(tsList, ts)
		}
		sort.Float64s(tsList)

		var points []DiskIODataPoint
		for _, ts := range tsList {
			points = append(points, DiskIODataPoint{
				Timestamp:        timestamps[ts],
				TsUnix:           ts,
				ReadBytesPerSec:  math.Round(readRates[ts]*100) / 100,
				WriteBytesPerSec: math.Round(writeRates[ts]*100) / 100,
			})
		}
		result.Devices[dev] = points
	}

	return result
}

func main() {
	dbPath := flag.String("db", "metrics.db", "Path to metrics JSONL file")
	listen := flag.String("listen", ":9099", "Listen address (e.g. :9099)")
	flag.Parse()

	store := NewMetricStore(*dbPath)
	if err := store.Load(); err != nil {
		log.Fatalf("Failed to load metrics: %v", err)
	}
	log.Printf("Loaded metrics from %s", *dbPath)

	// Background reload every 30 seconds
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := store.Load(); err != nil {
				log.Printf("Warning: reload failed: %v", err)
			}
		}
	}()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Load(); err != nil {
			http.Error(w, "reload error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.MetricNames())
	})

	mux.HandleFunc("GET /api/labels", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name parameter required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.LabelSets(name))
	})

	mux.HandleFunc("GET /api/query", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name parameter required", http.StatusBadRequest)
			return
		}
		labels := r.URL.Query().Get("labels")
		start := parseFloat(r.URL.Query().Get("start"))
		end := parseFloat(r.URL.Query().Get("end"))

		points := store.Query(name, labels, start, end)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(points)
	})

	mux.HandleFunc("GET /api/timerange", func(w http.ResponseWriter, r *http.Request) {
		minT, maxT := store.TimeRange()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]float64{"min": minT, "max": maxT})
	})

	mux.HandleFunc("GET /api/cpu", func(w http.ResponseWriter, r *http.Request) {
		result := store.CPUUsage()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("GET /api/memory", func(w http.ResponseWriter, r *http.Request) {
		result := store.MemoryUsage()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("GET /api/diskio", func(w http.ResponseWriter, r *http.Request) {
		result := store.DiskIO()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	log.Printf("MetricViewer listening on %s", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

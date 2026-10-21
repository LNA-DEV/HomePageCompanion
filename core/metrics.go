package core

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// metrics is a small Prometheus-text registry: counters, gauges and
// summaries (count and sum), each with labels. The core measures jobs,
// events, HTTP requests and capability calls itself; building blocks add
// their own through companion.Metrics.
type metrics struct {
	mu       sync.Mutex
	counters map[string]map[string]float64 // name → label set → value
	gauges   map[string]map[string]float64
	sums     map[string]map[string][2]float64 // count, sum
	// history keeps one point per minute for the dashboard widget.
	history []point
}

type point struct {
	Time     time.Time `json:"time"`
	Requests float64   `json:"requests"`
	Errors   float64   `json:"errors"`
	JobsOK   float64   `json:"jobsOk"`
	JobsFail float64   `json:"jobsFailed"`
}

func newMetrics() *metrics {
	return &metrics{
		counters: map[string]map[string]float64{},
		gauges:   map[string]map[string]float64{},
		sums:     map[string]map[string][2]float64{},
	}
}

// labelKey renders label pairs as Prometheus does: name="value",…
func labelKey(labels []string) string {
	if len(labels)%2 == 1 {
		labels = append(labels, "")
	}
	pairs := make([]string, 0, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(labels[i+1])
		pairs = append(pairs, fmt.Sprintf(`%s="%s"`, labels[i], v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (m *metrics) add(name string, delta float64, labels ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counters[name] == nil {
		m.counters[name] = map[string]float64{}
	}
	m.counters[name][labelKey(labels)] += delta
}

func (m *metrics) set(name string, v float64, labels ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gauges[name] == nil {
		m.gauges[name] = map[string]float64{}
	}
	m.gauges[name][labelKey(labels)] = v
}

func (m *metrics) observe(name string, v float64, labels ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sums[name] == nil {
		m.sums[name] = map[string][2]float64{}
	}
	s := m.sums[name][labelKey(labels)]
	m.sums[name][labelKey(labels)] = [2]float64{s[0] + 1, s[1] + v}
}

// total sums a counter over all label sets matching the filter.
func (m *metrics) total(name string, match func(labels string) bool) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var t float64
	for k, v := range m.counters[name] {
		if match == nil || match(k) {
			t += v
		}
	}
	return t
}

// sample appends the current totals to the history (once a minute, a day
// long).
func (m *metrics) sample(now time.Time) {
	p := point{
		Time:     now,
		Requests: m.total("companion_http_requests_total", nil),
		Errors:   m.total("companion_http_requests_total", func(l string) bool { return strings.Contains(l, `code="5`) }),
		JobsOK:   m.total("companion_job_runs_total", func(l string) bool { return strings.Contains(l, `result="ok"`) }),
		JobsFail: m.total("companion_job_runs_total", func(l string) bool { return strings.Contains(l, `result="error"`) }),
	}
	m.mu.Lock()
	m.history = append(m.history, p)
	if len(m.history) > 24*60 {
		m.history = m.history[len(m.history)-24*60:]
	}
	m.mu.Unlock()
}

func (m *metrics) historyCopy() []point {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]point{}, m.history...)
}

// write renders every metric in the Prometheus text format.
func (m *metrics) write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	writeSet := func(kind string, set map[string]map[string]float64) {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(w, "# TYPE %s %s\n", n, kind)
			keys := make([]string, 0, len(set[n]))
			for k := range set[n] {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "%s%s %s\n", n, braces(k), num(set[n][k]))
			}
		}
	}
	writeSet("counter", m.counters)
	writeSet("gauge", m.gauges)
	names := make([]string, 0, len(m.sums))
	for n := range m.sums {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "# TYPE %s summary\n", n)
		keys := make([]string, 0, len(m.sums[n]))
		for k := range m.sums[n] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := m.sums[n][k]
			fmt.Fprintf(w, "%s_count%s %s\n", n, braces(k), num(s[0]))
			fmt.Fprintf(w, "%s_sum%s %s\n", n, braces(k), num(s[1]))
		}
	}
}

func braces(k string) string {
	if k == "" {
		return ""
	}
	return "{" + k + "}"
}

func num(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// scopedMetrics is a building block's companion.Metrics: every value gets
// its block="<id>" label.
type scopedMetrics struct {
	m     *metrics
	block string
}

func (m *metrics) scope(block string) scopedMetrics { return scopedMetrics{m: m, block: block} }

func (s scopedMetrics) Add(name string, delta float64, labels ...string) {
	s.m.add(name, delta, append([]string{"block", s.block}, labels...)...)
}

func (s scopedMetrics) Set(name string, v float64, labels ...string) {
	s.m.set(name, v, append([]string{"block", s.block}, labels...)...)
}

// Package metrics renders a small subset of the Prometheus text exposition
// format (version 0.0.4) without external dependencies: counters, histograms and
// function-backed gauges and counters.
//
// Label values must come from a small fixed set chosen by the caller. Never use
// tenant identifiers, service identifiers, tokens or other request data as label
// values; every series is capped to protect against accidental cardinality growth.
package metrics

import (
	"bytes"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ContentType is the media type of the text exposition format.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// MaxSeries caps the number of label combinations kept per metric. Combinations
// beyond it are dropped.
const MaxSeries = 500

var (
	metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNamePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// Label is one name/value pair of a sample.
type Label struct{ Name, Value string }

// Encoder accumulates exposition text.
type Encoder struct{ buf bytes.Buffer }

// Header writes the HELP and TYPE lines of a metric family.
func (e *Encoder) Header(name, help, typ string) {
	e.buf.WriteString("# HELP " + name + " " + escapeHelp(help) + "\n")
	e.buf.WriteString("# TYPE " + name + " " + typ + "\n")
}

// Sample writes one sample line.
func (e *Encoder) Sample(name string, labels []Label, value float64) {
	e.buf.WriteString(name)
	if len(labels) > 0 {
		e.buf.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				e.buf.WriteByte(',')
			}
			e.buf.WriteString(l.Name + `="` + escapeLabelValue(l.Value) + `"`)
		}
		e.buf.WriteByte('}')
	}
	e.buf.WriteByte(' ')
	e.buf.WriteString(formatFloat(value))
	e.buf.WriteByte('\n')
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func escapeHelp(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

func escapeLabelValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// Collector writes one or more metric families.
type Collector interface{ Collect(*Encoder) }

// Registry holds collectors and renders them in registration order.
type Registry struct {
	mu         sync.Mutex
	collectors []Collector
}

func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) Register(c Collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.collectors = append(r.collectors, c)
}

// WriteTo renders every registered collector.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.Lock()
	collectors := append([]Collector(nil), r.collectors...)
	r.mu.Unlock()
	var e Encoder
	for _, c := range collectors {
		c.Collect(&e)
	}
	n, err := w.Write(e.buf.Bytes())
	return int64(n), err
}

func validateNames(name string, labels []string) {
	if !metricNamePattern.MatchString(name) {
		panic("metrics: invalid metric name " + name)
	}
	for _, l := range labels {
		if !labelNamePattern.MatchString(l) || strings.HasPrefix(l, "__") {
			panic("metrics: invalid label name " + l)
		}
	}
}

func labelPairs(names, values []string) []Label {
	out := make([]Label, len(names))
	for i := range names {
		out[i] = Label{names[i], values[i]}
	}
	return out
}

func seriesKey(values []string) string { return strings.Join(values, "\xff") }

// CounterVec is a counter family with fixed label names.
type CounterVec struct {
	name, help string
	labels     []string
	mu         sync.Mutex
	values     map[string]*counterSeries
}

type counterSeries struct {
	labelValues []string
	count       uint64
}

func NewCounterVec(name, help string, labels ...string) *CounterVec {
	validateNames(name, labels)
	return &CounterVec{name: name, help: help, labels: labels, values: map[string]*counterSeries{}}
}

// Inc adds one to the series for the label values. A call with the wrong number
// of values, or past MaxSeries combinations, is ignored.
func (c *CounterVec) Inc(values ...string) {
	if len(values) != len(c.labels) {
		return
	}
	key := seriesKey(values)
	c.mu.Lock()
	defer c.mu.Unlock()
	s, exists := c.values[key]
	if !exists {
		if len(c.values) >= MaxSeries {
			return
		}
		s = &counterSeries{labelValues: append([]string(nil), values...)}
		c.values[key] = s
	}
	s.count++
}

func (c *CounterVec) Collect(e *Encoder) {
	c.mu.Lock()
	keys := make([]string, 0, len(c.values))
	for k := range c.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type row struct {
		labels []Label
		value  float64
	}
	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		s := c.values[k]
		rows = append(rows, row{labelPairs(c.labels, s.labelValues), float64(s.count)})
	}
	c.mu.Unlock()
	e.Header(c.name, c.help, "counter")
	for _, r := range rows {
		e.Sample(c.name, r.labels, r.value)
	}
}

// HistogramVec is a histogram family with fixed buckets and label names.
type HistogramVec struct {
	name, help string
	labels     []string
	buckets    []float64
	mu         sync.Mutex
	values     map[string]*histogramSeries
}

type histogramSeries struct {
	labelValues []string
	counts      []uint64 // per bucket, not cumulative
	count       uint64
	sum         float64
}

// DefaultBuckets suit HTTP request latency in seconds.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

func NewHistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	validateNames(name, labels)
	for _, l := range labels {
		if l == "le" {
			panic("metrics: label name le is reserved")
		}
	}
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	return &HistogramVec{name: name, help: help, labels: labels, buckets: b, values: map[string]*histogramSeries{}}
}

// Observe records one value in seconds.
func (h *HistogramVec) Observe(value float64, labelValues ...string) {
	if len(labelValues) != len(h.labels) || math.IsNaN(value) {
		return
	}
	key := seriesKey(labelValues)
	h.mu.Lock()
	defer h.mu.Unlock()
	s, exists := h.values[key]
	if !exists {
		if len(h.values) >= MaxSeries {
			return
		}
		s = &histogramSeries{labelValues: append([]string(nil), labelValues...), counts: make([]uint64, len(h.buckets))}
		h.values[key] = s
	}
	s.count++
	s.sum += value
	for i, upper := range h.buckets {
		if value <= upper {
			s.counts[i]++
			break
		}
	}
}

func (h *HistogramVec) Collect(e *Encoder) {
	h.mu.Lock()
	keys := make([]string, 0, len(h.values))
	for k := range h.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	snapshot := make([]histogramSeries, 0, len(keys))
	for _, k := range keys {
		s := *h.values[k]
		s.counts = append([]uint64(nil), s.counts...)
		snapshot = append(snapshot, s)
	}
	h.mu.Unlock()
	e.Header(h.name, h.help, "histogram")
	for _, s := range snapshot {
		base := labelPairs(h.labels, s.labelValues)
		var cumulative uint64
		for i, upper := range h.buckets {
			cumulative += s.counts[i]
			e.Sample(h.name+"_bucket", append(append([]Label(nil), base...), Label{"le", formatFloat(upper)}), float64(cumulative))
		}
		e.Sample(h.name+"_bucket", append(append([]Label(nil), base...), Label{"le", "+Inf"}), float64(s.count))
		e.Sample(h.name+"_sum", base, s.sum)
		e.Sample(h.name+"_count", base, float64(s.count))
	}
}

// Sample is one value of a function-backed metric.
type Sample struct {
	LabelValues []string
	Value       float64
}

// FuncCollector is a metric family whose samples come from a function called at
// scrape time. Type is "gauge" or "counter".
type FuncCollector struct {
	Name, Help, Type string
	Labels           []string
	Fn               func() []Sample
}

func NewGaugeFunc(name, help string, labels []string, fn func() []Sample) *FuncCollector {
	validateNames(name, labels)
	return &FuncCollector{Name: name, Help: help, Type: "gauge", Labels: labels, Fn: fn}
}

func NewCounterFunc(name, help string, labels []string, fn func() []Sample) *FuncCollector {
	validateNames(name, labels)
	return &FuncCollector{Name: name, Help: help, Type: "counter", Labels: labels, Fn: fn}
}

func (f *FuncCollector) Collect(e *Encoder) {
	samples := f.Fn()
	if len(samples) > MaxSeries {
		samples = samples[:MaxSeries]
	}
	e.Header(f.Name, f.Help, f.Type)
	for _, s := range samples {
		if len(s.LabelValues) != len(f.Labels) {
			continue
		}
		e.Sample(f.Name, labelPairs(f.Labels, s.LabelValues), s.Value)
	}
}

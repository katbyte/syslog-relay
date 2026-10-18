package metrics

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// List is every reading a tool's page can hold, declared in one place so its collector can name them all. A tool keeps one in a variable of its own.
type List struct {
	namespace string
	labels    []string

	mu    sync.Mutex
	descs []*prometheus.Desc
}

// NewList is an empty list of readings whose names start with namespace. Labels, when given, start every reading's labels: "card" for a tool that
// reports on several cards, each card's name then given to NewWriter.
func NewList(namespace string, labels ...string) *List {
	return &List{namespace: namespace, labels: labels}
}

// Reading adds one reading to the list, and is what a Writer writes it with: its name after the namespace, what it is, and the labels it has beside
// the list's own. A name or label a metrics store would not take is refused by New, when the tool starts.
func (l *List) Reading(name, help string, labels ...string) *prometheus.Desc {
	full, all := l.namespace+"_"+name, slices.Concat(l.labels, labels)

	d := prometheus.NewDesc(full, help, all, nil)
	if err := storable(full, all); err != nil {
		d = prometheus.NewInvalidDesc(err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.descs = append(l.descs, d)

	return d
}

// storable says what a metrics store would not take about a reading's name or its labels' names. Prometheus's own library takes more than every store
// does, a dash in a name say.
func storable(name string, labels []string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q cannot be a reading's name: letters, digits and underscores, not starting with a digit", name)
	}

	for _, label := range labels {
		if !namePattern.MatchString(label) || strings.HasPrefix(label, reservedPrefix) {
			return fmt.Errorf("%q cannot be the name of a label of %s: letters, digits and underscores, not starting with a digit or with two underscores", label, name)
		}
	}

	return nil
}

// Describe names every reading on the list, for a collector's own Describe to hand on.
func (l *List) Describe(ch chan<- *prometheus.Desc) {
	l.mu.Lock()
	descs := slices.Clone(l.descs)
	l.mu.Unlock()

	for _, d := range descs {
		ch <- d
	}
}

// Number is any number a reading can be given: a count, a size, a total from the kernel, a temperature.
type Number interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}

// Writer writes a collector's readings. A second reading with the same name and labels is left off: it would spoil the whole page, and what a tool
// reports on seldom promises that two of its things have different names.
type Writer struct {
	ch      chan<- prometheus.Metric
	values  []string
	written map[written]bool
	seen    map[string]bool
}

// written is one reading already on the page, by what it is and its labels.
type written struct {
	desc   *prometheus.Desc
	labels string
}

// NewWriter writes to ch, the channel a collector's Collect is given. Values are for the list's own labels, in the same order: the card's name, say.
func NewWriter(ch chan<- prometheus.Metric, values ...string) *Writer {
	return &Writer{ch: ch, values: values, written: map[written]bool{}, seen: map[string]bool{}}
}

// First says whether a thing has not been seen before, and notes that it now has: a slot by its enclosure and number, say. Writing a thing's readings
// only the first time keeps one listed twice off the page whole, where a reading that carries its value in a label, a status, would differ.
func (w *Writer) First(kind string, labels ...string) bool {
	key := strings.Join(slices.Concat([]string{kind}, w.values, labels), "\x00")
	if w.seen[key] {
		return false
	}
	w.seen[key] = true

	return true
}

// write writes one reading, unless one of that name and those labels is already on the page. One that cannot be written, with the wrong number of
// labels say, is left off the page and logged, and the rest of the page is still served.
func (w *Writer) write(d *prometheus.Desc, kind prometheus.ValueType, value float64, labels ...string) {
	all := slices.Concat(w.values, labels)

	key := written{desc: d, labels: strings.Join(all, "\x00")}
	if w.written[key] {
		return
	}
	w.written[key] = true

	m, err := prometheus.NewConstMetric(d, kind, value, all...)
	if err != nil {
		m = prometheus.NewInvalidMetric(d, err)
	}

	w.ch <- m
}

// Gauge writes a reading that can go up and down.
func (w *Writer) Gauge[N Number](d *prometheus.Desc, value N, labels ...string) {
	w.write(d, prometheus.GaugeValue, float64(value), labels...)
}

// Total writes a reading that only goes up until whatever keeps it starts again: a count of failures, or of errors since a disk was fitted.
func (w *Writer) Total[N Number](d *prometheus.Desc, value N, labels ...string) {
	w.write(d, prometheus.CounterValue, float64(value), labels...)
}

// Flag is Gauge for a yes or no: 1 or 0.
func (w *Writer) Flag(d *prometheus.Desc, yes bool, labels ...string) { //nolint:revive // flag-parameter: the yes or no is the reading, not a switch for what the call does
	if yes {
		w.Gauge(d, 1, labels...)
		return
	}

	w.Gauge(d, 0, labels...)
}

// At is Gauge for a moment, in seconds since 1970. The zero time, for something that has not happened, is left off.
func (w *Writer) At(d *prometheus.Desc, t time.Time, labels ...string) {
	if t.IsZero() {
		return
	}

	w.Gauge(d, float64(t.UnixMilli())/1000, labels...)
}

// States writes 1 for the state something is in and 0 for each other state it could be in, so none vanishes when it is left. A state not among those
// known is written too, at 1: what is reported on may be newer than the tool. The state is the last label, after the ones given.
func (w *Writer) States[S ~string](d *prometheus.Desc, known []S, is S, labels ...string) {
	for _, state := range known {
		w.Flag(d, state == is, slices.Concat(labels, []string{string(state)})...)
	}

	if is != "" {
		w.Gauge(d, 1, slices.Concat(labels, []string{string(is)})...)
	}
}

// Counts writes how many there are of each kind: every kind known, at 0 when there is none, and any other kind there turns out to be. The kind is the
// last label, after the ones given.
func (w *Writer) Counts[K ~string](d *prometheus.Desc, known []K, n map[K]int, labels ...string) {
	for _, kind := range known {
		w.Gauge(d, n[kind], slices.Concat(labels, []string{string(kind)})...)
	}

	for kind, count := range n {
		w.Gauge(d, count, slices.Concat(labels, []string{string(kind)})...)
	}
}

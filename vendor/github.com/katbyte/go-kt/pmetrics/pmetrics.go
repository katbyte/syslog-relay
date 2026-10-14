// Package pmetrics is a tool's Prometheus metrics page: its own readings and nothing else, with the labels whoever runs it wants on every one, written as
// text or served over HTTP beside a health probe.
package pmetrics

import (
	"fmt"
	"io"
	"maps"
	"regexp"
	"runtime"
	"slices"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/katbyte/go-kt/version"
)

// namePattern is what a metrics store takes as the start of a reading's name.
var namePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Options says what the page is.
type Options struct {
	// Name is the tool's name, "leontp-metrics": what the root of the server answers with, beside its version.
	Name string
	// Namespace starts the name of the one reading the page adds itself, which says what build wrote it: "leontp" gives leontp_metrics_build_info.
	Namespace string
	// Labels are put on every reading on the page: hostname=ntp say, for a store that tells one source from another by them. ParseLabels reads
	// them from a setting.
	Labels map[string]string
}

// Page holds a tool's readings and writes them out. It holds none of the Go runtime's or the process's own.
type Page struct {
	name     string
	registry *prometheus.Registry
}

// New is the page of these collectors' readings, each with the labels on it, and one more that says which build wrote the page. A label under a name
// a reading already has is an error, as is one a metrics store would not take: said when the tool starts, not found missing on a dashboard.
func New(o Options, collectors ...prometheus.Collector) (*Page, error) {
	if !namePattern.MatchString(o.Namespace) {
		return nil, fmt.Errorf("%q cannot start a reading's name: letters, digits and underscores, not starting with a digit", o.Namespace)
	}

	if err := ValidateLabels(o.Labels); err != nil {
		return nil, err
	}

	all := append([]prometheus.Collector{newBuildInfo(o.Namespace)}, collectors...)

	// each label is tried alone first, so the one that clashes can be named: the registry's own words for it run to a paragraph and name none
	for _, name := range slices.Sorted(maps.Keys(o.Labels)) {
		if register(prometheus.Labels{name: o.Labels[name]}, prometheus.NewRegistry(), all) != nil && register(nil, prometheus.NewRegistry(), all) == nil {
			return nil, fmt.Errorf("the label %q is one a reading on the page already has", name)
		}
	}

	p := &Page{name: o.Name, registry: prometheus.NewRegistry()}
	if err := register(o.Labels, p.registry, all); err != nil {
		return nil, fmt.Errorf("putting the readings on the page: %w", err)
	}

	return p, nil
}

// register puts the collectors' readings in the registry with the labels on each.
func register(labels prometheus.Labels, registry *prometheus.Registry, collectors []prometheus.Collector) error {
	labelled := prometheus.WrapRegistererWith(labels, registry)
	for _, c := range collectors {
		if err := labelled.Register(c); err != nil {
			return err
		}
	}

	return nil
}

// Write writes the page as text, the way a scrape reads it.
func (p *Page) Write(w io.Writer) error {
	families, err := p.registry.Gather()
	if err != nil {
		return fmt.Errorf("gathering the readings: %w", err)
	}

	enc := expfmt.NewEncoder(w, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := enc.Encode(family); err != nil {
			return fmt.Errorf("writing %s: %w", family.GetName(), err)
		}
	}

	return nil
}

// buildInfo is the reading that says which build of the tool wrote the page: always 1, the build in its labels.
type buildInfo struct {
	desc *prometheus.Desc
}

func newBuildInfo(namespace string) buildInfo {
	return buildInfo{desc: prometheus.NewDesc(namespace+"_metrics_build_info", "Always 1, with the version of the tool that wrote the page.", []string{"version", "commit", "goversion"}, nil)}
}

func (b buildInfo) Describe(ch chan<- *prometheus.Desc) {
	ch <- b.desc
}

func (b buildInfo) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(b.desc, prometheus.GaugeValue, 1, version.Version, version.GitCommit, runtime.Version())
}

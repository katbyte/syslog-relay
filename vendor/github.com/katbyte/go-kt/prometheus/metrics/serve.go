package metrics

import (
	"context"
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/httpd"
	"github.com/katbyte/go-kt/version"
)

const (
	// Path is where the page is served.
	Path = "/metrics"
	// HealthPath answers "ok" while the tool is serving, whatever state the thing it reports on is in.
	HealthPath = httpd.HealthPath
)

// scrapeErrors sends what goes wrong writing the page to the log, as errors.
type scrapeErrors struct{}

func (scrapeErrors) Println(v ...any) { clog.Log.Error(v...) }

// Handler is the routes: the page at Path, the health probe at HealthPath, and at the root a few lines saying where they are. Apart from
// Serve so they can be tested or mounted elsewhere without a port.
func (p *Page) Handler() http.Handler {
	mux := http.NewServeMux()
	// a reading that cannot be written is left out and logged; the rest of the page is still served
	mux.Handle("GET "+Path, promhttp.HandlerFor(p.registry, promhttp.HandlerOpts{ErrorLog: scrapeErrors{}, ErrorHandling: promhttp.ContinueOnError}))
	httpd.Health(mux)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%s %s\n%s\n%s\n", p.name, version.Version, Path, HealthPath)
	})

	return mux
}

// Serve serves h on listen until the context ends, then lets requests under way finish. Whoever calls it ends the context on a signal.
func Serve(ctx context.Context, listen string, h http.Handler) error {
	ln, err := httpd.Listen(ctx, listen)
	if err != nil {
		return err
	}

	clog.Log.Infof("serving the metrics page on %s%s", ln.Addr(), Path)

	return httpd.Serve(ctx, ln, h)
}

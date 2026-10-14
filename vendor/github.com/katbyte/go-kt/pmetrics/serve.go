package pmetrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/katbyte/go-kt/clog"
	"github.com/katbyte/go-kt/version"
)

const (
	// Path is where the page is served.
	Path = "/metrics"
	// HealthPath answers "ok" while the tool is serving, whatever state the thing it reports on is in: a container is not unhealthy because what it
	// watches is down.
	HealthPath = "/healthz"

	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
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
	mux.HandleFunc("GET "+HealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "%s %s\n%s\n%s\n", p.name, version.Version, Path, HealthPath)
	})

	return mux
}

// Serve serves h on listen until the context ends, then lets requests under way finish. Whoever calls it ends the context on a signal.
func Serve(ctx context.Context, listen string, h http.Handler) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", listen, err)
	}

	srv := &http.Server{Handler: h, ReadHeaderTimeout: readHeaderTimeout}
	clog.Log.Infof("serving the metrics page on %s%s", ln.Addr(), Path)

	failed := make(chan error, 1)
	go func() { failed <- srv.Serve(ln) }()

	select {
	case err := <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("serving on %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}

	clog.Log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	return nil
}

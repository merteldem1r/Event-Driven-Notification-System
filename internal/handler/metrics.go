package handler

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// unmatchedRoute labels requests to URLs without a route, so random URLs can't create new series.
const unmatchedRoute = "unmatched"

// RequestMetrics counts requests and measures their duration per method, route and status.
// The route is chi's pattern, e.g. /api/v1/notifications/{id}: one series for all IDs.
func RequestMetrics(reg prometheus.Registerer) func(http.Handler) http.Handler {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP requests handled, by method, route and status code.",
	}, []string{"method", "route", "status"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time to handle an HTTP request, by method and route.",
		Buckets: prometheus.DefBuckets, // 5 ms to 10 s
	}, []string{"method", "route"})
	reg.MustRegister(requests, duration)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // nothing written: net/http sends 200
			}
			route := routeOf(r)
			requests.WithLabelValues(r.Method, route, strconv.Itoa(status)).Inc()
			duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		})
	}
}

// routeOf returns the chi pattern that handled r. It is known only after routing, so it is read
// after the handler ran. No pattern, or a catch-all ("/api/v1/*", set by chi when nothing under
// /api/v1 matched), means no route: we have no wildcard routes of our own.
func routeOf(r *http.Request) string {
	rc := chi.RouteContext(r.Context())
	if rc == nil {
		return unmatchedRoute
	}
	pattern := rc.RoutePattern()
	if pattern == "" || strings.HasSuffix(pattern, "*") {
		return unmatchedRoute
	}
	return pattern
}

// Metrics serves the registry's metrics in Prometheus's text format. If one collector fails
// (e.g. the queue query), the others are still served and the error is logged.
func Metrics(gatherer prometheus.Gatherer, logger *slog.Logger) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      promLogger{logger},
	})
}

// promLogger passes the Prometheus library's error messages to slog.
type promLogger struct{ logger *slog.Logger }

// Println logs v as one error line.
func (l promLogger) Println(v ...any) {
	l.logger.Error("metrics: " + fmt.Sprint(v...))
}

package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"example.com/skeleton/internal/resilience"
)

// HandlerConfig tunes the standard chain. Zero values get safe defaults.
type HandlerConfig struct {
	MaxBodyBytes  int64         // default 1 MiB
	Deadline      time.Duration // per-request budget, default 5s (< Config.WriteTimeout)
	LogAll        bool          // false: log only 5xx and slow requests
	SlowThreshold time.Duration // default 1s
	OTel          []otelhttp.Option
}

// Routes registers handlers on the standard chain.
type Routes struct {
	// API routes get load shedding, a request deadline and a body limit.
	API func(chi.Router)
	// Streams (SSE, long polling, large downloads) skip load shedding and the
	// deadline: they hold a connection for minutes, so a limiter slot or a 5s
	// budget would be wrong. End them on the drain channel from cmd/server,
	// and bound them by connection count.
	Streams func(chi.Router)
}

// NewHandler wraps routes in the standard chain. Order, outermost first:
//
//	otelhttp   spans and metrics for everything, including shed requests
//	RouteTag   low-cardinality span name and http.route
//	AccessLog  sees the final status, including 500s from Recover and 503s from LoadShed
//	Recover    a panic anywhere below becomes a 500 (or an aborted response)
//	-- API group only --
//	LoadShed   rejects before any body is read or work is done
//	Deadline   request budget for everything downstream
//	BodyLimit  caps memory per request (streams get it too)
//
// Health and pprof live on the admin port, so probes are never shed.
func NewHandler(logger *slog.Logger, limiter *resilience.Limiter, cfg HandlerConfig, routes Routes) http.Handler {
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	cfg.Deadline = orDefault(cfg.Deadline, 5*time.Second)
	cfg.SlowThreshold = orDefault(cfg.SlowThreshold, time.Second)

	r := chi.NewRouter()
	r.Use(RouteTag, AccessLog(logger, !cfg.LogAll, cfg.SlowThreshold), Recover(logger))
	r.Group(func(api chi.Router) {
		api.Use(LoadShed(limiter), Deadline(cfg.Deadline), BodyLimit(cfg.MaxBodyBytes))
		if routes.API != nil {
			routes.API(api)
		}
	})
	if routes.Streams != nil {
		r.Group(func(streams chi.Router) {
			streams.Use(BodyLimit(cfg.MaxBodyBytes))
			routes.Streams(streams)
		})
	}
	return otelhttp.NewHandler(r, "http.server", cfg.OTel...)
}

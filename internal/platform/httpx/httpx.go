// Package httpx builds Proxier's single HTTP listener: the Echo app, its
// middleware chain and /healthz. Admin and public routes are added by the
// platform and the modules (docs/architecture.md#http-surfaces).
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// Health reports whether the process can serve: the database answers and
// the workers are alive. It must be quick and reveal nothing.
type Health func(ctx context.Context) error

// Options configure New.
type Options struct {
	Log *slog.Logger
	// TrustedProxies are the addresses whose X-Real-IP is believed. With
	// none, the client IP is always the socket address.
	TrustedProxies []netip.Prefix
	Health         Health
}

// New returns the Echo app with the shared middleware and /healthz.
func New(o Options) *echo.Echo {
	// Groups don't register their own 404 routes: web.Mount installs one
	// catch-all that runs the admin chain.
	e := echo.NewWithConfig(echo.Config{NoGroupAutoRegister404Routes: true})
	e.Logger = o.Log
	e.IPExtractor = IPExtractor(o.TrustedProxies)

	e.Pre(middleware.RemoveTrailingSlash())
	e.Use(middleware.RequestID())
	e.Use(RequestLogger(o.Log))
	// Inside the logger, so a panic still leaves its request line.
	e.Use(middleware.Recover())

	e.GET("/healthz", healthz(o.Health))
	return e
}

// IPExtractor takes the client IP from X-Real-IP only when the request comes
// from a trusted proxy (the SSH tunnel's end); otherwise it is the socket
// address. Nothing is trusted implicitly: not loopback, not private networks.
// (Echo's own Real-IP extractor checks the header's address, not the peer's.)
func IPExtractor(trusted []netip.Prefix) echo.IPExtractor {
	return func(r *http.Request) string {
		peer := socketAddr(r)
		if !peer.IsValid() {
			return ""
		}
		for _, p := range trusted {
			if p.Contains(peer) {
				h := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(r.Header.Get("X-Real-IP")), "["), "]")
				if ip, err := netip.ParseAddr(h); err == nil {
					return ip.Unmap().String()
				}
				break
			}
		}
		return peer.String()
	}
}

func socketAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	if ip, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return ip.Unmap()
	}
	return netip.Addr{}
}

func healthz(h Health) echo.HandlerFunc {
	return func(c *echo.Context) error {
		c.Response().Header().Set("Cache-Control", "no-store")
		if h != nil {
			ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
			defer cancel()
			if err := h(ctx); err != nil {
				c.Logger().Warn("health check failed", "error", err)
				return c.String(http.StatusServiceUnavailable, "unavailable\n")
			}
		}
		return c.String(http.StatusOK, "ok\n")
	}
}

// RequestLogger logs one line per request. /healthz is logged only when it
// fails.
func RequestLogger(log *slog.Logger) echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogLatency: true, LogRemoteIP: true, LogMethod: true, LogURIPath: true, LogStatus: true, LogRequestID: true,
		HandleError: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []any{"method", v.Method, "path", v.URIPath, "status", v.Status,
				"latency_ms", v.Latency.Milliseconds(), "ip", v.RemoteIP, "request_id", v.RequestID}
			if v.Error != nil {
				attrs = append(attrs, "error", v.Error.Error())
			}
			switch {
			case v.Status >= 500:
				log.Error("request", attrs...)
			case v.Status >= 400:
				log.Warn("request", attrs...)
			case v.URIPath != "/healthz":
				log.Info("request", attrs...)
			}
			return nil
		},
	})
}

// Run serves on addr until ctx is cancelled, then drains connections.
func Run(ctx context.Context, e *echo.Echo, addr string, listening func(net.Addr)) error {
	sc := echo.StartConfig{
		Address: addr, HideBanner: true, HidePort: true,
		GracefulTimeout:  15 * time.Second,
		ListenerAddrFunc: listening,
	}
	if err := sc.Start(ctx, e); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

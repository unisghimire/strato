package middleware

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Metrics holds the service's Prometheus instruments. Buckets are tuned for
// an API tier: 5ms floor for cache hits, 10s ceiling for large completions.
type Metrics struct {
	rpcDuration  *prometheus.HistogramVec
	rpcInFlight  prometheus.Gauge
	uploadBytes  prometheus.Counter
	downloadByte prometheus.Counter
}

// NewMetrics registers Strato's metrics on the given registerer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		rpcDuration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "strato",
			Name:      "rpc_duration_seconds",
			Help:      "RPC latency by method and status code.",
			Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"method", "code"}),
		rpcInFlight: factory.NewGauge(prometheus.GaugeOpts{
			Namespace: "strato",
			Name:      "rpc_in_flight",
			Help:      "RPCs currently being served.",
		}),
		uploadBytes: factory.NewCounter(prometheus.CounterOpts{
			Namespace: "strato",
			Name:      "upload_bytes_total",
			Help:      "Total bytes received through chunk uploads.",
		}),
		downloadByte: factory.NewCounter(prometheus.CounterOpts{
			Namespace: "strato",
			Name:      "download_bytes_total",
			Help:      "Total plaintext bytes streamed to clients.",
		}),
	}
}

// AddUploadBytes records received upload volume.
func (m *Metrics) AddUploadBytes(n int64) { m.uploadBytes.Add(float64(n)) }

// AddDownloadBytes records served download volume.
func (m *Metrics) AddDownloadBytes(n int64) { m.downloadByte.Add(float64(n)) }

// UnaryInterceptor instruments every RPC.
func (m *Metrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		m.rpcInFlight.Inc()
		defer m.rpcInFlight.Dec()

		start := time.Now()
		resp, err := handler(ctx, req)

		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		m.rpcDuration.WithLabelValues(info.FullMethod, code.String()).
			Observe(time.Since(start).Seconds())
		return resp, err
	}
}

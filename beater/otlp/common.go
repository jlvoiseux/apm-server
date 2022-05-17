package otlp

import (
	"github.com/elastic/apm-server/beater/request"
	"github.com/elastic/beats/v7/libbeat/monitoring"
)

var (
	monitoringKeys = append(request.DefaultResultIDs,
		request.IDResponseErrorsRateLimit,
		request.IDResponseErrorsTimeout,
		request.IDResponseErrorsUnauthorized,
	)
)

const (
	metricsFullMethod = "/opentelemetry.proto.collector.metrics.v1.MetricsService/Export"
	tracesFullMethod  = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
	logsFullMethod    = "/opentelemetry.proto.collector.logs.v1.LogsService/Export"
)

func collectMetricsMonitoring(mode monitoring.Mode, V monitoring.Visitor) {
	V.OnRegistryStart()
	defer V.OnRegistryFinished()

	currentMonitoredConsumerMu.RLock()
	c := currentMonitoredConsumer
	currentMonitoredConsumerMu.RUnlock()
	if c == nil {
		return
	}

	stats := c.Stats()
	monitoring.ReportInt(V, "unsupported_dropped", stats.UnsupportedMetricsDropped)
}

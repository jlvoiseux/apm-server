package otlp

import (
	"context"
	"github.com/elastic/apm-server/model"
	"github.com/elastic/apm-server/processor/otel"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
)

func RegisterHTTPServices(router *mux.Router, processor model.BatchProcessor, tracesPath string, metricsPath string, logsPath string) error {

	consumer := &otel.Consumer{Processor: processor}
	setCurrentMonitoredConsumer(consumer)

	if err := otlpreceiver.RegisterHTTPTraceReceiver(context.Background(), consumer, router, tracesPath); err != nil {
		return errors.Wrap(err, "failed to register OTLP trace receiver")
	}
	if err := otlpreceiver.RegisterHTTPMetricsReceiver(context.Background(), consumer, router, metricsPath); err != nil {
		return errors.Wrap(err, "failed to register OTLP metrics receiver")
	}
	if err := otlpreceiver.RegisterHTTPLogsReceiver(context.Background(), consumer, router, logsPath); err != nil {
		return errors.Wrap(err, "failed to register OTLP logs receiver")
	}
	return nil
}

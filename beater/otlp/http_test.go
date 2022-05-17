package otlp_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/elastic/apm-server/agentcfg"
	"github.com/elastic/apm-server/beater/api"
	"github.com/elastic/apm-server/beater/auth"
	"github.com/elastic/apm-server/beater/config"
	"github.com/elastic/apm-server/beater/otlp"
	"github.com/elastic/apm-server/beater/ratelimit"
	"github.com/elastic/apm-server/model"
	"github.com/elastic/beats/v7/libbeat/beat"
	"github.com/elastic/beats/v7/libbeat/monitoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/model/otlpgrpc"
	"go.opentelemetry.io/collector/model/pdata"
	"net"
	"net/http"
	"testing"
)

func TestConsumeTracesHTTP(t *testing.T) {
	var batches []model.Batch
	var reportError error
	var batchProcessor model.ProcessBatchFunc = func(ctx context.Context, batch *model.Batch) error {
		batches = append(batches, *batch)
		fmt.Println("BATCH ENTERED")
		return reportError
	}

	addr := newHTTPServer(t, batchProcessor)

	// Send a minimal trace to verify that everything is connected properly.
	//
	// We intentionally do not check the published event contents; those are
	// tested in processor/otel.
	traces := pdata.NewTraces()
	span := traces.ResourceSpans().AppendEmpty().InstrumentationLibrarySpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("operation_name")

	tracesRequest := otlpgrpc.NewTracesRequest()
	tracesRequest.SetTraces(traces)
	request, err := tracesRequest.Marshal()
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/traces", addr), bytes.NewReader(request))
	req.Header.Set("Content-Type", "application/x-protobuf")
	client := http.Client{}
	_, err = client.Do(req)
	assert.NoError(t, err)
	require.Len(t, batches, 1)
	assert.Len(t, batches[0], 1)

	actual := map[string]interface{}{}
	monitoring.GetRegistry("apm-server.otlp.http.traces").Do(monitoring.Full, func(key string, value interface{}) {
		actual[key] = value
	})
	assert.Equal(t, map[string]interface{}{
		"request.count":                int64(1),
		"response.count":               int64(1),
		"response.errors.count":        int64(0),
		"response.valid.count":         int64(1),
		"response.errors.ratelimit":    int64(0),
		"response.errors.timeout":      int64(0),
		"response.errors.unauthorized": int64(0),
	}, actual)
}

func newHTTPServer(t *testing.T, batchProcessor model.BatchProcessor) string {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	receivers, err := otlp.NewHTTPReceivers(batchProcessor)
	require.NoError(t, err)
	cfg := &config.Config{}
	auth, _ := auth.NewAuthenticator(cfg.AgentAuth)
	ratelimitStore, _ := ratelimit.NewStore(1000, 1000, 1000)
	router, err := api.NewMux(beat.Info{Version: "1.2.3"}, cfg, batchProcessor, auth, agentcfg.NewFetcher(cfg), ratelimitStore, nil, receivers, false, func() bool { return true })
	require.NoError(t, err)
	srv := http.Server{Handler: router}
	go srv.Serve(lis)
	return lis.Addr().String()
}

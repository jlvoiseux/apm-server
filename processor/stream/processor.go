// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package stream

import (
	"bytes"
	"context"
	"io"
	"sync"

	"go.elastic.co/apm/v2"

	"github.com/pkg/errors"

	"github.com/elastic/apm-server/beater/config"
	"github.com/elastic/apm-server/decoder"
	"github.com/elastic/apm-server/model"
	"github.com/elastic/apm-server/model/modeldecoder"
	"github.com/elastic/apm-server/model/modeldecoder/rumv3"
	v2 "github.com/elastic/apm-server/model/modeldecoder/v2"
)

var (
	errUnrecognizedObject = errors.New("did not recognize object type")
)

const (
	errorEventType            = "error"
	metricsetEventType        = "metricset"
	spanEventType             = "span"
	transactionEventType      = "transaction"
	rumv3ErrorEventType       = "e"
	rumv3TransactionEventType = "x"
)

type decodeMetadataFunc func(decoder.Decoder, *model.APMEvent) error

// Processor decodes a streams and is safe for concurrent use. The processor
// accepts a channel that is used as a semaphore to control the maximum
// concurrent number of stream decode operations that can happen at any time.
// The buffered channel is meant to be shared between all the processors so
// the concurrency limit is shared between all the intake endpoints.
type Processor struct {
	streamReaderPool sync.Pool
	decodeMetadata   decodeMetadataFunc
	sem              chan struct{}
	MaxEventSize     int
}

func BackendProcessor(cfg *config.Config, sem chan struct{}) *Processor {
	return &Processor{
		MaxEventSize:   cfg.MaxEventSize,
		decodeMetadata: v2.DecodeNestedMetadata,
		sem:            sem,
	}
}

func RUMV2Processor(cfg *config.Config, sem chan struct{}) *Processor {
	return &Processor{
		MaxEventSize:   cfg.MaxEventSize,
		decodeMetadata: v2.DecodeNestedMetadata,
		sem:            sem,
	}
}

func RUMV3Processor(cfg *config.Config, sem chan struct{}) *Processor {
	return &Processor{
		MaxEventSize:   cfg.MaxEventSize,
		decodeMetadata: rumv3.DecodeNestedMetadata,
		sem:            sem,
	}
}

func (p *Processor) readMetadata(reader *streamReader, out *model.APMEvent) error {
	if err := p.decodeMetadata(reader, out); err != nil {
		err = reader.wrapError(err)
		if err == io.EOF {
			return &InvalidInputError{
				Message:  "EOF while reading metadata",
				Document: string(reader.LatestLine()),
			}
		}
		if _, ok := err.(*InvalidInputError); ok {
			return err
		}
		return &InvalidInputError{
			Message:  err.Error(),
			Document: string(reader.LatestLine()),
		}
	}
	return nil
}

// identifyEventType takes a reader and reads ahead the first key of the
// underlying json input. This method makes some assumptions met by the
// input format:
// - the input is in JSON format
// - every valid ndjson line only has one root key
// - the bytes that we must match on are ASCII
func (p *Processor) identifyEventType(body []byte) []byte {
	// find event type, trim spaces and account for single and double quotes
	var quote byte
	var key []byte
	for i, r := range body {
		if r == '"' || r == '\'' {
			quote = r
			key = body[i+1:]
			break
		}
	}
	end := bytes.IndexByte(key, quote)
	if end == -1 {
		return nil
	}
	return key[:end]
}

// readBatch reads up to `batchSize` events from the ndjson stream into
// batch, returning the number of events read and any error encountered.
// Callers should always process the n > 0 events returned before considering
// the error err.
func (p *Processor) readBatch(
	ctx context.Context,
	baseEvent model.APMEvent,
	batchSize int,
	batch *model.Batch,
	reader *streamReader,
	result *Result,
) (int, error) {

	// input events are decoded and appended to the batch
	origLen := len(*batch)
	for i := 0; i < batchSize && !reader.IsEOF(); i++ {
		body, err := reader.ReadAhead()
		if err != nil && err != io.EOF {
			err := reader.wrapError(err)
			var invalidInput *InvalidInputError
			if errors.As(err, &invalidInput) {
				result.LimitedAdd(err)
				continue
			}
			// return early, we assume we can only recover from a input error types
			return len(*batch) - origLen, err
		}
		if len(body) == 0 {
			// required for backwards compatibility - sending empty lines was permitted in previous versions
			continue
		}
		// We copy the event for each iteration of the batch, as to avoid
		// shallow copies of Labels and NumericLabels.
		input := modeldecoder.Input{Base: copyEvent(baseEvent)}
		switch eventType := p.identifyEventType(body); string(eventType) {
		case errorEventType:
			err = v2.DecodeNestedError(reader, &input, batch)
		case metricsetEventType:
			err = v2.DecodeNestedMetricset(reader, &input, batch)
		case spanEventType:
			err = v2.DecodeNestedSpan(reader, &input, batch)
		case transactionEventType:
			err = v2.DecodeNestedTransaction(reader, &input, batch)
		case rumv3ErrorEventType:
			err = rumv3.DecodeNestedError(reader, &input, batch)
		case rumv3TransactionEventType:
			err = rumv3.DecodeNestedTransaction(reader, &input, batch)
		default:
			err = errors.Wrap(errUnrecognizedObject, string(eventType))
		}
		if err != nil && err != io.EOF {
			result.LimitedAdd(&InvalidInputError{
				Message:  err.Error(),
				Document: string(reader.LatestLine()),
			})
		}
	}
	if reader.IsEOF() {
		return len(*batch) - origLen, io.EOF
	}
	return len(*batch) - origLen, nil
}

// HandleStream processes a stream of events in batches of batchSize at a time,
// updating result as events are accepted, or per-event errors occur.
//
// HandleStream will return an error when a terminal stream-level error occurs,
// such as the rate limit being exceeded, or due to authorization errors. In
// this case the result will only cover the subset of events accepted.
//
// Callers must not access result concurrently with HandleStream.
func (p *Processor) HandleStream(
	ctx context.Context,
	baseEvent model.APMEvent,
	reader io.Reader,
	batchSize int,
	processor model.BatchProcessor,
	result *Result,
) error {
	// Since processor.ProcessBatch can block for some time until all the batch
	// is added to the modelindexer's cache and there isn't a fail-fast rejection,
	// we want to cap how many in-flight requests are read at any time.
	// Defaults to 200 (N), only allowing N requests to read and cache Y events
	// (determined by batchSize) from the batch, effectively limitting the decoding
	// concurrency to N batches at any time, which should be a good ceiling. The
	// ceiling also reduces the contention on the modelindexer.activeMu.
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	sr := p.getStreamReader(reader)
	defer func() {
		sr.release()
		<-p.sem
	}()

	// first item is the metadata object
	if err := p.readMetadata(sr, &baseEvent); err != nil {
		// no point in continuing if we couldn't read the metadata
		return err
	}

	sp, ctx := apm.StartSpan(ctx, "Stream", "Reporter")
	defer sp.End()

	for {
		var batch model.Batch
		n, readErr := p.readBatch(ctx, baseEvent, batchSize, &batch, sr, result)
		if n > 0 {
			// NOTE(axw) ProcessBatch takes ownership of batch, which means we cannot reuse
			// the slice memory. We should investigate alternative interfaces between the
			// processor and publisher which would enable better memory reuse, e.g. by using
			// a sync.Pool for creating batches, and having the publisher (terminal processor)
			// release batches back into the pool.
			if err := processor.ProcessBatch(ctx, &batch); err != nil {
				return err
			}
			result.AddAccepted(len(batch))
		}
		if readErr == io.EOF {
			break
		} else if readErr != nil {
			return readErr
		}
	}
	return nil
}

// getStreamReader returns a streamReader that reads ND-JSON lines from r.
func (p *Processor) getStreamReader(r io.Reader) *streamReader {
	if sr, ok := p.streamReaderPool.Get().(*streamReader); ok {
		sr.Reset(r)
		return sr
	}
	return &streamReader{
		processor:           p,
		NDJSONStreamDecoder: decoder.NewNDJSONStreamDecoder(r, p.MaxEventSize),
	}
}

// streamReader wraps NDJSONStreamReader, converting errors to stream errors.
type streamReader struct {
	processor *Processor
	*decoder.NDJSONStreamDecoder
}

// release releases the streamReader, adding it to its Processor's sync.Pool.
// The streamReader must not be used after release returns.
func (sr *streamReader) release() {
	sr.Reset(nil)
	sr.processor.streamReaderPool.Put(sr)
}

func (sr *streamReader) wrapError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(decoder.JSONDecodeError); ok {
		return &InvalidInputError{
			Message:  err.Error(),
			Document: string(sr.LatestLine()),
		}
	}

	var e = err
	if err, ok := err.(modeldecoder.DecoderError); ok {
		e = err.Unwrap()
	}
	if errors.Is(e, decoder.ErrLineTooLong) {
		return &InvalidInputError{
			TooLarge: true,
			Message:  "event exceeded the permitted size.",
			Document: string(sr.LatestLine()),
		}
	}
	return err
}

// copyEvent returns a shallow copy of the APMEvent with a deep copy of the
// labels and numeric labels.
func copyEvent(e model.APMEvent) model.APMEvent {
	var out = e
	if out.Labels != nil {
		out.Labels = out.Labels.Clone()
	}
	if out.NumericLabels != nil {
		out.NumericLabels = out.NumericLabels.Clone()
	}
	return out
}

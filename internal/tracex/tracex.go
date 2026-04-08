package tracex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const (
	requestIDKey     contextKey = "request_id"
	forcedTraceIDKey contextKey = "forced_trace_id"
	forcedSpanIDKey  contextKey = "forced_span_id"
)

func NewID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(raw[:])
}

func WithIDs(ctx context.Context, traceID, requestID, spanID string) context.Context {
	if strings.TrimSpace(requestID) != "" {
		ctx = context.WithValue(ctx, requestIDKey, requestID)
	}
	if strings.TrimSpace(traceID) != "" && !trace.SpanContextFromContext(ctx).HasTraceID() {
		if parsed, ok := parseTraceID(traceID); ok {
			spanCtx := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID: parsed,
				Remote:  true,
			})
			ctx = trace.ContextWithRemoteSpanContext(ctx, spanCtx)
			ctx = context.WithValue(ctx, forcedTraceIDKey, traceID)
		}
	}
	if strings.TrimSpace(spanID) != "" {
		ctx = context.WithValue(ctx, forcedSpanIDKey, spanID)
	}
	return ctx
}

func EnsureTrace(ctx context.Context) context.Context {
	if TraceID(ctx) != "" && RequestID(ctx) != "" {
		return ctx
	}
	return WithIDs(ctx, firstNonEmpty(TraceID(ctx), NewID()), firstNonEmpty(RequestID(ctx), NewID()), SpanID(ctx))
}

func TraceID(ctx context.Context) string {
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.HasTraceID() {
		return spanCtx.TraceID().String()
	}
	if traceID, _ := ctx.Value(forcedTraceIDKey).(string); traceID != "" {
		return traceID
	}
	return ""
}

func RequestID(ctx context.Context) string {
	if requestID, _ := ctx.Value(requestIDKey).(string); requestID != "" {
		return requestID
	}
	return ""
}

func SpanID(ctx context.Context) string {
	if spanCtx := trace.SpanContextFromContext(ctx); spanCtx.HasSpanID() {
		return spanCtx.SpanID().String()
	}
	if spanID, _ := ctx.Value(forcedSpanIDKey).(string); spanID != "" {
		return spanID
	}
	return ""
}

func Logger(ctx context.Context) *slog.Logger {
	fields := make([]any, 0, 6)
	if traceID := TraceID(ctx); traceID != "" {
		fields = append(fields, "trace_id", traceID)
	}
	if requestID := RequestID(ctx); requestID != "" {
		fields = append(fields, "request_id", requestID)
	}
	if spanID := SpanID(ctx); spanID != "" {
		fields = append(fields, "span_id", spanID)
	}
	return slog.Default().With(fields...)
}

func StartSpan(ctx context.Context, name string, kv ...any) (context.Context, func(error)) {
	ctx = EnsureTrace(ctx)
	attrs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		attrs = append(attrs, attribute.String(key, fmt.Sprint(kv[i+1])))
	}
	ctx, span := otel.Tracer("nexus").Start(ctx, name, trace.WithAttributes(attrs...))
	log := Logger(ctx)
	log.Info("span.start", "span_name", name)
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			log.Error("span.end", "span_name", name, "error", err.Error())
		} else {
			span.SetStatus(codes.Ok, "")
			log.Info("span.end", "span_name", name)
		}
		span.End()
	}
}

func Setup(ctx context.Context, serviceName, endpoint string, sampleRatio float64) (func(context.Context) error, error) {
	if strings.TrimSpace(endpoint) == "" {
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, err
	}
	if sampleRatio < 0 {
		sampleRatio = 0
	}
	if sampleRatio > 1 {
		sampleRatio = 1
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(sampleRatio)),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
			attribute.String("service.instance.id", NewID()),
		)),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown, nil
}

func parseTraceID(in string) (trace.TraceID, bool) {
	var id trace.TraceID
	raw, err := hex.DecodeString(strings.TrimSpace(in))
	if err != nil || len(raw) != len(id) {
		return id, false
	}
	copy(id[:], raw)
	return id, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

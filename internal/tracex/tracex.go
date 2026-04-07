package tracex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"
)

type contextKey string

const (
	traceIDKey   contextKey = "trace_id"
	requestIDKey contextKey = "request_id"
	spanIDKey    contextKey = "span_id"
)

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		now := time.Now().UTC().UnixNano()
		return hex.EncodeToString([]byte(time.Unix(0, now).UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func WithIDs(ctx context.Context, traceID, requestID, spanID string) context.Context {
	if traceID != "" {
		ctx = context.WithValue(ctx, traceIDKey, traceID)
	}
	if requestID != "" {
		ctx = context.WithValue(ctx, requestIDKey, requestID)
	}
	if spanID != "" {
		ctx = context.WithValue(ctx, spanIDKey, spanID)
	}
	return ctx
}

func EnsureTrace(ctx context.Context) context.Context {
	traceID := TraceID(ctx)
	if traceID == "" {
		traceID = NewID()
	}
	requestID := RequestID(ctx)
	if requestID == "" {
		requestID = NewID()
	}
	return WithIDs(ctx, traceID, requestID, SpanID(ctx))
}

func TraceID(ctx context.Context) string {
	if value, ok := ctx.Value(traceIDKey).(string); ok {
		return value
	}
	return ""
}

func RequestID(ctx context.Context) string {
	if value, ok := ctx.Value(requestIDKey).(string); ok {
		return value
	}
	return ""
}

func SpanID(ctx context.Context) string {
	if value, ok := ctx.Value(spanIDKey).(string); ok {
		return value
	}
	return ""
}

func Logger(ctx context.Context, attrs ...any) *slog.Logger {
	logger := slog.Default()
	if traceID := TraceID(ctx); traceID != "" {
		logger = logger.With("trace_id", traceID)
	}
	if requestID := RequestID(ctx); requestID != "" {
		logger = logger.With("request_id", requestID)
	}
	if spanID := SpanID(ctx); spanID != "" {
		logger = logger.With("span_id", spanID)
	}
	if len(attrs) > 0 {
		logger = logger.With(attrs...)
	}
	return logger
}

func StartSpan(ctx context.Context, name string, attrs ...any) (context.Context, func(error, ...any)) {
	ctx = EnsureTrace(ctx)
	spanID := NewID()
	ctx = WithIDs(ctx, TraceID(ctx), RequestID(ctx), spanID)
	start := time.Now()
	Logger(ctx, "span", name).Info("span.start", attrs...)
	return ctx, func(err error, endAttrs ...any) {
		items := []any{"span", name, "duration_ms", time.Since(start).Milliseconds()}
		items = append(items, endAttrs...)
		if err != nil {
			items = append(items, "error", err.Error())
			Logger(ctx).Error("span.end", items...)
			return
		}
		Logger(ctx).Info("span.end", items...)
	}
}

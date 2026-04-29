package tracex

import (
	"fmt"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func Middleware(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-ID")
		if traceID == "" {
			traceID = NewID()
		}
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = NewID()
		}
		ctx, end := StartSpan(WithIDs(r.Context(), traceID, requestID, ""), "http.request",
			"service", service,
			"method", r.Method,
			"path", r.URL.Path,
		)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		recorder.Header().Set("X-Trace-ID", traceID)
		recorder.Header().Set("X-Request-ID", requestID)
		var spanErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				recorder.status = http.StatusInternalServerError
				spanErr = fmt.Errorf("panic: %v", recovered)
				Logger(ctx).Error("http.panic",
					"service", service,
					"method", r.Method,
					"path", r.URL.Path,
					"error", spanErr.Error(),
				)
				end(spanErr)
				panic(recovered)
			}
			if recorder.status >= http.StatusInternalServerError {
				spanErr = fmt.Errorf("http %d", recorder.status)
			}
			end(spanErr)
		}()
		next.ServeHTTP(recorder, r.WithContext(ctx))
		Logger(ctx).Info("http.access",
			"service", service,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"remote_addr", r.RemoteAddr,
			"content_length", r.ContentLength,
			"at", time.Now().UTC().Format(time.RFC3339Nano),
		)
	})
}

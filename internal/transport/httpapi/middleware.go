package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func correlationID(r *http.Request) string {
	if value := r.Header.Get("X-Correlation-ID"); value != "" {
		return value
	}
	return r.Header.Get("X-Request-ID")
}

func (h *Handler) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		r.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

var _ http.ResponseWriter = (*statusWriter)(nil)

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (h *Handler) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r)
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern != "" {
			span := trace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + routePattern)
			span.SetAttributes(attribute.String("http.route", routePattern))
		}
		h.log.InfoContext(
			r.Context(),
			"http request",
			"requestId", r.Header.Get("X-Request-ID"),
			"method", r.Method,
			"path", r.URL.Path,
			"route", routePattern,
			"status", status,
			"durationMs", time.Since(started).Milliseconds(),
		)
	})
}

func (h *Handler) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				h.log.ErrorContext(
					r.Context(),
					"http panic recovered",
					"error", fmt.Sprint(recovered),
					"requestId", r.Header.Get("X-Request-ID"),
				)
				writeProblem(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

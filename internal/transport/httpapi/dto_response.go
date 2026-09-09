package httpapi

import "time"

// ProblemResponse is the standard error DTO returned by the HTTP API.
// Error.Code is stable and intended for programmatic handling; Error.Message
// is a human-readable diagnostic and must not be parsed by clients.
type ProblemResponse struct {
	Error ProblemDetail `json:"error"`
}

type ProblemDetail struct {
	Code    string `json:"code" example:"INVALID_REQUEST"`
	Message string `json:"message" example:"request is invalid"`
}

// HealthResponse is the DTO returned by public health endpoints.
type HealthResponse struct {
	Status    string                 `json:"status" example:"ready"`
	CheckedAt time.Time              `json:"checkedAt" format:"date-time"`
	Checks    map[string]HealthCheck `json:"checks"`
}

// HealthCheck describes the state of one runtime dependency.
type HealthCheck struct {
	Status    string `json:"status" example:"up"`
	LatencyMS int64  `json:"latencyMs" example:"4"`
	Message   string `json:"message,omitempty" example:"dependency unavailable"`
}

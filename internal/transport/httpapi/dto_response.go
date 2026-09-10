package httpapi

import "time"

type ProblemResponse struct {
	Error ProblemDetail `json:"error"`
}

type ProblemDetail struct {
	Code    string `json:"code" example:"INVALID_REQUEST"`
	Message string `json:"message" example:"request is invalid"`
}

type HealthResponse struct {
	Status    string                 `json:"status" example:"ready"`
	CheckedAt time.Time              `json:"checkedAt" format:"date-time"`
	Checks    map[string]HealthCheck `json:"checks"`
}

type HealthCheck struct {
	Status    string `json:"status" example:"up"`
	LatencyMS int64  `json:"latencyMs" example:"4"`
	Message   string `json:"message,omitempty" example:"dependency unavailable"`
}

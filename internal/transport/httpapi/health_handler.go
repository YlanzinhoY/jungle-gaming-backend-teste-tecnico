package httpapi

import (
	"context"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

// live godoc
//
//	@Summary	Process liveness
//	@Tags		Health
//	@Produce	json
//	@Success	200	{object}	HealthResponse
//	@Router		/health/live [get]
func (h *Handler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:    "live",
		CheckedAt: time.Now().UTC(),
		Checks: map[string]HealthCheck{
			"process": {Status: "up"},
		},
	})
}

// ready godoc
//
//	@Summary		Detailed dependency readiness
//	@Description	Checks PostgreSQL, the SQS input and event queues, and OIDC discovery in parallel.
//	@Tags			Health
//	@Produce		json
//	@Success		200	{object}	HealthResponse
//	@Failure		503	{object}	HealthResponse
//	@Router			/health/ready [get]
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	checks := []readinessCheck{
		{name: "database", check: h.store.Ping},
		{name: "sqsInputQueue", check: h.sqs.CheckInputQueue},
		{name: "sqsEventQueue", check: h.sqs.CheckEventQueue},
		{name: "identityProvider", check: h.identity.Check},
	}

	results := make(chan readinessResult, len(checks))
	for _, check := range checks {
		go check.run(r.Context(), results)
	}

	response := HealthResponse{
		Status:    "ready",
		CheckedAt: time.Now().UTC(),
		Checks:    make(map[string]HealthCheck, len(checks)),
	}
	for range checks {
		result := <-results
		response.Checks[result.name] = result.healthCheck
		if result.err != nil {
			response.Status = "not_ready"
			h.log.WarnContext(r.Context(), "readiness check failed", "dependency", result.name, "error", result.err)
		}
	}

	status := http.StatusOK
	if response.Status != "ready" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, response)
}

type readinessCheck struct {
	name  string
	check func(context.Context) error
}

type readinessResult struct {
	name        string
	healthCheck HealthCheck
	err         error
}

func (c readinessCheck) run(parent context.Context, results chan<- readinessResult) {
	ctx, cancel := context.WithTimeout(parent, readinessTimeout)
	defer cancel()

	startedAt := time.Now()
	err := c.check(ctx)
	result := readinessResult{
		name: c.name,
		healthCheck: HealthCheck{
			Status:    "up",
			LatencyMS: time.Since(startedAt).Milliseconds(),
		},
	}
	if err != nil {
		result.healthCheck.Status = "down"
		result.healthCheck.Message = "dependency unavailable"
		result.err = err
	}
	results <- result
}

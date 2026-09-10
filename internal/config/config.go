package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-playground/validator/v10"
)

type Config struct {
	HTTP          HTTP          `validate:"required"`
	Database      Database      `validate:"required"`
	OIDC          OIDC          `validate:"required"`
	AWS           AWS           `validate:"required"`
	Workers       Workers       `validate:"required"`
	Observability Observability `validate:"required"`
}

type HTTP struct {
	Address         string        `validate:"required"`
	ReadTimeout     time.Duration `validate:"gt=0"`
	WriteTimeout    time.Duration `validate:"gt=0"`
	ShutdownTimeout time.Duration `validate:"gt=0"`
}

type Database struct {
	URL            string `validate:"required"`
	MaxConnections int32  `validate:"gte=2"`
}

type OIDC struct {
	IssuerURL      string        `validate:"required,url"`
	DiscoveryURL   string        `validate:"omitempty,url"`
	Audience       string        `validate:"required"`
	ProviderClaim  string        `validate:"required"`
	InternalRole   string        `validate:"required"`
	StartupTimeout time.Duration `validate:"gt=0"`
}

type AWS struct {
	Region                           string `validate:"required"`
	Endpoint                         string `validate:"omitempty,http_url"`
	AccessKey                        string
	SecretKey                        string
	InputQueue                       string `validate:"required,endswith=.fifo"`
	InputDLQ                         string `validate:"required,endswith=.fifo"`
	EventQueue                       string `validate:"required,endswith=.fifo"`
	EventDLQ                         string `validate:"required,endswith=.fifo"`
	CreateQueues                     bool
	DisableMessageChecksumValidation bool
	MaxReceives                      int   `validate:"gte=1"`
	Visibility                       int32 `validate:"gte=1,lte=43200"`
}

type Workers struct {
	Enabled                 bool
	ConsumerConcurrency     int           `validate:"gte=1"`
	OutboxBatchSize         int           `validate:"gte=1,lte=500"`
	ReferenceBatchSize      int           `validate:"gte=1,lte=500"`
	ReferenceMaxAttempts    int           `validate:"gte=1"`
	PollInterval            time.Duration `validate:"gt=0"`
	ReferenceBaseBackoff    time.Duration `validate:"gt=0"`
	ReferenceMaxBackoff     time.Duration `validate:"gtefield=ReferenceBaseBackoff"`
	OutboxLease             time.Duration `validate:"gt=0"`
	ConsumerRetryBase       time.Duration `validate:"gt=0"`
	ConsumerRetryMax        time.Duration `validate:"gtefield=ConsumerRetryBase"`
	ConsumerPostCommitDelay time.Duration `validate:"gte=0"`
}

type Observability struct {
	TracingEnabled bool
	ServiceName    string `validate:"required"`
	ServiceVersion string `validate:"required"`
	Environment    string `validate:"required"`
	OTLPEndpoint   string `validate:"required,hostname_port"`
	OTLPInsecure   bool
	TraceSample    float64 `validate:"gte=0,lte=1"`
}

func Load() (Config, error) {
	parser := envParser{}
	cfg := Config{
		HTTP: HTTP{
			Address:         env("HTTP_ADDRESS", ":8080"),
			ReadTimeout:     parser.duration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:    parser.duration("HTTP_WRITE_TIMEOUT", 20*time.Second),
			ShutdownTimeout: parser.duration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Database: Database{
			URL:            env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/wagering?sslmode=disable"),
			MaxConnections: int32(parser.integer("DATABASE_MAX_CONNECTIONS", 20)),
		},
		OIDC: OIDC{
			IssuerURL:      env("OIDC_ISSUER_URL", "http://localhost:8081/realms/gaming"),
			DiscoveryURL:   env("OIDC_DISCOVERY_URL", ""),
			Audience:       env("OIDC_AUDIENCE", "wagering-api"),
			ProviderClaim:  env("OIDC_PROVIDER_CLAIM", "provider_id"),
			InternalRole:   env("OIDC_INTERNAL_ROLE", "wallet-service"),
			StartupTimeout: parser.duration("OIDC_STARTUP_TIMEOUT", 10*time.Second),
		},
		AWS: AWS{
			Region:                           env("AWS_REGION", "us-east-1"),
			Endpoint:                         env("AWS_ENDPOINT_URL", "http://localhost:4566"),
			AccessKey:                        env("AWS_ACCESS_KEY_ID", ""),
			SecretKey:                        env("AWS_SECRET_ACCESS_KEY", ""),
			InputQueue:                       env("SQS_INPUT_QUEUE", "wager-transactions.fifo"),
			InputDLQ:                         env("SQS_INPUT_DLQ", "wager-transactions-dlq.fifo"),
			EventQueue:                       env("SQS_EVENT_QUEUE", "wager-events.fifo"),
			EventDLQ:                         env("SQS_EVENT_DLQ", "wager-events-dlq.fifo"),
			CreateQueues:                     parser.boolean("SQS_CREATE_QUEUES", true),
			DisableMessageChecksumValidation: parser.boolean("SQS_DISABLE_MESSAGE_CHECKSUM_VALIDATION", false),
			MaxReceives:                      parser.integer("SQS_MAX_RECEIVES", 5),
			Visibility:                       int32(parser.integer("SQS_VISIBILITY_TIMEOUT_SECONDS", 30)),
		},
		Workers: Workers{
			Enabled:                 parser.boolean("WORKERS_ENABLED", true),
			ConsumerConcurrency:     parser.integer("SQS_CONSUMER_CONCURRENCY", 4),
			OutboxBatchSize:         parser.integer("OUTBOX_BATCH_SIZE", 50),
			ReferenceBatchSize:      parser.integer("REFERENCE_BATCH_SIZE", 50),
			ReferenceMaxAttempts:    parser.integer("REFERENCE_MAX_ATTEMPTS", 10),
			PollInterval:            parser.duration("WORKER_POLL_INTERVAL", time.Second),
			ReferenceBaseBackoff:    parser.duration("REFERENCE_BASE_BACKOFF", 2*time.Second),
			ReferenceMaxBackoff:     parser.duration("REFERENCE_MAX_BACKOFF", 5*time.Minute),
			OutboxLease:             parser.duration("OUTBOX_LEASE", 30*time.Second),
			ConsumerRetryBase:       parser.duration("SQS_RETRY_BASE_BACKOFF", 2*time.Second),
			ConsumerRetryMax:        parser.duration("SQS_RETRY_MAX_BACKOFF", 2*time.Minute),
			ConsumerPostCommitDelay: parser.duration("SQS_CONSUMER_POST_COMMIT_DELAY", 0),
		},
		Observability: Observability{
			TracingEnabled: parser.boolean("OTEL_TRACING_ENABLED", true),
			ServiceName:    env("OTEL_SERVICE_NAME", "jungle-gaming-api"),
			ServiceVersion: env("OTEL_SERVICE_VERSION", "local"),
			Environment:    env("OTEL_DEPLOYMENT_ENVIRONMENT", "local"),
			OTLPEndpoint:   env("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
			OTLPInsecure:   parser.boolean("OTEL_EXPORTER_OTLP_INSECURE", true),
			TraceSample:    parser.decimal("OTEL_TRACES_SAMPLE_RATIO", 1),
		},
	}
	if err := errors.Join(parser.parseErrors...); err != nil {
		return Config{}, fmt.Errorf("invalid environment configuration: %w", err)
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	if (c.AWS.AccessKey == "") != (c.AWS.SecretKey == "") {
		return errors.New("invalid configuration: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together")
	}
	validate := validator.New(validator.WithRequiredStructEnabled())
	if err := validate.Struct(c); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type envParser struct{ parseErrors []error }

func (p *envParser) integer(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		p.parseErrors = append(p.parseErrors, fmt.Errorf("%s=%q must be an integer", name, value))
		return fallback
	}
	return parsed
}

func (p *envParser) boolean(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		p.parseErrors = append(p.parseErrors, fmt.Errorf("%s=%q must be a boolean", name, value))
		return fallback
	}
	return parsed
}

func (p *envParser) duration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		p.parseErrors = append(p.parseErrors, fmt.Errorf("%s=%q must be a Go duration", name, value))
		return fallback
	}
	return parsed
}

func (p *envParser) decimal(name string, fallback float64) float64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		p.parseErrors = append(p.parseErrors, fmt.Errorf("%s=%q must be a decimal number", name, value))
		return fallback
	}
	return parsed
}

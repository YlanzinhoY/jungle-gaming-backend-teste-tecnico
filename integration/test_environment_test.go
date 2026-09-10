//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	postgresImage  = "postgres:17.11-alpine"
	ministackImage = "ministackorg/ministack:1.5.0"
	keycloakImage  = "quay.io/keycloak/keycloak:26.7.3"

	postgresAlias  = "postgres"
	ministackAlias = "ministack"
	keycloakAlias  = "keycloak"
	internalIssuer = "http://keycloak:8080/realms/gaming"
)

type sqsCredential struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

type e2eEnvironment struct {
	root                string
	network             testcontainers.Network
	networkName         string
	containers          []testcontainers.Container
	apis                []testcontainers.Container
	apiImage            string
	imageClient         *testcontainers.DockerProvider
	emptyDomainDatabase bool

	databaseURL    string
	keycloakURL    string
	sqsEndpoint    string
	sqsCredentials map[string]sqsCredential
	apiURLs        []string
}

var testEnvironment *e2eEnvironment

func TestMain(m *testing.M) {
	// A cold API image build can take several minutes on a constrained Docker
	// host. Keep the E2E environment isolated while giving startup a realistic
	// bounded deadline. Set E2E_NO_CACHE=1 when cache bypass is specifically
	// required; normal test runs still rebuild layers invalidated by source changes.
	startContext, cancelStart := context.WithTimeout(context.Background(), 12*time.Minute)
	environment, err := startE2EEnvironment(startContext)
	cancelStart()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Testcontainers E2E environment:", err)
		os.Exit(1)
	}
	testEnvironment = environment
	configureE2EGlobals(environment)

	code := m.Run()
	stopContext, cancelStop := context.WithTimeout(context.Background(), 2*time.Minute)
	environment.close(stopContext)
	cancelStop()
	os.Exit(code)
}

func startE2EEnvironment(ctx context.Context) (*e2eEnvironment, error) {
	root, err := filepath.Abs("..")
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	environment := &e2eEnvironment{root: root}

	networkName := "jungle-gaming-e2e-" + fmt.Sprint(time.Now().UnixNano())
	network, err := testcontainers.GenericNetwork(ctx, testcontainers.GenericNetworkRequest{
		NetworkRequest: testcontainers.NetworkRequest{
			Name:     networkName,
			Driver:   "bridge",
			Internal: false,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create E2E network: %w", err)
	}
	environment.network = network
	environment.networkName = networkName

	if err := environment.startPostgres(ctx); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := applyMigrations(ctx, environment.root, environment.databaseURL); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := environment.assertEmptyDomainDatabase(ctx); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := environment.startMiniStack(ctx); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := environment.startKeycloak(ctx); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := environment.buildAPIImage(ctx); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	if err := environment.replaceAPIs(ctx, 3, nil); err != nil {
		environment.close(context.Background())
		return nil, err
	}
	return environment, nil
}

func (e *e2eEnvironment) startPostgres(ctx context.Context) error {
	container, err := e.run(ctx, testcontainers.ContainerRequest{
		Image: postgresImage,
		Env: map[string]string{
			"POSTGRES_DB":       "wagering",
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": "postgres",
		},
		ExposedPorts: []string{"5432/tcp"},
		Networks:     []string{e.networkName},
		NetworkAliases: map[string][]string{
			e.networkName: {postgresAlias},
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(90 * time.Second),
	})
	if err != nil {
		return fmt.Errorf("start PostgreSQL test container: %w", err)
	}
	endpoint, err := container.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return fmt.Errorf("resolve PostgreSQL test endpoint: %w", err)
	}
	e.databaseURL = "postgres://postgres:postgres@" + endpoint + "/wagering?sslmode=disable"
	return waitForPostgres(ctx, e.databaseURL)
}

func waitForPostgres(ctx context.Context, databaseURL string) error {
	deadline := time.Now().Add(45 * time.Second)
	var lastError error
	for time.Now().Before(deadline) {
		connection, err := pgx.Connect(ctx, databaseURL)
		if err == nil {
			connection.Close(ctx)
			return nil
		}
		lastError = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("wait for PostgreSQL readiness: %w", lastError)
}

func applyMigrations(ctx context.Context, root, databaseURL string) error {
	migrationsPath := filepath.ToSlash(filepath.Join(root, "internal", "infrastructure", "postgres", "migrations"))
	migration, err := migrate.New("file://"+migrationsPath, databaseURL)
	if err != nil {
		return fmt.Errorf("open test database migrations: %w", err)
	}
	defer func() {
		_, _ = migration.Close()
	}()
	if err := migration.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply test database migrations: %w", err)
	}
	return nil
}

func (e *e2eEnvironment) assertEmptyDomainDatabase(ctx context.Context) error {
	connection, err := pgx.Connect(ctx, e.databaseURL)
	if err != nil {
		return fmt.Errorf("connect to fresh test database: %w", err)
	}
	defer connection.Close(ctx)
	var domainRows int
	err = connection.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM wallets) +
		(SELECT count(*) FROM wager_transactions) +
		(SELECT count(*) FROM wallet_ledger_entries) +
		(SELECT count(*) FROM inbox_messages) +
		(SELECT count(*) FROM outbox_events)`).Scan(&domainRows)
	if err != nil {
		return fmt.Errorf("count fresh test database records: %w", err)
	}
	if domainRows != 0 {
		return fmt.Errorf("fresh test database has %d seeded domain records", domainRows)
	}
	e.emptyDomainDatabase = true
	return nil
}

func (e *e2eEnvironment) startMiniStack(ctx context.Context) error {
	bootstrapDir := filepath.Join(e.root, "scripts", "aws-emulator")
	credentialsPath := "/tmp/jungle-gaming-sqs-credentials.json"
	container, err := e.run(ctx, testcontainers.ContainerRequest{
		Image: ministackImage,
		Env: map[string]string{
			"AUTH":                         "true",
			"AWS_DEFAULT_REGION":           "us-east-1",
			"AWS_ACCESS_KEY_ID":            "test",
			"AWS_SECRET_ACCESS_KEY":        "test",
			"PERSIST_STATE":                "0",
			"SQS_APP_CREDENTIALS_PATH":     "/tmp/jungle-gaming-app-credentials",
			"SQS_TOOLING_CREDENTIALS_PATH": "/tmp/jungle-gaming-tooling-credentials",
			"SQS_IAM_READY_PATH":           "/tmp/jungle-gaming-sqs-iam-ready",
			"SQS_CREDENTIALS_JSON_PATH":    credentialsPath,
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      filepath.Join(bootstrapDir, "00-clear-ready.sh"),
				ContainerFilePath: "/docker-entrypoint-initaws.d/00-clear-ready.sh",
				FileMode:          0o755,
			},
			{
				HostFilePath:      filepath.Join(bootstrapDir, "10-bootstrap.py"),
				ContainerFilePath: "/docker-entrypoint-initaws.d/ready.d/10-bootstrap.py",
				FileMode:          0o755,
			},
		},
		ExposedPorts: []string{"4566/tcp"},
		Networks:     []string{e.networkName},
		NetworkAliases: map[string][]string{
			e.networkName: {ministackAlias},
		},
		WaitingFor: wait.ForHTTP("/_ministack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(90 * time.Second),
	})
	if err != nil {
		return fmt.Errorf("start MiniStack test container: %w", err)
	}
	endpoint, err := container.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return fmt.Errorf("resolve MiniStack test endpoint: %w", err)
	}
	e.sqsEndpoint = endpoint
	return e.loadSQSCredentials(ctx, container, credentialsPath)
}

func (e *e2eEnvironment) loadSQSCredentials(
	ctx context.Context,
	container testcontainers.Container,
	path string,
) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		reader, err := container.CopyFileFromContainer(ctx, path)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}
		contents, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if readErr != nil {
			return fmt.Errorf("read MiniStack credentials: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close MiniStack credentials: %w", closeErr)
		}
		credentials := make(map[string]sqsCredential)
		if err := json.Unmarshal(contents, &credentials); err != nil {
			return fmt.Errorf("decode MiniStack credentials: %w", err)
		}
		for _, profile := range []string{"application", "provider-publisher", "event-consumer", "test-harness", "denied"} {
			credential := credentials[profile]
			if credential.AccessKeyID == "" || credential.SecretAccessKey == "" {
				return fmt.Errorf("MiniStack credential profile %q is incomplete", profile)
			}
		}
		e.sqsCredentials = credentials
		return nil
	}
	return fmt.Errorf("wait for MiniStack IAM bootstrap: %w", lastErr)
}

func (e *e2eEnvironment) startKeycloak(ctx context.Context) error {
	realmFile := filepath.Join(e.root, "docs", "keycloak-realm.json")
	container, err := e.run(ctx, testcontainers.ContainerRequest{
		Image: keycloakImage,
		Cmd:   []string{"start-dev", "--import-realm"},
		Env: map[string]string{
			"KC_BOOTSTRAP_ADMIN_USERNAME": "admin",
			"KC_BOOTSTRAP_ADMIN_PASSWORD": "admin-local",
			"KC_HEALTH_ENABLED":           "true",
			"KC_HTTP_ENABLED":             "true",
			"KC_HOSTNAME":                 "http://" + keycloakAlias + ":8080",
			"KC_HOSTNAME_STRICT":          "false",
		},
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      realmFile,
			ContainerFilePath: "/opt/keycloak/data/import/gaming-realm.json",
			FileMode:          0o644,
		}},
		ExposedPorts: []string{"8080/tcp"},
		Networks:     []string{e.networkName},
		NetworkAliases: map[string][]string{
			e.networkName: {keycloakAlias},
		},
		WaitingFor: wait.ForHTTP("/realms/gaming/.well-known/openid-configuration").
			WithPort("8080/tcp").
			WithStartupTimeout(3 * time.Minute),
	})
	if err != nil {
		return fmt.Errorf("start Keycloak test container: %w", err)
	}
	endpoint, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		return fmt.Errorf("resolve Keycloak test endpoint: %w", err)
	}
	e.keycloakURL = endpoint
	return nil
}

func (e *e2eEnvironment) buildAPIImage(ctx context.Context) error {
	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return fmt.Errorf("create Docker provider for API image: %w", err)
	}
	e.imageClient = provider
	image, err := provider.BuildImage(ctx, &testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    e.root,
			Dockerfile: "Dockerfile",
			Repo:       "jungle-gaming-e2e",
			Tag:        fmt.Sprint(time.Now().UnixNano()),
			BuildOptionsModifier: func(options *client.ImageBuildOptions) {
				options.NoCache = os.Getenv("E2E_NO_CACHE") == "1"
			},
		},
	})
	if err != nil {
		return fmt.Errorf("build E2E API image: %w", err)
	}
	e.apiImage = image
	return nil
}

func (e *e2eEnvironment) replaceAPIs(ctx context.Context, count int, overrides map[string]string) error {
	for _, api := range e.apis {
		if api != nil {
			_ = api.Terminate(ctx)
		}
	}
	e.apis = nil
	e.apiURLs = nil
	for index := 0; index < count; index++ {
		api, endpoint, err := e.startAPI(ctx, index+1, overrides)
		if err != nil {
			return err
		}
		e.apis = append(e.apis, api)
		e.apiURLs = append(e.apiURLs, endpoint)
	}
	configureE2EGlobals(e)
	return nil
}

func (e *e2eEnvironment) startAPI(ctx context.Context, index int, overrides map[string]string) (testcontainers.Container, string, error) {
	env := map[string]string{
		"HTTP_ADDRESS":                            ":8080",
		"DATABASE_URL":                            "postgres://postgres:postgres@postgres:5432/wagering?sslmode=disable",
		"DATABASE_MAX_CONNECTIONS":                "12",
		"OIDC_ISSUER_URL":                         internalIssuer,
		"OIDC_DISCOVERY_URL":                      internalIssuer,
		"OIDC_AUDIENCE":                           "wagering-api",
		"OIDC_PROVIDER_CLAIM":                     "provider_id",
		"OIDC_INTERNAL_ROLE":                      "wallet-service",
		"OIDC_STARTUP_TIMEOUT":                    "30s",
		"AWS_REGION":                              "us-east-1",
		"AWS_ENDPOINT_URL":                        "http://ministack:4566",
		"AWS_ACCESS_KEY_ID":                       e.sqsCredentials["application"].AccessKeyID,
		"AWS_SECRET_ACCESS_KEY":                   e.sqsCredentials["application"].SecretAccessKey,
		"SQS_INPUT_QUEUE":                         "wager-transactions.fifo",
		"SQS_INPUT_DLQ":                           "wager-transactions-dlq.fifo",
		"SQS_EVENT_QUEUE":                         "wager-events.fifo",
		"SQS_EVENT_DLQ":                           "wager-events-dlq.fifo",
		"SQS_CREATE_QUEUES":                       "false",
		"SQS_DISABLE_MESSAGE_CHECKSUM_VALIDATION": "true",
		"SQS_MAX_RECEIVES":                        "5",
		"SQS_VISIBILITY_TIMEOUT_SECONDS":          "30",
		"SQS_RETRY_BASE_BACKOFF":                  "2s",
		"SQS_RETRY_MAX_BACKOFF":                   "2m",
		"WORKERS_ENABLED":                         "true",
		"SQS_CONSUMER_CONCURRENCY":                "2",
		"OUTBOX_BATCH_SIZE":                       "50",
		"REFERENCE_BATCH_SIZE":                    "50",
		"REFERENCE_MAX_ATTEMPTS":                  "10",
		"REFERENCE_BASE_BACKOFF":                  "2s",
		"REFERENCE_MAX_BACKOFF":                   "5m",
		"OUTBOX_LEASE":                            "30s",
		"WORKER_POLL_INTERVAL":                    "200ms",
		"OTEL_TRACING_ENABLED":                    "false",
		"OTEL_SERVICE_NAME":                       "jungle-gaming-api",
		"OTEL_SERVICE_VERSION":                    "e2e-test",
		"OTEL_DEPLOYMENT_ENVIRONMENT":             "testcontainers",
		"OTEL_EXPORTER_OTLP_ENDPOINT":             "localhost:4317",
		"OTEL_EXPORTER_OTLP_INSECURE":             "true",
		"OTEL_TRACES_SAMPLE_RATIO":                "0",
	}
	for key, value := range overrides {
		env[key] = value
	}
	container, err := e.run(ctx, testcontainers.ContainerRequest{
		Image:        e.apiImage,
		Env:          env,
		ExposedPorts: []string{"8080/tcp"},
		Networks:     []string{e.networkName},
		NetworkAliases: map[string][]string{
			e.networkName: {fmt.Sprintf("api-%d", index)},
		},
		WaitingFor: wait.ForHTTP("/health/ready").
			WithPort("8080/tcp").
			WithStartupTimeout(90 * time.Second),
	})
	if err != nil {
		return nil, "", fmt.Errorf("start API %d test container: %w", index, err)
	}
	endpoint, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		return nil, "", fmt.Errorf("resolve API %d test endpoint: %w", index, err)
	}
	return container, endpoint, nil
}

func (e *e2eEnvironment) run(ctx context.Context, request testcontainers.ContainerRequest) (testcontainers.Container, error) {
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if container != nil {
		e.containers = append(e.containers, container)
	}
	return container, err
}

func (e *e2eEnvironment) close(ctx context.Context) {
	for index := len(e.containers) - 1; index >= 0; index-- {
		if e.containers[index] != nil {
			_ = e.containers[index].Terminate(ctx)
		}
	}
	if e.imageClient != nil {
		if e.apiImage != "" {
			_, _ = e.imageClient.Client().ImageRemove(ctx, e.apiImage, client.ImageRemoveOptions{
				Force:         true,
				PruneChildren: true,
			})
		}
		_ = e.imageClient.Close()
	}
	if e.network != nil {
		_ = e.network.Remove(ctx)
	}
}

func configureE2EGlobals(environment *e2eEnvironment) {
	keycloakURL = environment.keycloakURL
	oidcIssuerURL = internalIssuer
	postgresURL = environment.databaseURL
	sqsEndpoint = environment.sqsEndpoint
	apiURLs = append([]string(nil), environment.apiURLs...)
}

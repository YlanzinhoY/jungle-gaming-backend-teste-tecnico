//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/composition"
	postgresadapter "github.com/enzom/jungle-gaming/internal/infrastructure/postgres"
)

var (
	keycloakURL = environment("INTEGRATION_KEYCLOAK_URL", "http://localhost:8081")
	postgresURL = environment(
		"INTEGRATION_DATABASE_URL",
		"postgres://postgres:postgres@localhost:5432/wagering?sslmode=disable",
	)
	sqsEndpoint = environment("INTEGRATION_SQS_ENDPOINT", "http://localhost:4566")
	apiURLs     = strings.Split(environment(
		"INTEGRATION_API_URLS",
		"http://localhost:8080,http://localhost:8082,http://localhost:8083",
	), ",")
)

type testClient struct {
	internalToken  string
	providerToken  string
	providerBToken string
	providerCToken string
}

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
	Balance  money  `json:"balance"`
	Version  int64  `json:"version"`
}

type wagerResult struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	Balance          money  `json:"balance"`
	FailureCode      string `json:"failureCode"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

type problemResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type transaction struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	FailureCode     string `json:"failureCode"`
	ResultBalance   *money `json:"resultBalance"`
	AttemptCount    int    `json:"attemptCount"`
	TransactionKind string `json:"kind"`
}

type ledgerPage struct {
	Items []struct {
		ID            string `json:"id"`
		TransactionID string `json:"transactionId"`
		Direction     string `json:"direction"`
	} `json:"items"`
}

type outboundEventEnvelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId"`
	CausationID   *string         `json:"causationId"`
	OccurredAt    string          `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

type receivedOutboundEvent struct {
	Envelope outboundEventEnvelope
	Fields   map[string]json.RawMessage
	Attrs    map[string]string
}

type expectedOutboundEvent struct {
	EventType     string
	AggregateID   string
	CorrelationID string
	CausationID   *string
	DataFields    []string
	DataValues    map[string]any
	TimeDataField string
}

func TestHealthMetricsAndAuthorization(t *testing.T) {
	client := newTestClient(t)

	for _, path := range []string{"/health/live", "/health/ready"} {
		status, _, err := request(http.MethodGet, apiURLs[0]+path, "", nil, nil)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if status != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, status)
		}
	}

	status, body, err := request(http.MethodGet, apiURLs[0]+"/metrics", "", nil, nil)
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	if status != http.StatusOK || !bytes.Contains(body, []byte("jungle_wager_transactions_total")) {
		t.Fatalf("GET /metrics status=%d missing application metrics", status)
	}

	status, body, _ = request(http.MethodPost, apiURLs[0]+"/wallets", "", map[string]any{}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("wallet without token status = %d, want 401", status)
	}
	assertProblemCode(t, body, "AUTHENTICATION_REQUIRED")
	status, body, _ = request(http.MethodPost, apiURLs[0]+"/wallets", "invalid", map[string]any{}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("wallet with invalid token status = %d, want 401", status)
	}
	assertProblemCode(t, body, "INVALID_TOKEN")
	status, body, _ = request(http.MethodPost, apiURLs[0]+"/wallets", client.providerToken, map[string]any{}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("wallet with provider token status = %d, want 403", status)
	}
	assertProblemCode(t, body, "INTERNAL_ROLE_REQUIRED")
	status, _, _ = request(
		http.MethodGet,
		apiURLs[0]+"/providers/provider-b/wagering/transactions/unknown",
		client.providerToken,
		nil,
		nil,
	)
	if status != http.StatusForbidden {
		t.Fatalf("cross-provider query status = %d, want 403", status)
	}

	status, body, _ = request(http.MethodGet, apiURLs[0]+"/unknown", "", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", status)
	}
	assertProblemCode(t, body, "ROUTE_NOT_FOUND")
	status, body, _ = request(http.MethodGet, apiURLs[0]+"/wallets", "", nil, nil)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, want 405", status)
	}
	assertProblemCode(t, body, "ROUTE_METHOD_NOT_ALLOWED")
}

func TestProviderAIsIsolatedFromExistingProviderBTransaction(t *testing.T) {
	client := newTestClient(t)
	currentWallet := client.createWallet(t, "100.00")
	externalID := "provider-b-" + uuid.NewString()
	payload := wagerPayloadForProvider(
		"provider-b",
		currentWallet,
		externalID,
		"round-"+uuid.NewString(),
		"BET",
		"25.00",
		"",
	)
	idempotencyKey := "provider-b:" + externalID

	status, body, err := request(
		http.MethodPost,
		apiURLs[0]+"/wagering/transactions",
		client.providerBToken,
		payload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("provider B transaction = status %d, error %v, body %s", status, err, body)
	}
	var created wagerResult
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode provider B transaction: %v", err)
	}
	if created.TransactionID == "" || created.IdempotentReplay {
		t.Fatalf("provider B transaction = %+v, want a new transaction", created)
	}

	status, body, err = request(
		http.MethodGet,
		apiURLs[1]+"/providers/provider-b/wagering/transactions/"+externalID,
		client.providerToken,
		nil,
		nil,
	)
	if err != nil || status != http.StatusForbidden {
		t.Fatalf("provider A query of provider B transaction = status %d, error %v, body %s", status, err, body)
	}
	assertProblemCode(t, body, "PROVIDER_FORBIDDEN")

	status, body, err = request(
		http.MethodPost,
		apiURLs[2]+"/wagering/transactions",
		client.providerToken,
		payload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusForbidden {
		t.Fatalf("provider A replay of provider B transaction = status %d, error %v, body %s", status, err, body)
	}
	assertProblemCode(t, body, "PROVIDER_FORBIDDEN")

	status, body, err = request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerBToken,
		payload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("provider B replay after denied provider A requests = status %d, error %v, body %s", status, err, body)
	}
	var replay wagerResult
	if err := json.Unmarshal(body, &replay); err != nil {
		t.Fatalf("decode provider B replay: %v", err)
	}
	if replay.TransactionID != created.TransactionID || !replay.IdempotentReplay {
		t.Fatalf("provider B replay = %+v, want original transaction %s and idempotentReplay=true", replay, created.TransactionID)
	}

	client.assertWallet(t, apiURLs[2], currentWallet.ID, "75.00", 2)
	client.assertLedgerEntries(t, currentWallet.ID, 2)

	pool := newPool(t)
	var externalTransactions int
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND origin='EXTERNAL' AND provider_id='provider-b'`,
		currentWallet.ID,
	).Scan(&externalTransactions); err != nil {
		t.Fatalf("count provider B transactions: %v", err)
	}
	if externalTransactions != 1 {
		t.Fatalf("provider B transaction count = %d, want 1 after denied provider A requests", externalTransactions)
	}
}

func TestProviderCClientCredentialsAreAccepted(t *testing.T) {
	client := newTestClient(t)
	status, body, err := request(
		http.MethodGet,
		apiURLs[0]+"/providers/provider-c/wagering/transactions/not-created",
		client.providerCToken,
		nil,
		nil,
	)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("provider C authenticated query = status %d, error %v, body %s", status, err, body)
	}
	assertProblemCode(t, body, "NOT_FOUND")
}

func TestOutboundEventsMatchDocumentedContract(t *testing.T) {
	client := newTestClient(t)
	sqsClient := newSQSClient(t)
	eventQueueURL := queueURL(t, sqsClient, "wager-events.fifo")
	drainQueue(t, sqsClient, eventQueueURL)

	currentWallet := client.createWallet(t, "100.00")
	marker := "event-contract-" + uuid.NewString()
	processedExternalID := "processed-" + uuid.NewString()
	rejectedExternalID := "rejected-" + uuid.NewString()
	pendingExternalID := "pending-" + uuid.NewString()
	missingReferenceID := "missing-" + uuid.NewString()
	roundID := "round-" + uuid.NewString()

	status, body, err := request(
		http.MethodPost,
		apiURLs[0]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(currentWallet, processedExternalID, roundID, "BET", "25.00", ""),
		map[string]string{
			"Idempotency-Key":  "provider-a:" + processedExternalID,
			"X-Correlation-ID": marker,
		},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("processed wager = status %d, error %v, body %s", status, err, body)
	}
	var processed wagerResult
	if err := json.Unmarshal(body, &processed); err != nil {
		t.Fatalf("decode processed wager: %v", err)
	}

	status, body, err = request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(currentWallet, rejectedExternalID, roundID, "BET", "100.00", ""),
		map[string]string{
			"Idempotency-Key":  "provider-a:" + rejectedExternalID,
			"X-Correlation-ID": marker,
		},
	)
	if err != nil || status != http.StatusUnprocessableEntity {
		t.Fatalf("rejected wager = status %d, error %v, body %s", status, err, body)
	}
	var rejected wagerResult
	if err := json.Unmarshal(body, &rejected); err != nil {
		t.Fatalf("decode rejected wager: %v", err)
	}
	if rejected.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("rejected wager failure code = %q, want INSUFFICIENT_FUNDS", rejected.FailureCode)
	}

	status, body, err = request(
		http.MethodPost,
		apiURLs[2]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(currentWallet, pendingExternalID, roundID, "REFUND", "25.00", missingReferenceID),
		map[string]string{
			"Idempotency-Key":  "provider-a:" + pendingExternalID,
			"X-Correlation-ID": marker,
		},
	)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("pending reference wager = status %d, error %v, body %s", status, err, body)
	}
	var pending wagerResult
	if err := json.Unmarshal(body, &pending); err != nil {
		t.Fatalf("decode pending reference wager: %v", err)
	}
	if pending.Status != "PENDING_REFERENCE" {
		t.Fatalf("pending reference status = %s, want PENDING_REFERENCE", pending.Status)
	}

	// The normal retry policy takes several minutes. Use the production recovery
	// method with a one-attempt policy so the terminal-failure envelope is also
	// emitted and verified in this integration test.
	pool := newPool(t)
	failingRecovery := application.NewWagerService(
		postgresadapter.NewStore(pool),
		contractValidator{},
		contractMetrics{},
		slog.Default(),
		application.ReferencePolicy{MaxAttempts: 1},
	)
	if err := failingRecovery.RecordPendingReferenceFailure(context.Background(), pending.TransactionID); err != nil {
		t.Fatalf("record terminal reference failure: %v", err)
	}

	walletVersion := int64(2)
	want := map[string]expectedOutboundEvent{
		"WagerTransactionProcessed:" + processed.TransactionID: {
			EventType:     "WagerTransactionProcessed",
			AggregateID:   processed.TransactionID,
			CorrelationID: marker,
			DataFields: []string{
				"transactionId", "externalTransactionId", "providerId", "walletId", "playerId", "roundId", "gameId",
				"kind", "money", "status", "balance",
			},
			DataValues: map[string]any{
				"transactionId":         processed.TransactionID,
				"externalTransactionId": processedExternalID,
				"providerId":            "provider-a",
				"walletId":              currentWallet.ID,
				"playerId":              currentWallet.PlayerID,
				"roundId":               roundID,
				"gameId":                "fortune-chimp",
				"kind":                  "BET",
				"money":                 map[string]string{"amount": "25.00", "currency": "BRL"},
				"status":                "PROCESSED",
				"balance":               map[string]string{"amount": "75.00", "currency": "BRL"},
			},
		},
		"WalletBalanceChanged:" + processed.TransactionID: {
			EventType:     "WalletBalanceChanged",
			AggregateID:   currentWallet.ID,
			CorrelationID: marker,
			CausationID:   &processed.TransactionID,
			DataFields:    []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"},
			DataValues: map[string]any{
				"walletId":      currentWallet.ID,
				"transactionId": processed.TransactionID,
				"direction":     "DEBIT",
				"money":         map[string]string{"amount": "25.00", "currency": "BRL"},
				"balanceBefore": map[string]string{"amount": "100.00", "currency": "BRL"},
				"balanceAfter":  map[string]string{"amount": "75.00", "currency": "BRL"},
				"walletVersion": walletVersion,
			},
		},
		"WagerTransactionRejected:" + rejected.TransactionID: {
			EventType:     "WagerTransactionRejected",
			AggregateID:   rejected.TransactionID,
			CorrelationID: marker,
			DataFields:    []string{"transactionId", "externalTransactionId", "providerId", "walletId", "kind", "status", "failureCode", "balance"},
			DataValues: map[string]any{
				"transactionId":         rejected.TransactionID,
				"externalTransactionId": rejectedExternalID,
				"providerId":            "provider-a",
				"walletId":              currentWallet.ID,
				"kind":                  "BET",
				"status":                "REJECTED",
				"failureCode":           "INSUFFICIENT_FUNDS",
				"balance":               map[string]string{"amount": "75.00", "currency": "BRL"},
			},
		},
		"WagerTransactionPendingReference:" + pending.TransactionID: {
			EventType:     "WagerTransactionPendingReference",
			AggregateID:   pending.TransactionID,
			CorrelationID: marker,
			DataFields: []string{
				"transactionId", "externalTransactionId", "providerId", "walletId", "referenceExternalTransactionId", "status", "nextAttemptAt",
			},
			DataValues: map[string]any{
				"transactionId":                  pending.TransactionID,
				"externalTransactionId":          pendingExternalID,
				"providerId":                     "provider-a",
				"walletId":                       currentWallet.ID,
				"referenceExternalTransactionId": missingReferenceID,
				"status":                         "PENDING_REFERENCE",
			},
			TimeDataField: "nextAttemptAt",
		},
		"WagerTransactionFailed:" + pending.TransactionID: {
			EventType:     "WagerTransactionFailed",
			AggregateID:   pending.TransactionID,
			CorrelationID: pending.TransactionID,
			DataFields:    []string{"transactionId", "externalTransactionId", "providerId", "walletId", "kind", "status", "failureCode", "balance"},
			DataValues: map[string]any{
				"transactionId":         pending.TransactionID,
				"externalTransactionId": pendingExternalID,
				"providerId":            "provider-a",
				"walletId":              currentWallet.ID,
				"kind":                  "REFUND",
				"status":                "FAILED",
				"failureCode":           "PROCESSING_ATTEMPTS_EXHAUSTED",
				"balance":               map[string]string{"amount": "75.00", "currency": "BRL"},
			},
		},
	}

	got := receiveOutboundEvents(t, sqsClient, eventQueueURL, want)
	for key, expected := range want {
		assertOutboundEventContract(t, got[key], expected)
	}
}

func TestFxLifecycleAgainstRealDependencies(t *testing.T) {
	t.Setenv("HTTP_ADDRESS", "127.0.0.1:0")
	t.Setenv("DATABASE_URL", postgresURL)
	t.Setenv(
		"OIDC_ISSUER_URL",
		environment("INTEGRATION_OIDC_ISSUER_URL", keycloakURL+"/realms/gaming"),
	)
	t.Setenv("OIDC_DISCOVERY_URL", keycloakURL+"/realms/gaming")
	t.Setenv("AWS_ENDPOINT_URL", sqsEndpoint)
	t.Setenv("WORKERS_ENABLED", "true")
	t.Setenv("SQS_CONSUMER_CONCURRENCY", "1")

	app := fx.New(composition.Module(), fx.NopLogger)
	startContext, cancelStart := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStart()
	if err := app.Start(startContext); err != nil {
		t.Fatalf("fx application start: %v", err)
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelStop()
	if err := app.Stop(stopContext); err != nil {
		t.Fatalf("fx application stop: %v", err)
	}
}

func TestWalletOpeningRules(t *testing.T) {
	client := newTestClient(t)
	pool := newPool(t)
	playerID := uuid.NewString()
	payload := map[string]any{
		"playerId":       playerID,
		"initialBalance": map[string]string{"amount": "0.00", "currency": "BRL"},
	}
	status, body, err := request(http.MethodPost, apiURLs[0]+"/wallets", client.internalToken, payload, nil)
	if err != nil || status != http.StatusCreated {
		t.Fatalf("zero wallet = status %d, error %v, body %s", status, err, body)
	}
	var zeroWallet wallet
	if err := json.Unmarshal(body, &zeroWallet); err != nil {
		t.Fatalf("decode zero wallet: %v", err)
	}
	client.assertWallet(t, apiURLs[1], zeroWallet.ID, "0.00", 1)
	client.assertLedgerEntries(t, zeroWallet.ID, 0)

	status, _, err = request(http.MethodPost, apiURLs[2]+"/wallets", client.internalToken, payload, nil)
	if err != nil {
		t.Fatalf("duplicate wallet: %v", err)
	}
	if status != http.StatusConflict {
		t.Fatalf("duplicate player/currency wallet status = %d, want 409", status)
	}

	positiveWallet := client.createWallet(t, "12.34")
	var (
		origin             string
		kind               string
		externalFieldsNull bool
		eventCount         int
	)
	if err := pool.QueryRow(
		context.Background(),
		`SELECT origin, kind,
			(provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
			 AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL)
		 FROM wager_transactions WHERE wallet_id=$1 AND kind='OPENING'`,
		positiveWallet.ID,
	).Scan(&origin, &kind, &externalFieldsNull); err != nil {
		t.Fatalf("query opening transaction: %v", err)
	}
	if origin != "INTERNAL" || kind != "OPENING" || !externalFieldsNull {
		t.Fatalf("opening metadata = origin %s, kind %s, external fields null %t", origin, kind, externalFieldsNull)
	}
	if err := pool.QueryRow(
		context.Background(),
		`SELECT count(*) FROM outbox_events
		 WHERE payload->'data'->>'walletId'=$1
		   AND event_type IN ('WagerTransactionProcessed','WalletBalanceChanged')`,
		positiveWallet.ID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("query opening events: %v", err)
	}
	if eventCount != 2 {
		t.Fatalf("opening outbox events = %d, want 2", eventCount)
	}
}

func TestMigrationsUpAndDownOnIsolatedDatabase(t *testing.T) {
	adminConfig, err := pgxpool.ParseConfig(postgresURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL URL: %v", err)
	}
	adminConfig.ConnConfig.Database = "postgres"
	adminPool, err := pgxpool.NewWithConfig(context.Background(), adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	defer adminPool.Close()

	databaseName := "migration_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := adminPool.Exec(context.Background(), "CREATE DATABASE "+databaseName); err != nil {
		t.Fatalf("create isolated database: %v", err)
	}
	defer func() {
		_, _ = adminPool.Exec(context.Background(), "DROP DATABASE "+databaseName+" WITH (FORCE)")
	}()

	testConfig, err := pgxpool.ParseConfig(postgresURL)
	if err != nil {
		t.Fatalf("parse test PostgreSQL URL: %v", err)
	}
	testConfig.ConnConfig.Database = databaseName
	testPool, err := pgxpool.NewWithConfig(context.Background(), testConfig)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	migrationURL, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatalf("parse migration PostgreSQL URL: %v", err)
	}
	migrationURL.Path = "/" + databaseName
	migrator, err := migrate.New(
		"file://../internal/infrastructure/postgres/migrations",
		migrationURL.String(),
	)
	if err != nil {
		t.Fatalf("create golang-migrate instance: %v", err)
	}
	defer func() {
		_, _ = migrator.Close()
	}()
	if err := migrator.Up(); err != nil {
		t.Fatalf("first migration up: %v", err)
	}
	if err := migrator.Up(); !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("idempotent migration up: %v, want ErrNoChange", err)
	}
	version, dirty, err := migrator.Version()
	if err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 4 || dirty {
		t.Fatalf("migration state = (%d, dirty=%t), want (4, false)", version, dirty)
	}
	for index := 0; index < 4; index++ {
		if err := migrator.Steps(-1); err != nil {
			t.Fatalf("migration down %d: %v", index+1, err)
		}
	}
	var walletsTable *string
	if err := testPool.QueryRow(context.Background(), `SELECT to_regclass('public.wallets')::text`).Scan(&walletsTable); err != nil {
		t.Fatalf("check tables after down: %v", err)
	}
	if walletsTable != nil {
		t.Fatalf("wallets table remains after all down migrations: %s", *walletsTable)
	}
	testPool.Close()
}

func TestAllExternalOperationKindsAndReconciliation(t *testing.T) {
	client := newTestClient(t)
	currentWallet := client.createWallet(t, "100.00")
	roundID := "round-" + uuid.NewString()
	betID := "bet-" + uuid.NewString()
	winID := "win-" + uuid.NewString()
	rollbackID := "rollback-" + uuid.NewString()
	refundID := "refund-" + uuid.NewString()
	lossID := "loss-" + uuid.NewString()

	operations := []struct {
		externalID string
		kind       string
		amount     string
		reference  string
	}{
		{externalID: betID, kind: "BET", amount: "20.00"},
		{externalID: winID, kind: "WIN", amount: "30.00", reference: betID},
		{externalID: lossID, kind: "LOSS", amount: "0.00"},
		{externalID: rollbackID, kind: "ROLLBACK", amount: "30.00", reference: winID},
		{externalID: refundID, kind: "REFUND", amount: "20.00", reference: betID},
	}
	for index, operation := range operations {
		status, body, err := request(
			http.MethodPost,
			apiURLs[index%len(apiURLs)]+"/wagering/transactions",
			client.providerToken,
			wagerPayload(
				currentWallet,
				operation.externalID,
				roundID,
				operation.kind,
				operation.amount,
				operation.reference,
			),
			map[string]string{"Idempotency-Key": "provider-a:" + operation.externalID},
		)
		if err != nil || status != http.StatusOK {
			t.Fatalf("%s = status %d, error %v, body %s", operation.kind, status, err, body)
		}
	}

	client.assertWallet(t, apiURLs[2], currentWallet.ID, "100.00", 5)
	client.assertLedgerEntries(t, currentWallet.ID, 5)
	status, body, err := request(
		http.MethodPost,
		apiURLs[1]+"/wallets/"+currentWallet.ID+"/reconciliation",
		client.internalToken,
		nil,
		nil,
	)
	if err != nil || status != http.StatusOK || !bytes.Contains(body, []byte(`"consistent":true`)) {
		t.Fatalf("reconciliation = status %d, error %v, body %s", status, err, body)
	}
}

func TestIdempotencyAcrossThreeInstances(t *testing.T) {
	client := newTestClient(t)
	wallet := client.createWallet(t, "100.00")
	externalID := "transaction-" + uuid.NewString()
	idempotencyKey := "provider-a:" + externalID
	payload := wagerPayload(wallet, externalID, "round-"+uuid.NewString(), "BET", "25.00", "")

	type response struct {
		status int
		result wagerResult
		err    error
	}
	responses := make(chan response, 50)
	var workers sync.WaitGroup
	for index := 0; index < 50; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			status, body, err := request(
				http.MethodPost,
				apiURLs[index%len(apiURLs)]+"/wagering/transactions",
				client.providerToken,
				payload,
				map[string]string{"Idempotency-Key": idempotencyKey},
			)
			var result wagerResult
			if err == nil {
				err = json.Unmarshal(body, &result)
			}
			responses <- response{status: status, result: result, err: err}
		}(index)
	}
	workers.Wait()
	close(responses)

	originals := 0
	for response := range responses {
		if response.err != nil {
			t.Fatalf("parallel request error = %v", response.err)
		}
		if response.status != http.StatusOK || response.result.Status != "PROCESSED" {
			t.Fatalf("parallel result = status %d, body %+v", response.status, response.result)
		}
		if !response.result.IdempotentReplay {
			originals++
		}
	}
	if originals != 1 {
		t.Fatalf("non-replay responses = %d, want 1", originals)
	}
	client.assertWallet(t, apiURLs[2], wallet.ID, "75.00", 2)
	client.assertLedgerEntries(t, wallet.ID, 2)

	different := wagerPayload(wallet, externalID, payload["roundId"].(string), "BET", "26.00", "")
	status, _, _ := request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerToken,
		different,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if status != http.StatusConflict {
		t.Fatalf("same key with different payload status = %d, want 409", status)
	}
	status, _, _ = request(
		http.MethodPost,
		apiURLs[2]+"/wagering/transactions",
		client.providerToken,
		payload,
		map[string]string{"Idempotency-Key": idempotencyKey + ":different"},
	)
	if status != http.StatusConflict {
		t.Fatalf("same external ID with different key status = %d, want 409", status)
	}
}

func TestConcurrentEightyBRLBets(t *testing.T) {
	client := newTestClient(t)
	wallet := client.createWallet(t, "100.00")
	roundID := "round-" + uuid.NewString()

	type response struct {
		status int
		result wagerResult
		err    error
	}
	responses := make(chan response, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			externalID := fmt.Sprintf("bet-%d-%s", index, uuid.NewString())
			status, body, err := request(
				http.MethodPost,
				apiURLs[index]+"/wagering/transactions",
				client.providerToken,
				wagerPayload(wallet, externalID, roundID, "BET", "80.00", ""),
				map[string]string{"Idempotency-Key": "provider-a:" + externalID},
			)
			var result wagerResult
			if err == nil {
				err = json.Unmarshal(body, &result)
			}
			responses <- response{status: status, result: result, err: err}
		}(index)
	}

	statuses := make([]int, 0, 2)
	failures := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		response := <-responses
		if response.err != nil {
			t.Fatalf("concurrent bet error = %v", response.err)
		}
		statuses = append(statuses, response.status)
		failures = append(failures, response.result.FailureCode)
	}
	sort.Ints(statuses)
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusUnprocessableEntity {
		t.Fatalf("statuses = %v, want [200 422]", statuses)
	}
	if failures[0] != "INSUFFICIENT_FUNDS" && failures[1] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failure codes = %v, missing INSUFFICIENT_FUNDS", failures)
	}
	client.assertWallet(t, apiURLs[2], wallet.ID, "20.00", 2)
	client.assertLedgerEntries(t, wallet.ID, 2)
}

func TestDifferentWalletsAdvanceInParallel(t *testing.T) {
	client := newTestClient(t)
	wallets := []wallet{client.createWallet(t, "100.00"), client.createWallet(t, "100.00")}
	errorsFound := make(chan error, len(wallets))
	for index, current := range wallets {
		go func(index int, current wallet) {
			externalID := "parallel-" + uuid.NewString()
			status, _, err := request(
				http.MethodPost,
				apiURLs[index]+"/wagering/transactions",
				client.providerToken,
				wagerPayload(current, externalID, "round-"+uuid.NewString(), "BET", "10.00", ""),
				map[string]string{"Idempotency-Key": "provider-a:" + externalID},
			)
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("status = %d, want 200", status)
			}
			errorsFound <- err
		}(index, current)
	}
	for range wallets {
		if err := <-errorsFound; err != nil {
			t.Fatal(err)
		}
	}
	for _, current := range wallets {
		client.assertWallet(t, apiURLs[2], current.ID, "90.00", 2)
	}
}

func TestReferenceArrivesAfterRefund(t *testing.T) {
	client := newTestClient(t)
	wallet := client.createWallet(t, "100.00")
	roundID := "round-" + uuid.NewString()
	betExternalID := "bet-" + uuid.NewString()
	refundExternalID := "refund-" + uuid.NewString()

	status, body, err := request(
		http.MethodPost,
		apiURLs[0]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(wallet, refundExternalID, roundID, "REFUND", "25.00", betExternalID),
		map[string]string{"Idempotency-Key": "provider-a:" + refundExternalID},
	)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("early refund = status %d, error %v, body %s", status, err, body)
	}
	var pending wagerResult
	if err := json.Unmarshal(body, &pending); err != nil || pending.Status != "PENDING_REFERENCE" {
		t.Fatalf("early refund result = %+v, error %v", pending, err)
	}

	status, body, err = request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(wallet, betExternalID, roundID, "BET", "25.00", ""),
		map[string]string{"Idempotency-Key": "provider-a:" + betExternalID},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("referenced bet = status %d, error %v, body %s", status, err, body)
	}

	waitFor(t, 20*time.Second, func() (bool, error) {
		status, body, err := request(
			http.MethodGet,
			apiURLs[2]+"/providers/provider-a/wagering/transactions/"+refundExternalID,
			client.providerToken,
			nil,
			nil,
		)
		if err != nil || status != http.StatusOK {
			return false, err
		}
		var transaction transaction
		if err := json.Unmarshal(body, &transaction); err != nil {
			return false, err
		}
		return transaction.Status == "PROCESSED", nil
	})
	client.assertWallet(t, apiURLs[2], wallet.ID, "100.00", 3)
	client.assertLedgerEntries(t, wallet.ID, 3)
}

func TestHTTPAndSQSDeduplicateTheSameOperation(t *testing.T) {
	client := newTestClient(t)
	wallet := client.createWallet(t, "100.00")
	externalID := "cross-transport-" + uuid.NewString()
	idempotencyKey := "provider-a:" + externalID
	roundID := "round-" + uuid.NewString()
	payload := wagerPayload(wallet, externalID, roundID, "BET", "10.00", "")

	status, body, err := request(
		http.MethodPost,
		apiURLs[0]+"/wagering/transactions",
		client.providerToken,
		payload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("HTTP wager = status %d, error %v, body %s", status, err, body)
	}

	sqsClient := newSQSClient(t)
	inputURL := queueURL(t, sqsClient, "wager-transactions.fifo")
	messageIDs := []string{"message-" + uuid.NewString(), "message-" + uuid.NewString()}
	for _, messageID := range messageIDs {
		envelope := map[string]any{
			"messageId":  messageID,
			"type":       "WagerTransactionRequested",
			"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
			"data": map[string]any{
				"providerId":            payload["providerId"],
				"externalTransactionId": payload["externalTransactionId"],
				"idempotencyKey":        idempotencyKey,
				"playerId":              payload["playerId"],
				"walletId":              payload["walletId"],
				"roundId":               payload["roundId"],
				"gameId":                payload["gameId"],
				"kind":                  payload["kind"],
				"money":                 payload["money"],
			},
		}
		encoded, _ := json.Marshal(envelope)
		_, err = sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
			QueueUrl:               aws.String(inputURL),
			MessageBody:            aws.String(string(encoded)),
			MessageGroupId:         aws.String(wallet.ID),
			MessageDeduplicationId: aws.String(messageID),
		})
		if err != nil {
			t.Fatalf("SendMessage() error = %v", err)
		}
	}

	pool := newPool(t)
	waitFor(t, 60*time.Second, func() (bool, error) {
		var completed int
		err := pool.QueryRow(
			context.Background(),
			`SELECT count(*) FROM inbox_messages
			 WHERE consumer_name=$1 AND message_id=ANY($2) AND completed_at IS NOT NULL`,
			"wager-transaction-consumer-v1",
			messageIDs,
		).Scan(&completed)
		return completed == len(messageIDs), err
	})
	client.assertWallet(t, apiURLs[2], wallet.ID, "90.00", 2)
	client.assertLedgerEntries(t, wallet.ID, 2)
}

func TestInvalidSQSMessageReachesDLQ(t *testing.T) {
	sqsClient := newSQSClient(t)
	inputURL := queueURL(t, sqsClient, "wager-transactions.fifo")
	dlqURL := queueURL(t, sqsClient, "wager-transactions-dlq.fifo")
	marker := "invalid-" + uuid.NewString()
	attributes, err := sqsClient.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(inputURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameRedrivePolicy, types.QueueAttributeNameVisibilityTimeout},
	})
	if err != nil {
		t.Fatalf("GetQueueAttributes() error = %v", err)
	}
	if attributes.Attributes[string(types.QueueAttributeNameVisibilityTimeout)] != "30" ||
		!strings.Contains(attributes.Attributes[string(types.QueueAttributeNameRedrivePolicy)], `"maxReceiveCount":"5"`) {
		t.Fatalf("unexpected queue attributes: %+v", attributes.Attributes)
	}

	_, err = sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               aws.String(inputURL),
		MessageBody:            aws.String(marker),
		MessageGroupId:         aws.String(marker),
		MessageDeduplicationId: aws.String(marker),
	})
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	waitFor(t, 30*time.Second, func() (bool, error) {
		output, err := sqsClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(dlqURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			return false, err
		}
		for _, message := range output.Messages {
			matchesMarker := aws.ToString(message.Body) == marker
			if _, err := sqsClient.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(dlqURL),
				ReceiptHandle: message.ReceiptHandle,
			}); err != nil {
				return false, err
			}
			if matchesMarker {
				return true, nil
			}
		}
		return false, nil
	})
}

func TestDatabaseConstraintsAndAbandonedOutboxRecovery(t *testing.T) {
	client := newTestClient(t)
	wallet := client.createWallet(t, "100.00")
	pool := newPool(t)

	var ledgerID string
	if err := pool.QueryRow(
		context.Background(),
		`SELECT id::text FROM wallet_ledger_entries WHERE wallet_id=$1 LIMIT 1`,
		wallet.ID,
	).Scan(&ledgerID); err != nil {
		t.Fatalf("find ledger entry: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE wallet_ledger_entries SET money_minor=money_minor+1 WHERE id=$1`, ledgerID); err == nil {
		t.Fatal("database allowed ledger update")
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM wallet_ledger_entries WHERE id=$1`, ledgerID); err == nil {
		t.Fatal("database allowed ledger delete")
	}
	if _, err := pool.Exec(
		context.Background(),
		`INSERT INTO wallets(id,player_id,currency,balance_minor,version,created_at,updated_at) VALUES($1,$2,'BRL',-1,1,now(),now())`,
		uuid.NewString(),
		uuid.NewString(),
	); err == nil {
		t.Fatal("database allowed a negative wallet balance")
	}
	if _, err := pool.Exec(
		context.Background(),
		`UPDATE wallets SET balance_minor=balance_minor+1, version=version+1, updated_at=now() WHERE id=$1`,
		wallet.ID,
	); err == nil {
		t.Fatal("database allowed a wallet balance change without a ledger entry")
	}
	if _, err := pool.Exec(
		context.Background(),
		`INSERT INTO wager_transactions(
			id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, kind, money_minor, currency, status,
			result_balance_minor, result_currency, created_at, updated_at
		) VALUES (
			$1, 'EXTERNAL', $2, $3, $4, $5,
			$6, $7, 'direct-round', 'direct-game', 'BET', 100, 'BRL', 'PROCESSED',
			9900, 'BRL', now(), now()
		)`,
		uuid.NewString(),
		"direct-provider-"+uuid.NewString(),
		"direct-external-"+uuid.NewString(),
		"direct-key-"+uuid.NewString(),
		strings.Repeat("a", 64),
		wallet.ID,
		wallet.PlayerID,
	); err == nil {
		t.Fatal("database allowed a processed financial transaction without a ledger entry")
	}

	eventID := uuid.NewString()
	_, err := pool.Exec(
		context.Background(),
		`INSERT INTO outbox_events(event_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at,locked_by,locked_until)
		 VALUES($1,$2,'IntegrationRecoveryProbe',$3::jsonb,now(),now(),'dead-worker',now()+interval '1 second')`,
		eventID,
		wallet.ID,
		fmt.Sprintf(`{"eventId":%q,"eventType":"IntegrationRecoveryProbe"}`, eventID),
	)
	if err != nil {
		t.Fatalf("insert recovery event: %v", err)
	}
	waitFor(t, 20*time.Second, func() (bool, error) {
		var published bool
		err := pool.QueryRow(
			context.Background(),
			`SELECT published_at IS NOT NULL FROM outbox_events WHERE event_id=$1`,
			eventID,
		).Scan(&published)
		return published, err
	})
	if _, err := pool.Exec(
		context.Background(),
		`UPDATE outbox_events SET payload='{}'::jsonb WHERE event_id=$1`,
		eventID,
	); err == nil {
		t.Fatal("database allowed immutable outbox payload update")
	}
}

func TestTwoPublishersClaimEachOutboxEventOnce(t *testing.T) {
	pool := newPool(t)
	store := postgresadapter.NewStore(pool)
	marker := "IntegrationClaimProbe-" + uuid.NewString()
	now := time.Now().UTC()
	claimAt := now.Add(2 * time.Hour)

	for index := 0; index < 20; index++ {
		eventID := uuid.NewString()
		_, err := pool.Exec(
			context.Background(),
			`INSERT INTO outbox_events(event_id,aggregate_id,event_type,payload,occurred_at,next_attempt_at)
			 VALUES($1,$2,$3,$4::jsonb,$5,$6)`,
			eventID,
			"claim-probe-"+uuid.NewString(),
			marker,
			fmt.Sprintf(`{"eventId":%q,"eventType":%q}`, eventID, marker),
			now.Add(time.Duration(index)*time.Millisecond),
			now.Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("insert claim probe: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM outbox_events WHERE event_type=$1`, marker)
	})

	type claimResult struct {
		worker string
		ids    []string
		err    error
	}
	results := make(chan claimResult, 2)
	for index := 0; index < 2; index++ {
		workerID := fmt.Sprintf("integration-publisher-%d", index)
		go func() {
			events, err := store.ClaimOutbox(context.Background(), workerID, 20, time.Minute, claimAt)
			ids := make([]string, 0, len(events))
			for _, event := range events {
				if event.EventType == marker {
					ids = append(ids, event.EventID)
				}
			}
			results <- claimResult{worker: workerID, ids: ids, err: err}
		}()
	}

	claimedBy := make(map[string]string, 20)
	for index := 0; index < 2; index++ {
		result := <-results
		if result.err != nil {
			t.Fatalf("claim by %s: %v", result.worker, result.err)
		}
		for _, eventID := range result.ids {
			if firstWorker, duplicate := claimedBy[eventID]; duplicate {
				t.Fatalf("event %s claimed by both %s and %s", eventID, firstWorker, result.worker)
			}
			claimedBy[eventID] = result.worker
		}
	}
	if len(claimedBy) != 20 {
		t.Fatalf("claimed probe events = %d, want 20", len(claimedBy))
	}
}

func TestOutboxRecoversAfterPublishBeforeConfirmation(t *testing.T) {
	sqsClient := newSQSClient(t)
	eventQueueURL := queueURL(t, sqsClient, "wager-events.fifo")
	drainQueue(t, sqsClient, eventQueueURL)

	pool := newPool(t)
	eventID := uuid.NewString()
	transactionID := uuid.NewString()
	walletID := uuid.NewString()
	marker := "publish-before-confirmation-" + uuid.NewString()
	now := time.Now().UTC()
	payload, err := json.Marshal(map[string]any{
		"eventId":       eventID,
		"eventType":     "WagerTransactionProcessed",
		"aggregateId":   transactionID,
		"correlationId": marker,
		"occurredAt":    now.Format(time.RFC3339Nano),
		"version":       1,
		"data": map[string]any{
			"transactionId":         transactionID,
			"externalTransactionId": "recovery-" + uuid.NewString(),
			"providerId":            "provider-a",
			"walletId":              walletID,
			"playerId":              uuid.NewString(),
			"roundId":               "round-" + uuid.NewString(),
			"gameId":                "outbox-recovery-probe",
			"kind":                  "BET",
			"money":                 map[string]string{"amount": "1.00", "currency": "BRL"},
			"status":                "PROCESSED",
			"balance":               map[string]string{"amount": "99.00", "currency": "BRL"},
		},
	})
	if err != nil {
		t.Fatalf("marshal outbox recovery event: %v", err)
	}

	// The locked row is the durable state left by a publisher that has already
	// claimed the event. It is deliberately inserted with a live lease so one of
	// the running publishers cannot claim it before the simulated interruption.
	if _, err := pool.Exec(
		context.Background(),
		`INSERT INTO outbox_events(
			event_id, aggregate_id, event_type, payload, occurred_at, next_attempt_at, locked_by, locked_until
		) VALUES ($1,$2,$3,$4::jsonb,$5,$5,$6,$7)`,
		eventID,
		transactionID,
		"WagerTransactionProcessed",
		string(payload),
		now,
		"interrupted-publisher",
		now.Add(time.Hour),
	); err != nil {
		t.Fatalf("insert claimed outbox event: %v", err)
	}

	// This is the exact durable failure window: SendMessage succeeded, but the
	// process terminates before MarkOutboxPublished can commit.
	if _, err := sqsClient.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               aws.String(eventQueueURL),
		MessageBody:            aws.String(string(payload)),
		MessageGroupId:         aws.String(transactionID),
		MessageDeduplicationId: aws.String(eventID),
	}); err != nil {
		t.Fatalf("first outbox publish: %v", err)
	}
	var unpublished bool
	if err := pool.QueryRow(
		context.Background(),
		`SELECT published_at IS NULL FROM outbox_events WHERE event_id=$1`,
		eventID,
	).Scan(&unpublished); err != nil {
		t.Fatalf("read unconfirmed outbox event: %v", err)
	}
	if !unpublished {
		t.Fatal("outbox event was confirmed before the simulated interruption")
	}

	// Ending the lease models the publisher process disappearing. A normal
	// publisher must reclaim the same row, retry SendMessage with the same event
	// ID and confirm it; FIFO deduplication may collapse the duplicate delivery.
	leaseRelease, err := pool.Exec(
		context.Background(),
		`UPDATE outbox_events SET locked_until=now()-interval '1 second'
		 WHERE event_id=$1 AND locked_by='interrupted-publisher' AND published_at IS NULL`,
		eventID,
	)
	if err != nil {
		t.Fatalf("expire interrupted publisher lease: %v", err)
	}
	if leaseRelease.RowsAffected() != 1 {
		t.Fatalf("expired outbox leases = %d, want 1", leaseRelease.RowsAffected())
	}

	waitFor(t, 20*time.Second, func() (bool, error) {
		var published bool
		var storedPayload []byte
		err := pool.QueryRow(
			context.Background(),
			`SELECT published_at IS NOT NULL, payload FROM outbox_events WHERE event_id=$1`,
			eventID,
		).Scan(&published, &storedPayload)
		if err != nil || !published {
			return false, err
		}
		var stored, original any
		if err := json.Unmarshal(storedPayload, &stored); err != nil {
			return false, err
		}
		if err := json.Unmarshal(payload, &original); err != nil {
			return false, err
		}
		if !reflect.DeepEqual(stored, original) {
			return false, fmt.Errorf("recovered outbox payload changed")
		}
		return true, nil
	})

	waitFor(t, 20*time.Second, func() (bool, error) {
		output, err := sqsClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(eventQueueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			return false, err
		}
		for _, message := range output.Messages {
			if _, err := sqsClient.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(eventQueueURL),
				ReceiptHandle: message.ReceiptHandle,
			}); err != nil {
				return false, err
			}
			var envelope outboundEventEnvelope
			if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &envelope); err != nil {
				continue
			}
			if envelope.EventID != eventID {
				continue
			}
			if envelope.EventType != "WagerTransactionProcessed" || envelope.AggregateID != transactionID ||
				message.Attributes["MessageGroupId"] != transactionID ||
				message.Attributes["MessageDeduplicationId"] != eventID {
				return false, fmt.Errorf("recovered SQS event does not preserve routing identity")
			}
			return true, nil
		}
		return false, nil
	})
}

func newTestClient(t *testing.T) testClient {
	t.Helper()
	waitFor(t, 30*time.Second, func() (bool, error) {
		status, _, err := request(http.MethodGet, apiURLs[0]+"/health/ready", "", nil, nil)
		return status == http.StatusOK, err
	})
	return testClient{
		internalToken:  token(t, "wallet-service", "wallet-service-local-secret"),
		providerToken:  token(t, "provider-a", "provider-a-local-secret"),
		providerBToken: token(t, "provider-b", "provider-b-local-secret"),
		providerCToken: token(t, "provider-c", "provider-c-local-secret"),
	}
}

func token(t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	response, err := http.PostForm(keycloakURL+"/realms/gaming/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("request %s token: %v", clientID, err)
	}
	defer response.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s token: %v", clientID, err)
	}
	if response.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("%s token status = %d", clientID, response.StatusCode)
	}
	return body.AccessToken
}

func (c testClient) createWallet(t *testing.T, amount string) wallet {
	t.Helper()
	payload := map[string]any{
		"playerId": uuid.NewString(),
		"initialBalance": map[string]string{
			"amount":   amount,
			"currency": "BRL",
		},
	}
	status, body, err := request(http.MethodPost, apiURLs[0]+"/wallets", c.internalToken, payload, nil)
	if err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("create wallet status = %d, body = %s", status, body)
	}
	var result wallet
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	return result
}

func (c testClient) assertWallet(t *testing.T, baseURL, walletID, amount string, version int64) {
	t.Helper()
	status, body, err := request(http.MethodGet, baseURL+"/wallets/"+walletID, c.internalToken, nil, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("get wallet = status %d, error %v, body %s", status, err, body)
	}
	var result wallet
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode wallet: %v", err)
	}
	if result.Balance.Amount != amount || result.Balance.Currency != "BRL" || result.Version != version {
		t.Fatalf("wallet = balance %+v version %d, want %s BRL version %d", result.Balance, result.Version, amount, version)
	}
}

func (c testClient) assertLedgerEntries(t *testing.T, walletID string, count int) {
	t.Helper()
	status, body, err := request(
		http.MethodGet,
		apiURLs[0]+"/wallets/"+walletID+"/ledger?limit=100",
		c.internalToken,
		nil,
		nil,
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("get ledger = status %d, error %v, body %s", status, err, body)
	}
	var page ledgerPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	if len(page.Items) != count {
		t.Fatalf("ledger entries = %d, want %d", len(page.Items), count)
	}
}

func wagerPayload(
	wallet wallet,
	externalID, roundID, kind, amount, referenceExternalID string,
) map[string]any {
	return wagerPayloadForProvider("provider-a", wallet, externalID, roundID, kind, amount, referenceExternalID)
}

func wagerPayloadForProvider(
	providerID string,
	wallet wallet,
	externalID, roundID, kind, amount, referenceExternalID string,
) map[string]any {
	payload := map[string]any{
		"providerId":            providerID,
		"externalTransactionId": externalID,
		"playerId":              wallet.PlayerID,
		"walletId":              wallet.ID,
		"roundId":               roundID,
		"gameId":                "fortune-chimp",
		"kind":                  kind,
		"money": map[string]string{
			"amount":   amount,
			"currency": "BRL",
		},
	}
	if referenceExternalID != "" {
		payload["referenceExternalTransactionId"] = referenceExternalID
	}
	return payload
}

func request(
	method, endpoint, bearerToken string,
	body any,
	headers map[string]string,
) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	return response.StatusCode, responseBody, err
}

func assertProblemCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var problem problemResponse
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem response: %v, body %s", err, body)
	}
	if problem.Error.Code != want {
		t.Fatalf("problem code = %q, want %q (body %s)", problem.Error.Code, want, body)
	}
}

func newSQSClient(t *testing.T) *sqs.Client {
	t.Helper()
	configuration, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	return sqs.NewFromConfig(configuration, func(options *sqs.Options) {
		options.BaseEndpoint = aws.String(sqsEndpoint)
	})
}

func queueURL(t *testing.T, client *sqs.Client, name string) string {
	t.Helper()
	output, err := client.GetQueueUrl(context.Background(), &sqs.GetQueueUrlInput{
		QueueName: aws.String(name),
	})
	if err != nil {
		t.Fatalf("get queue %s: %v", name, err)
	}
	return aws.ToString(output.QueueUrl)
}

func drainQueue(t *testing.T, client *sqs.Client, url string) {
	t.Helper()
	for {
		output, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(url),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             0,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("drain queue %s: %v", url, err)
		}
		if len(output.Messages) == 0 {
			return
		}
		for _, message := range output.Messages {
			if _, err := client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(url),
				ReceiptHandle: message.ReceiptHandle,
			}); err != nil {
				t.Fatalf("delete drained message from %s: %v", url, err)
			}
		}
	}
}

func receiveOutboundEvents(
	t *testing.T,
	client *sqs.Client,
	url string,
	want map[string]expectedOutboundEvent,
) map[string]receivedOutboundEvent {
	t.Helper()
	got := make(map[string]receivedOutboundEvent, len(want))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && len(got) < len(want) {
		output, err := client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(url),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			t.Fatalf("receive outbound events: %v", err)
		}
		for _, message := range output.Messages {
			received, key, matched := decodeOutboundEvent(message, want)
			if _, err := client.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{
				QueueUrl:      aws.String(url),
				ReceiptHandle: message.ReceiptHandle,
			}); err != nil {
				t.Fatalf("delete outbound message: %v", err)
			}
			if !matched {
				continue
			}
			if previous, exists := got[key]; exists {
				if previous.Envelope.EventID != received.Envelope.EventID {
					t.Fatalf("multiple distinct events matched %s: %s and %s", key, previous.Envelope.EventID, received.Envelope.EventID)
				}
				continue // At-least-once delivery can repeat the same eventId.
			}
			got[key] = received
		}
	}
	if len(got) != len(want) {
		missing := make([]string, 0, len(want)-len(got))
		for key := range want {
			if _, found := got[key]; !found {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		t.Fatalf("did not receive expected outbound events within 30s: %s", strings.Join(missing, ", "))
	}
	return got
}

func decodeOutboundEvent(
	message types.Message,
	want map[string]expectedOutboundEvent,
) (receivedOutboundEvent, string, bool) {
	var envelope outboundEventEnvelope
	if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &envelope); err != nil {
		return receivedOutboundEvent{}, "", false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &fields); err != nil {
		return receivedOutboundEvent{}, "", false
	}
	transactionID, ok := jsonString(fields, "transactionId")
	if !ok {
		return receivedOutboundEvent{}, "", false
	}
	key := envelope.EventType + ":" + transactionID
	if _, ok := want[key]; !ok {
		return receivedOutboundEvent{}, key, false
	}
	return receivedOutboundEvent{Envelope: envelope, Fields: fields, Attrs: message.Attributes}, key, true
}

func assertOutboundEventContract(t *testing.T, got receivedOutboundEvent, want expectedOutboundEvent) {
	t.Helper()
	if _, err := uuid.Parse(got.Envelope.EventID); err != nil {
		t.Fatalf("%s eventId = %q, want UUID: %v", want.EventType, got.Envelope.EventID, err)
	}
	if got.Envelope.EventType != want.EventType || got.Envelope.AggregateID != want.AggregateID ||
		got.Envelope.CorrelationID != want.CorrelationID || got.Envelope.Version != 1 {
		t.Fatalf(
			"event envelope = type=%q aggregate=%q correlation=%q version=%d, want type=%q aggregate=%q correlation=%q version=1",
			got.Envelope.EventType,
			got.Envelope.AggregateID,
			got.Envelope.CorrelationID,
			got.Envelope.Version,
			want.EventType,
			want.AggregateID,
			want.CorrelationID,
		)
	}
	if (got.Envelope.CausationID == nil) != (want.CausationID == nil) ||
		got.Envelope.CausationID != nil && *got.Envelope.CausationID != *want.CausationID {
		t.Fatalf("%s causationId = %v, want %v", want.EventType, got.Envelope.CausationID, want.CausationID)
	}
	if !strings.HasSuffix(got.Envelope.OccurredAt, "Z") {
		t.Fatalf("%s occurredAt = %q, want UTC RFC3339Nano", want.EventType, got.Envelope.OccurredAt)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.Envelope.OccurredAt); err != nil {
		t.Fatalf("%s occurredAt = %q: %v", want.EventType, got.Envelope.OccurredAt, err)
	}
	if got.Attrs["MessageGroupId"] != want.AggregateID || got.Attrs["MessageDeduplicationId"] != got.Envelope.EventID {
		t.Fatalf(
			"%s SQS attributes = group %q dedup %q, want group %q dedup %q",
			want.EventType,
			got.Attrs["MessageGroupId"],
			got.Attrs["MessageDeduplicationId"],
			want.AggregateID,
			got.Envelope.EventID,
		)
	}
	assertFieldNames(t, want.EventType+" data", got.Fields, want.DataFields)
	for field, wantValue := range want.DataValues {
		gotValue, exists := got.Fields[field]
		if !exists {
			t.Fatalf("%s data is missing %s", want.EventType, field)
		}
		expectedJSON, err := json.Marshal(wantValue)
		if err != nil {
			t.Fatalf("marshal expected %s data.%s: %v", want.EventType, field, err)
		}
		var actual, expected any
		if err := json.Unmarshal(gotValue, &actual); err != nil {
			t.Fatalf("decode %s data.%s: %v", want.EventType, field, err)
		}
		if err := json.Unmarshal(expectedJSON, &expected); err != nil {
			t.Fatalf("decode expected %s data.%s: %v", want.EventType, field, err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s data.%s = %#v, want %#v", want.EventType, field, actual, expected)
		}
	}
	if want.TimeDataField != "" {
		value, exists := jsonString(got.Fields, want.TimeDataField)
		if !exists || !strings.HasSuffix(value, "Z") {
			t.Fatalf("%s data.%s = %q, want UTC RFC3339Nano", want.EventType, want.TimeDataField, value)
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			t.Fatalf("%s data.%s = %q: %v", want.EventType, want.TimeDataField, value, err)
		}
	}
}

func assertFieldNames(t *testing.T, label string, fields map[string]json.RawMessage, want []string) {
	t.Helper()
	gotNames := make([]string, 0, len(fields))
	for name := range fields {
		gotNames = append(gotNames, name)
	}
	wantNames := append([]string(nil), want...)
	sort.Strings(gotNames)
	sort.Strings(wantNames)
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("%s fields = %v, want %v", label, gotNames, wantNames)
	}
}

func jsonString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, exists := fields[name]
	if !exists {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

type contractValidator struct{}

func (contractValidator) Struct(any) error      { return nil }
func (contractValidator) Var(any, string) error { return nil }

type contractMetrics struct{}

func (contractMetrics) ObserveWager(string, bool, time.Duration) {}
func (contractMetrics) IncConcurrencyConflict()                  {}
func (contractMetrics) IncSQSRetry(string)                       {}
func (contractMetrics) IncSQSDLQHandoff()                        {}
func (contractMetrics) ObserveOutboxLag(time.Duration)           {}
func (contractMetrics) IncReconciliationDivergence()             {}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), postgresURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func waitFor(t *testing.T, timeout time.Duration, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		done, err := condition()
		if done {
			return
		}
		if err != nil && !strings.Contains(err.Error(), "no rows") {
			lastErr = err
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("condition was not met within %v; last error: %v", timeout, lastErr)
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

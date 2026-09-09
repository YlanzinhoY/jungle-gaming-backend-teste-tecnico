package messaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/application"
	"github.com/enzom/jungle-gaming/internal/config"
	"github.com/enzom/jungle-gaming/internal/domain"
)

const consumerName = "wager-transaction-consumer-v1"

type wagerEnvelope struct {
	MessageID  string    `json:"messageId" validate:"notblank,max=256"`
	Type       string    `json:"type" validate:"eq=WagerTransactionRequested"`
	OccurredAt time.Time `json:"occurredAt" validate:"required"`
	Data       struct {
		ProviderID            string `json:"providerId"`
		ExternalTransactionID string `json:"externalTransactionId"`
		IdempotencyKey        string `json:"idempotencyKey"`
		PlayerID              string `json:"playerId"`
		WalletID              string `json:"walletId"`
		RoundID               string `json:"roundId"`
		GameID                string `json:"gameId"`
		Kind                  string `json:"kind"`
		Money                 struct {
			Amount   string `json:"amount" validate:"required"`
			Currency string `json:"currency" validate:"required,iso4217"`
		} `json:"money"`
		ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
	} `json:"data"`
}

func RegisterConsumer(
	lifecycle fx.Lifecycle,
	queues *Queues,
	service *application.WagerService,
	validate application.Validator,
	metrics application.Metrics,
	cfg config.Config,
	log *slog.Logger,
) {
	if !cfg.Workers.Enabled {
		return
	}
	var cancel context.CancelFunc
	var workers sync.WaitGroup
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, stop := context.WithCancel(context.Background())
			cancel = stop
			for index := 0; index < cfg.Workers.ConsumerConcurrency; index++ {
				workers.Add(1)
				go func(worker int) {
					defer workers.Done()
					consume(ctx, worker, queues, service, validate, metrics, cfg, log)
				}(index)
			}
			return nil
		},
		OnStop: func(ctx context.Context) error {
			if cancel != nil {
				cancel()
			}
			done := make(chan struct{})
			go func() {
				workers.Wait()
				close(done)
			}()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
}

func consume(
	ctx context.Context,
	worker int,
	queues *Queues,
	service *application.WagerService,
	validate application.Validator,
	metrics application.Metrics,
	cfg config.Config,
	log *slog.Logger,
) {
	for ctx.Err() == nil {
		output, err := queues.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(queues.InputURL()),
			MaxNumberOfMessages:         1,
			WaitTimeSeconds:             20,
			VisibilityTimeout:           cfg.AWS.Visibility,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
			MessageAttributeNames:       []string{"traceparent", "tracestate", "baggage"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.ErrorContext(ctx, "SQS receive failed", "worker", worker, "error", err)
			waitContext(ctx, time.Second)
			continue
		}
		for _, message := range output.Messages {
			handleMessage(ctx, queues, service, validate, metrics, cfg, message, log)
		}
	}
}

func handleMessage(
	ctx context.Context,
	queues *Queues,
	service *application.WagerService,
	validate application.Validator,
	metrics application.Metrics,
	cfg config.Config,
	message types.Message,
	log *slog.Logger,
) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, sqsMessageCarrier(message.MessageAttributes))
	ctx, span := otel.Tracer("github.com/enzom/jungle-gaming/messaging").Start(
		ctx,
		"SQS process WagerTransactionRequested",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "aws_sqs"),
			attribute.String("messaging.operation.name", "process"),
			attribute.String("messaging.message.id", aws.ToString(message.MessageId)),
		),
	)
	defer span.End()

	receiveCount := approximateReceiveCount(message)
	body := aws.ToString(message.Body)
	var envelope wagerEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid message envelope")
		log.ErrorContext(
			ctx,
			"invalid SQS wager envelope; leaving for redrive",
			"sqsMessageId", aws.ToString(message.MessageId),
			"error", err,
		)
		scheduleRetry(queues, metrics, cfg, message, receiveCount, true, log)
		return
	}
	if err := validate.Struct(envelope); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid message envelope")
		log.ErrorContext(
			ctx,
			"invalid SQS wager envelope; leaving for redrive",
			"sqsMessageId", aws.ToString(message.MessageId),
			"error", err,
		)
		scheduleRetry(queues, metrics, cfg, message, receiveCount, true, log)
		return
	}
	money, err := domain.ParseMoney(envelope.Data.Money.Amount, envelope.Data.Money.Currency)
	span.SetAttributes(
		attribute.String("app.message_id", envelope.MessageID),
		attribute.String("app.provider_id", envelope.Data.ProviderID),
		attribute.String("app.wallet_id", envelope.Data.WalletID),
	)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid money")
		log.ErrorContext(
			ctx,
			"invalid SQS money; leaving for redrive",
			"messageId", envelope.MessageID,
			"error", err,
		)
		scheduleRetry(queues, metrics, cfg, message, receiveCount, true, log)
		return
	}
	hash := sha256.Sum256([]byte(body))
	stopHeartbeat := maintainVisibility(ctx, queues, cfg.AWS.Visibility, message, log)
	result, err := service.Process(ctx, application.ProcessWagerCommand{
		ProviderID:                     envelope.Data.ProviderID,
		ExternalTransactionID:          envelope.Data.ExternalTransactionID,
		IdempotencyKey:                 envelope.Data.IdempotencyKey,
		PlayerID:                       envelope.Data.PlayerID,
		WalletID:                       envelope.Data.WalletID,
		RoundID:                        envelope.Data.RoundID,
		GameID:                         envelope.Data.GameID,
		Kind:                           domain.TransactionKind(envelope.Data.Kind),
		Money:                          money,
		ReferenceExternalTransactionID: envelope.Data.ReferenceExternalTransactionID,
		CorrelationID:                  envelope.MessageID,
		Inbox: &application.InboxInput{
			ConsumerName: consumerName,
			MessageID:    envelope.MessageID,
			PayloadHash:  hex.EncodeToString(hash[:]),
		},
	})
	stopHeartbeat()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "wager processing failed")
		permanent := errors.Is(err, application.ErrInboxPayloadConflict) ||
			errors.Is(err, application.ErrIdempotencyPayloadConflict) ||
			errors.Is(err, application.ErrExternalTransactionConflict) ||
			errors.Is(err, domain.ErrInvalidTransaction) ||
			errors.Is(err, domain.ErrInvalidMoney)
		log.ErrorContext(
			ctx,
			"SQS wager processing failed",
			"messageId", envelope.MessageID,
			"permanent", permanent,
			"error", err,
		)
		if ctx.Err() != nil {
			releaseMessage(queues, message, log)
			return
		}
		scheduleRetry(queues, metrics, cfg, message, receiveCount, permanent, log)
		return
	}
	span.SetAttributes(attribute.String("app.transaction_id", result.TransactionID))
	if cfg.Workers.ConsumerPostCommitDelay > 0 {
		log.InfoContext(
			ctx,
			"SQS consumer post-commit delay active",
			"messageId", envelope.MessageID,
			"transactionId", result.TransactionID,
			"delay", cfg.Workers.ConsumerPostCommitDelay.String(),
		)
		waitContext(ctx, cfg.Workers.ConsumerPostCommitDelay)
		if ctx.Err() != nil {
			return
		}
	}
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := queues.client.DeleteMessage(deleteCtx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queues.InputURL()),
		ReceiptHandle: message.ReceiptHandle,
	}); err != nil {
		log.ErrorContext(
			ctx,
			"SQS delete after durable handling failed",
			"messageId", envelope.MessageID,
			"transactionId", result.TransactionID,
			"error", err,
		)
	}
}

func approximateReceiveCount(message types.Message) int {
	value := message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]
	count, err := strconv.Atoi(value)
	if err != nil || count < 1 {
		return 1
	}
	return count
}

func scheduleRetry(
	queues *Queues,
	metrics application.Metrics,
	cfg config.Config,
	message types.Message,
	receiveCount int,
	permanent bool,
	log *slog.Logger,
) {
	reason := "transient"
	if permanent {
		reason = "permanent"
	}
	metrics.IncSQSRetry(reason)
	if receiveCount >= cfg.AWS.MaxReceives {
		metrics.IncSQSDLQHandoff()
	}

	delay := retryDelay(cfg.Workers.ConsumerRetryBase, cfg.Workers.ConsumerRetryMax, receiveCount)
	if permanent || receiveCount >= cfg.AWS.MaxReceives {
		delay = 0
	}
	seconds := int32((delay + time.Second - 1) / time.Second)
	changeVisibility(queues, message, seconds, log)
	log.Warn(
		"SQS message scheduled for retry",
		"sqsMessageId", aws.ToString(message.MessageId),
		"receiveCount", receiveCount,
		"visibilitySeconds", seconds,
		"permanent", permanent,
		"eligibleForDLQ", receiveCount >= cfg.AWS.MaxReceives,
	)
}

func retryDelay(base, maximum time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for index := 1; index < attempt && delay < maximum; index++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func maintainVisibility(
	parent context.Context,
	queues *Queues,
	visibilitySeconds int32,
	message types.Message,
	log *slog.Logger,
) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	interval := time.Duration(visibilitySeconds) * time.Second / 2
	if interval < time.Second {
		interval = time.Second
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				changeVisibility(queues, message, visibilitySeconds, log)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func releaseMessage(queues *Queues, message types.Message, log *slog.Logger) {
	changeVisibility(queues, message, 0, log)
}

func changeVisibility(queues *Queues, message types.Message, seconds int32, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := queues.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queues.InputURL()), ReceiptHandle: message.ReceiptHandle, VisibilityTimeout: seconds,
	}); err != nil {
		log.Error(
			"change SQS message visibility failed",
			"sqsMessageId", aws.ToString(message.MessageId),
			"visibilitySeconds", seconds,
			"error", err,
		)
	}
}

func waitContext(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

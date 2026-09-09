package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.uber.org/fx"

	"github.com/enzom/jungle-gaming/internal/config"
	"github.com/enzom/jungle-gaming/internal/infrastructure/observability"
)

type Queues struct {
	client                                       *sqs.Client
	cfg                                          config.AWS
	log                                          *slog.Logger
	mu                                           sync.RWMutex
	inputURL, inputDLQURL, eventURL, eventDLQURL string
}

func NewSQSClient(cfg config.Config, tracing *observability.Tracing) (*sqs.Client, error) {
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.AWS.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AWS.AccessKey, cfg.AWS.SecretKey, "")),
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	otelaws.AppendMiddlewares(&awsCfg.APIOptions, otelaws.WithTracerProvider(tracing.TracerProvider()))
	return sqs.NewFromConfig(awsCfg, func(options *sqs.Options) {
		if cfg.AWS.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.AWS.Endpoint)
		}
	}), nil
}

func NewQueues(lifecycle fx.Lifecycle, client *sqs.Client, cfg config.Config, log *slog.Logger) *Queues {
	queues := &Queues{client: client, cfg: cfg.AWS, log: log}
	lifecycle.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		if err := queues.prepare(ctx); err != nil {
			return fmt.Errorf("prepare SQS queues: %w", err)
		}
		log.InfoContext(
			ctx,
			"SQS queues ready",
			"inputQueue", cfg.AWS.InputQueue,
			"eventQueue", cfg.AWS.EventQueue,
			"endpoint", cfg.AWS.Endpoint,
		)
		return nil
	}})
	return queues
}

func (q *Queues) prepare(ctx context.Context) error {
	inputDLQURL, inputDLQARN, err := q.ensureQueue(ctx, q.cfg.InputDLQ, "")
	if err != nil {
		return err
	}
	inputURL, _, err := q.ensureQueue(ctx, q.cfg.InputQueue, inputDLQARN)
	if err != nil {
		return err
	}
	eventDLQURL, eventDLQARN, err := q.ensureQueue(ctx, q.cfg.EventDLQ, "")
	if err != nil {
		return err
	}
	eventURL, _, err := q.ensureQueue(ctx, q.cfg.EventQueue, eventDLQARN)
	if err != nil {
		return err
	}
	q.mu.Lock()
	q.inputURL, q.inputDLQURL, q.eventURL, q.eventDLQURL = inputURL, inputDLQURL, eventURL, eventDLQURL
	q.mu.Unlock()
	return nil
}

func (q *Queues) ensureQueue(ctx context.Context, name, deadLetterARN string) (string, string, error) {
	urlOutput, err := q.client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		var missing *types.QueueDoesNotExist
		if !q.cfg.CreateQueues || !errors.As(err, &missing) {
			return "", "", err
		}
		createAttributes := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}
		created, createErr := q.client.CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName:  aws.String(name),
			Attributes: createAttributes,
		})
		if createErr != nil {
			return "", "", createErr
		}
		if created.QueueUrl == nil {
			return "", "", fmt.Errorf("SQS create queue %q returned no URL", name)
		}
		urlOutput = &sqs.GetQueueUrlOutput{QueueUrl: created.QueueUrl}
	}
	if urlOutput.QueueUrl == nil {
		return "", "", fmt.Errorf("SQS queue %q returned no URL", name)
	}
	if q.cfg.CreateQueues {
		desiredAttributes := map[string]string{"VisibilityTimeout": fmt.Sprint(q.cfg.Visibility)}
		if deadLetterARN != "" {
			policy, marshalErr := json.Marshal(map[string]string{
				"deadLetterTargetArn": deadLetterARN,
				"maxReceiveCount":     fmt.Sprint(q.cfg.MaxReceives),
			})
			if marshalErr != nil {
				return "", "", marshalErr
			}
			desiredAttributes["RedrivePolicy"] = string(policy)
		}
		if _, err := q.client.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
			QueueUrl:   urlOutput.QueueUrl,
			Attributes: desiredAttributes,
		}); err != nil {
			return "", "", err
		}
	}
	attributes, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: urlOutput.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return "", "", err
	}
	return aws.ToString(urlOutput.QueueUrl), attributes.Attributes[string(types.QueueAttributeNameQueueArn)], nil
}

func (q *Queues) Check(ctx context.Context) error {
	if err := q.CheckInputQueue(ctx); err != nil {
		return err
	}
	return q.CheckEventQueue(ctx)
}

// CheckInputQueue verifies the queue that receives external wager operations.
func (q *Queues) CheckInputQueue(ctx context.Context) error {
	q.mu.RLock()
	inputURL := q.inputURL
	q.mu.RUnlock()
	return q.checkQueue(ctx, "input", inputURL)
}

// CheckEventQueue verifies the queue that receives published domain events.
func (q *Queues) CheckEventQueue(ctx context.Context) error {
	q.mu.RLock()
	eventURL := q.eventURL
	q.mu.RUnlock()
	return q.checkQueue(ctx, "event", eventURL)
}

func (q *Queues) checkQueue(ctx context.Context, name, queueURL string) error {
	if queueURL == "" {
		return fmt.Errorf("%s queue URL is not initialized", name)
	}
	if _, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}); err != nil {
		return fmt.Errorf("check %s queue: %w", name, err)
	}
	return nil
}

func (q *Queues) InputURL() string { q.mu.RLock(); defer q.mu.RUnlock(); return q.inputURL }
func (q *Queues) EventURL() string { q.mu.RLock(); defer q.mu.RUnlock(); return q.eventURL }

func (q *Queues) publish(ctx context.Context, eventID, aggregateID string, payload []byte) error {
	messageAttributes := make(map[string]types.MessageAttributeValue)
	otel.GetTextMapPropagator().Inject(ctx, sqsMessageCarrier(messageAttributes))
	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl: aws.String(q.EventURL()), MessageBody: aws.String(string(payload)),
		MessageGroupId: aws.String(aggregateID), MessageDeduplicationId: aws.String(eventID),
		MessageAttributes: messageAttributes,
	})
	return err
}

type sqsMessageCarrier map[string]types.MessageAttributeValue

var _ propagation.TextMapCarrier = sqsMessageCarrier{}

func (c sqsMessageCarrier) Get(key string) string {
	attributeValue, ok := c[key]
	if !ok || attributeValue.StringValue == nil {
		return ""
	}
	return aws.ToString(attributeValue.StringValue)
}

func (c sqsMessageCarrier) Set(key, value string) {
	c[key] = types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(value)}
}

func (c sqsMessageCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

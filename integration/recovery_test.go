//go:build integration && hostrecovery

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

func TestConsumerRedeliversAfterCommitBeforeDelete(t *testing.T) {
	client := newTestClient(t)
	sqsClient := newSQSClient(t)
	pool := newPool(t)

	compose(t, nil, "stop", "api-1", "api-2", "api-3")
	t.Cleanup(func() {
		if err := composeCommand(nil, "up", "-d", "--force-recreate", "api-1", "api-2", "api-3"); err != nil {
			t.Errorf("restore API instances: %v", err)
			return
		}
		for _, apiURL := range apiURLs {
			waitForAPI(t, apiURL)
		}
	})
	compose(t, []string{"SQS_CONSUMER_POST_COMMIT_DELAY=15s"}, "up", "--build", "-d", "--force-recreate", "api-1")
	waitForAPI(t, apiURLs[0])
	time.Sleep(time.Second)

	wallet := client.createWallet(t, "100.00")
	externalID := "post-commit-" + uuid.NewString()
	messageID := "post-commit-message-" + uuid.NewString()
	sendWagerMessage(t, sqsClient, wallet, externalID, messageID)

	waitFor(t, 60*time.Second, func() (bool, error) {
		var completed bool
		err := pool.QueryRow(
			context.Background(),
			`SELECT completed_at IS NOT NULL FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`,
			"wager-transaction-consumer-v1",
			messageID,
		).Scan(&completed)
		return completed, err
	})

	compose(t, nil, "kill", "api-1")
	receivedAgain := false
	waitFor(t, 45*time.Second, func() (bool, error) {
		output, err := sqsClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(queueURL(t, sqsClient, "wager-transactions.fifo")),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			VisibilityTimeout:           1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			return false, err
		}
		for _, message := range output.Messages {
			if !containsMessageID(aws.ToString(message.Body), messageID) {
				continue
			}
			receiveCount, countErr := strconv.Atoi(message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
			if countErr != nil || receiveCount < 2 {
				return false, fmt.Errorf("message was received %d times, want at least 2", receiveCount)
			}
			_, err = sqsClient.ChangeMessageVisibility(context.Background(), &sqs.ChangeMessageVisibilityInput{
				QueueUrl:          aws.String(queueURL(t, sqsClient, "wager-transactions.fifo")),
				ReceiptHandle:     message.ReceiptHandle,
				VisibilityTimeout: 0,
			})
			receivedAgain = err == nil
			return receivedAgain, err
		}
		return false, nil
	})
	if !receivedAgain {
		t.Fatal("message was not redelivered")
	}

	compose(t, nil, "up", "-d", "--force-recreate", "api-1", "api-2", "api-3")
	for _, apiURL := range apiURLs {
		waitForAPI(t, apiURL)
	}
	waitFor(t, 20*time.Second, func() (bool, error) {
		var count int
		err := pool.QueryRow(
			context.Background(),
			`SELECT count(*) FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2 AND completed_at IS NOT NULL`,
			"wager-transaction-consumer-v1",
			messageID,
		).Scan(&count)
		return count == 1, err
	})
	client.assertWallet(t, apiURLs[2], wallet.ID, "90.00", 2)
	client.assertLedgerEntries(t, wallet.ID, 2)
}

func TestRestartPreservesIdempotencyAndPendingReference(t *testing.T) {
	client := newTestClient(t)

	idempotencyWallet := client.createWallet(t, "100.00")
	idempotencyExternalID := "restart-idempotency-" + uuid.NewString()
	idempotencyKey := "provider-a:" + idempotencyExternalID
	idempotencyPayload := wagerPayload(
		idempotencyWallet,
		idempotencyExternalID,
		"round-"+uuid.NewString(),
		"BET",
		"10.00",
		"",
	)
	status, body, err := request(
		http.MethodPost,
		apiURLs[0]+"/wagering/transactions",
		client.providerToken,
		idempotencyPayload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("initial wager = status %d, error %v, body %s", status, err, body)
	}

	pendingWallet := client.createWallet(t, "100.00")
	roundID := "round-" + uuid.NewString()
	referenceExternalID := "restart-reference-" + uuid.NewString()
	pendingExternalID := "restart-refund-" + uuid.NewString()
	status, body, err = request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(pendingWallet, pendingExternalID, roundID, "REFUND", "25.00", referenceExternalID),
		map[string]string{"Idempotency-Key": "provider-a:" + pendingExternalID},
	)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("pending refund = status %d, error %v, body %s", status, err, body)
	}

	compose(t, nil, "restart", "api-1", "api-2", "api-3")
	for _, apiURL := range apiURLs {
		waitForAPI(t, apiURL)
	}

	status, body, err = request(
		http.MethodPost,
		apiURLs[2]+"/wagering/transactions",
		client.providerToken,
		idempotencyPayload,
		map[string]string{"Idempotency-Key": idempotencyKey},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("idempotent replay after restart = status %d, error %v, body %s", status, err, body)
	}
	var replay wagerResult
	if err := json.Unmarshal(body, &replay); err != nil || !replay.IdempotentReplay {
		t.Fatalf("idempotent replay after restart = %+v, error %v", replay, err)
	}
	client.assertWallet(t, apiURLs[0], idempotencyWallet.ID, "90.00", 2)
	client.assertLedgerEntries(t, idempotencyWallet.ID, 2)

	status, body, err = request(
		http.MethodGet,
		apiURLs[0]+"/providers/provider-a/wagering/transactions/"+pendingExternalID,
		client.providerToken,
		nil,
		nil,
	)
	if err != nil || status != http.StatusOK || !containsStatus(body, "PENDING_REFERENCE") {
		t.Fatalf("pending reference after restart = status %d, error %v, body %s", status, err, body)
	}

	status, body, err = request(
		http.MethodPost,
		apiURLs[1]+"/wagering/transactions",
		client.providerToken,
		wagerPayload(pendingWallet, referenceExternalID, roundID, "BET", "25.00", ""),
		map[string]string{"Idempotency-Key": "provider-a:" + referenceExternalID},
	)
	if err != nil || status != http.StatusOK {
		t.Fatalf("reference after restart = status %d, error %v, body %s", status, err, body)
	}

	waitFor(t, 20*time.Second, func() (bool, error) {
		status, body, err := request(
			http.MethodGet,
			apiURLs[2]+"/providers/provider-a/wagering/transactions/"+pendingExternalID,
			client.providerToken,
			nil,
			nil,
		)
		return err == nil && status == http.StatusOK && containsStatus(body, "PROCESSED"), err
	})
	client.assertWallet(t, apiURLs[2], pendingWallet.ID, "100.00", 3)
	client.assertLedgerEntries(t, pendingWallet.ID, 3)
}

func sendWagerMessage(t *testing.T, client *sqs.Client, currentWallet wallet, externalID, messageID string) {
	t.Helper()
	payload := wagerPayload(currentWallet, externalID, "round-"+uuid.NewString(), "BET", "10.00", "")
	envelope := map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"providerId":            payload["providerId"],
			"externalTransactionId": payload["externalTransactionId"],
			"idempotencyKey":        "provider-a:" + externalID,
			"playerId":              payload["playerId"],
			"walletId":              payload["walletId"],
			"roundId":               payload["roundId"],
			"gameId":                payload["gameId"],
			"kind":                  payload["kind"],
			"money":                 payload["money"],
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal SQS wager envelope: %v", err)
	}
	_, err = client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL(t, client, "wager-transactions.fifo")),
		MessageBody:            aws.String(string(encoded)),
		MessageGroupId:         aws.String(currentWallet.ID),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatalf("send SQS wager message: %v", err)
	}
}

func compose(t *testing.T, extraEnvironment []string, arguments ...string) {
	t.Helper()
	if err := composeCommand(extraEnvironment, arguments...); err != nil {
		t.Fatalf("docker compose %v: %v", arguments, err)
	}
}

func composeCommand(extraEnvironment []string, arguments ...string) error {
	command := exec.Command("docker", append([]string{"compose"}, arguments...)...)
	command.Dir = filepath.Join("..")
	command.Env = append(os.Environ(), extraEnvironment...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func waitForAPI(t *testing.T, apiURL string) {
	t.Helper()
	waitFor(t, 60*time.Second, func() (bool, error) {
		status, _, err := request(http.MethodGet, apiURL+"/health/ready", "", nil, nil)
		return status == http.StatusOK, err
	})
}

func containsMessageID(body, messageID string) bool {
	return stringContains(body, `"messageId":"`+messageID+`"`)
}

func containsStatus(body []byte, status string) bool {
	return stringContains(string(body), `"status":"`+status+`"`)
}

func stringContains(value, expected string) bool {
	return len(expected) == 0 || (len(value) >= len(expected) && contains(value, expected))
}

func contains(value, expected string) bool {
	for index := 0; index+len(expected) <= len(value); index++ {
		if value[index:index+len(expected)] == expected {
			return true
		}
	}
	return false
}

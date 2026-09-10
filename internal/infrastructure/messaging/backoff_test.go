package messaging

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestNormalizeQueueURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		queueURL string
		endpoint string
		want     string
		wantErr  bool
	}{
		{
			name:     "uses broker endpoint while preserving queue path",
			queueURL: "http://localhost:4566/000000000000/wager-events.fifo",
			endpoint: "http://ministack:4566",
			want:     "http://ministack:4566/000000000000/wager-events.fifo",
		},
		{
			name:     "does not alter URL without configured endpoint",
			queueURL: "https://sqs.example.test/queue",
			want:     "https://sqs.example.test/queue",
		},
		{
			name:     "rejects relative queue URL",
			queueURL: "/000000000000/wager-events.fifo",
			endpoint: "http://ministack:4566",
			wantErr:  true,
		},
		{
			name:     "rejects relative endpoint",
			queueURL: "http://localhost:4566/000000000000/wager-events.fifo",
			endpoint: "/sqs",
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeQueueURL(test.queueURL, test.endpoint)
			if test.wantErr {
				if err == nil {
					t.Fatal("normalizeQueueURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeQueueURL() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeQueueURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: time.Second},
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 5 * time.Second},
		{attempt: 20, want: 5 * time.Second},
	}
	for _, test := range tests {
		if got := retryDelay(time.Second, 5*time.Second, test.attempt); got != test.want {
			t.Errorf("retryDelay(%d) = %v, want %v", test.attempt, got, test.want)
		}
	}
}

func messageWithReceiveCount(value string) types.Message {
	return types.Message{Attributes: map[string]string{
		string(types.MessageSystemAttributeNameApproximateReceiveCount): value,
	}}
}

func TestApproximateReceiveCount(t *testing.T) {
	t.Parallel()

	if got := approximateReceiveCount(messageWithReceiveCount("3")); got != 3 {
		t.Fatalf("approximateReceiveCount() = %d, want 3", got)
	}
	if got := approximateReceiveCount(messageWithReceiveCount("invalid")); got != 1 {
		t.Fatalf("approximateReceiveCount() fallback = %d, want 1", got)
	}
}

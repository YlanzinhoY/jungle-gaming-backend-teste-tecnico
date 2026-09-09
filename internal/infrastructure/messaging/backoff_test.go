package messaging

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

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

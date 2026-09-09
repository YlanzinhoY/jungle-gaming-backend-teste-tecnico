package application

import (
	"testing"
	"time"
)

func TestLedgerCursorRoundTrip(t *testing.T) {
	t.Parallel()

	want := LedgerCursor{
		CreatedAt: time.Date(2026, time.September, 9, 12, 30, 0, 123, time.UTC),
		ID:        "0192f291-27dd-7d3f-8071-5f8685deef37",
	}
	got, err := decodeLedgerCursor(encodeLedgerCursor(want))
	if err != nil {
		t.Fatalf("decodeLedgerCursor() error = %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("cursor = %+v, want %+v", got, want)
	}
	if _, err := decodeLedgerCursor("not-a-cursor"); err == nil {
		t.Fatal("decodeLedgerCursor() accepted malformed cursor")
	}
}

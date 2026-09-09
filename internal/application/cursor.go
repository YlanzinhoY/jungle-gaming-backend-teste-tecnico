package application

import (
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

func encodeLedgerCursor(cursor LedgerCursor) string {
	raw := cursor.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + cursor.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeLedgerCursor(value string) (*LedgerCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || parts[1] == "" {
		return nil, errors.New("malformed cursor")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return nil, errors.New("cursor contains an invalid identifier")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}
	return &LedgerCursor{CreatedAt: createdAt, ID: parts[1]}, nil
}

package domain

import (
	"encoding/json"
	"math"
	"testing"
)

func FuzzParseMoneyRoundTrip(f *testing.F) {
	for _, seed := range []struct {
		amount   string
		currency string
	}{
		{amount: "0.00", currency: "BRL"},
		{amount: "25.00", currency: "BRL"},
		{amount: "92233720368547758.07", currency: "USD"},
		{amount: "92233720368547758.08", currency: "USD"},
		{amount: "-1.00", currency: "BRL"},
		{amount: "NaN", currency: "BRL"},
		{amount: "1e2", currency: "BRL"},
		{amount: "1.000", currency: "BRL"},
		{amount: "1.00", currency: "brl"},
	} {
		f.Add(seed.amount, seed.currency)
	}

	f.Fuzz(func(t *testing.T, amount, currency string) {
		value, err := ParseMoney(amount, currency)
		if err != nil {
			return
		}
		if value.IsNegative() {
			t.Fatalf("ParseMoney(%q, %q) returned negative value %d", amount, currency, value.Minor())
		}
		if value.Amount() != amount || value.Currency() != currency {
			t.Fatalf("non-canonical success: got (%q, %q), input was (%q, %q)", value.Amount(), value.Currency(), amount, currency)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		var decoded Money
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", encoded, err)
		}
		if !decoded.Equal(value) {
			t.Fatalf("JSON round trip = %+v, want %+v", decoded, value)
		}
	})
}

func FuzzMoneyJSONRoundTrip(f *testing.F) {
	for _, minor := range []int64{math.MinInt64, -125, -1, 0, 1, 125, math.MaxInt64} {
		f.Add(minor, uint8(0))
	}
	currencies := [...]string{"BRL", "USD", "EUR", "JPY"}

	f.Fuzz(func(t *testing.T, minor int64, currencyIndex uint8) {
		currency := currencies[int(currencyIndex)%len(currencies)]
		value, err := NewMoneyFromMinor(minor, currency)
		if err != nil {
			t.Fatalf("NewMoneyFromMinor(%d, %q) error = %v", minor, currency, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		var decoded Money
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", encoded, err)
		}
		if !decoded.Equal(value) {
			t.Fatalf("JSON round trip = %+v, want %+v", decoded, value)
		}
	})
}

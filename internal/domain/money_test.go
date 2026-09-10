package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestParseMoney(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		amount   string
		currency string
		minor    int64
		wantErr  error
	}{
		{name: "zero", amount: "0.00", currency: "BRL", minor: 0},
		{name: "regular", amount: "25.09", currency: "BRL", minor: 2509},
		{name: "maximum", amount: "92233720368547758.07", currency: "USD", minor: math.MaxInt64},
		{name: "empty", amount: "", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "negative", amount: "-1.00", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "scientific", amount: "1e2", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "nan", amount: "NaN", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "infinity", amount: "Infinity", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "one decimal", amount: "1.0", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "three decimals", amount: "1.000", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "leading zero", amount: "01.00", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "spaces", amount: " 1.00", currency: "BRL", wantErr: ErrInvalidMoney},
		{name: "overflow", amount: "92233720368547758.08", currency: "BRL", wantErr: ErrMoneyOverflow},
		{name: "lowercase currency", amount: "1.00", currency: "brl", wantErr: ErrInvalidMoney},
		{name: "short currency", amount: "1.00", currency: "BR", wantErr: ErrInvalidMoney},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			money, err := ParseMoney(test.amount, test.currency)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ParseMoney() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMoney() error = %v", err)
			}
			if money.Minor() != test.minor || money.Currency() != test.currency {
				t.Fatalf("ParseMoney() = (%d, %s), want (%d, %s)", money.Minor(), money.Currency(), test.minor, test.currency)
			}
			if money.Amount() != test.amount {
				t.Fatalf("Amount() = %q, want %q", money.Amount(), test.amount)
			}
		})
	}
}

func TestMoneyOperations(t *testing.T) {
	t.Parallel()

	ten := mustMoney(t, "10.00", "BRL")
	three := mustMoney(t, "3.00", "BRL")

	sum, err := ten.Add(three)
	if err != nil || sum.Amount() != "13.00" {
		t.Fatalf("Add() = (%v, %v), want 13.00", sum, err)
	}
	difference, err := ten.Subtract(three)
	if err != nil || difference.Amount() != "7.00" {
		t.Fatalf("Subtract() = (%v, %v), want 7.00", difference, err)
	}
	negative, err := three.Negate()
	if err != nil || negative.Amount() != "-3.00" {
		t.Fatalf("Negate() = (%v, %v), want -3.00", negative, err)
	}
	comparison, err := three.Compare(ten)
	if err != nil || comparison != -1 {
		t.Fatalf("Compare() = (%d, %v), want -1", comparison, err)
	}

	usd := mustMoney(t, "1.00", "USD")
	if _, err := ten.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add() currency error = %v, want %v", err, ErrCurrencyMismatch)
	}
	maximum, _ := NewMoneyFromMinor(math.MaxInt64, "BRL")
	cent, _ := NewMoneyFromMinor(1, "BRL")
	if _, err := maximum.Add(cent); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("Add() overflow error = %v, want %v", err, ErrMoneyOverflow)
	}
	minimum, _ := NewMoneyFromMinor(math.MinInt64, "BRL")
	if _, err := minimum.Negate(); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("Negate() overflow error = %v, want %v", err, ErrMoneyOverflow)
	}
}

func TestMoneyJSON(t *testing.T) {
	t.Parallel()

	original, _ := NewMoneyFromMinor(-125, "BRL")
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(encoded) != `{"amount":"-1.25","currency":"BRL"}` {
		t.Fatalf("Marshal() = %s", encoded)
	}
	var decoded Money
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !decoded.Equal(original) {
		t.Fatalf("round trip = %v, want %v", decoded, original)
	}
	if err := json.Unmarshal([]byte(`{"amount":1.25,"currency":"BRL"}`), &decoded); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("numeric amount error = %v, want %v", err, ErrInvalidMoney)
	}
	if err := json.Unmarshal([]byte(`{"amount":"-0.00","currency":"BRL"}`), &decoded); !errors.Is(err, ErrInvalidMoney) {
		t.Fatalf("negative zero error = %v, want %v", err, ErrInvalidMoney)
	}
}

func TestMoneyJSONMinimumInt64Regression(t *testing.T) {
	t.Parallel()

	original, err := NewMoneyFromMinor(math.MinInt64, "BRL")
	if err != nil {
		t.Fatalf("NewMoneyFromMinor() error = %v", err)
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(encoded) != `{"amount":"-92233720368547758.08","currency":"BRL"}` {
		t.Fatalf("Marshal() = %s", encoded)
	}
	var decoded Money
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", encoded, err)
	}
	if !decoded.Equal(original) {
		t.Fatalf("round trip = %+v, want %+v", decoded, original)
	}
}

func mustMoney(t *testing.T, amount, currency string) Money {
	t.Helper()
	money, err := ParseMoney(amount, currency)
	if err != nil {
		t.Fatalf("ParseMoney(%q, %q): %v", amount, currency, err)
	}
	return money
}

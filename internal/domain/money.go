package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"golang.org/x/text/currency"
)

var (
	_amountPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.([0-9]{2})$`)
)

type Money struct {
	minor    int64
	currency string
}

type serializedMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(serializedMoney{Amount: m.Amount(), Currency: m.Currency()})
}

func (m *Money) UnmarshalJSON(data []byte) error {
	if m == nil {
		return fmt.Errorf("%w: nil money destination", ErrInvalidMoney)
	}
	var encoded serializedMoney
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidMoney, err)
	}
	negative := len(encoded.Amount) > 0 && encoded.Amount[0] == '-'
	amount := encoded.Amount
	if negative {
		amount = amount[1:]
	}
	parsed, err := ParseMoney(amount, encoded.Currency)
	if err != nil {
		return err
	}
	if negative {
		if parsed.IsZero() {
			return fmt.Errorf("%w: negative zero is not canonical", ErrInvalidMoney)
		}
		parsed, err = parsed.Negate()
		if err != nil {
			return err
		}
	}
	*m = parsed
	return nil
}

func ParseMoney(amount, currency string) (Money, error) {
	if _, err := parseCurrency(currency); err != nil {
		return Money{}, fmt.Errorf("%w: currency must be an ISO 4217 uppercase code", ErrInvalidMoney)
	}
	matches := _amountPattern.FindStringSubmatch(amount)
	if matches == nil {
		return Money{}, fmt.Errorf(
			"%w: amount must be a non-negative decimal with exactly two digits of scale",
			ErrInvalidMoney,
		)
	}
	major, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return Money{}, fmt.Errorf("%w: amount is outside int64 range", ErrMoneyOverflow)
	}
	cents, _ := strconv.ParseUint(matches[2], 10, 64)
	if major > uint64((math.MaxInt64-int64(cents))/100) {
		return Money{}, fmt.Errorf("%w: amount is outside int64 range", ErrMoneyOverflow)
	}
	return Money{minor: int64(major*100 + cents), currency: currency}, nil
}

func NewMoneyFromMinor(minor int64, currency string) (Money, error) {
	if _, err := parseCurrency(currency); err != nil {
		return Money{}, fmt.Errorf("%w: currency must be an ISO 4217 uppercase code", ErrInvalidMoney)
	}
	return Money{minor: minor, currency: currency}, nil
}

func Zero(currency string) (Money, error) { return NewMoneyFromMinor(0, currency) }

func (m Money) Validate() error {
	if _, err := parseCurrency(m.currency); err != nil {
		return fmt.Errorf("%w: uninitialized or invalid currency", ErrInvalidMoney)
	}
	return nil
}

func parseCurrency(code string) (currency.Unit, error) {
	if len(code) != 3 {
		return currency.Unit{}, ErrInvalidMoney
	}
	for _, value := range code {
		if value < 'A' || value > 'Z' {
			return currency.Unit{}, ErrInvalidMoney
		}
	}
	return currency.ParseISO(code)
}

func (m Money) Amount() string {
	negative := m.minor < 0
	var absolute uint64
	if negative {
		absolute = uint64(-(m.minor + 1)) + 1
	} else {
		absolute = uint64(m.minor)
	}
	value := strconv.FormatUint(absolute/100, 10) + "."
	if cents := absolute % 100; cents < 10 {
		value += "0"
	}
	value += strconv.FormatUint(absolute%100, 10)
	if negative {
		return "-" + value
	}
	return value
}

func (m Money) Add(other Money) (Money, error) {
	if err := m.compatible(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

func (m Money) Subtract(other Money) (Money, error) {
	negated, err := other.Negate()
	if err != nil {
		return Money{}, err
	}
	return m.Add(negated)
}

func (m Money) Negate() (Money, error) {
	if err := m.Validate(); err != nil {
		return Money{}, err
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

func (m Money) Compare(other Money) (int, error) {
	if err := m.compatible(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (m Money) compatible(other Money) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if err := other.Validate(); err != nil {
		return err
	}
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

func (m Money) Minor() int64           { return m.minor }
func (m Money) Currency() string       { return m.currency }
func (m Money) IsZero() bool           { return m.minor == 0 }
func (m Money) IsPositive() bool       { return m.minor > 0 }
func (m Money) IsNegative() bool       { return m.minor < 0 }
func (m Money) Equal(other Money) bool { return m == other }

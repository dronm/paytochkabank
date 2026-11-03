package paytochkabank

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Money is an exact RUB amount in kopecks. For example, Money(12345) is 123.45 RUB.
// Its default JSON representation is an integer in kopecks. Bank-specific wire
// formats are converted internally; callers never need floating-point amounts.
type Money int64

// Rubles formats the amount in rubles with exactly two decimal places.
func (m Money) Rubles() string {
	s := strconv.FormatInt(int64(m), 10)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	for len(s) < 3 {
		s = "0" + s
	}
	return sign + s[:len(s)-2] + "." + s[len(s)-2:]
}

func (m Money) String() string { return m.Rubles() }

var decimalPattern = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// ParseMoney converts a decimal RUB amount to kopecks without rounding. It rejects
// overflow and nonzero fractions smaller than one kopeck. Exponents are accepted
// within a bounded range, for compatibility with JSON number representations.
func ParseMoney(rubles string) (Money, error) {
	if len(rubles) == 0 || len(rubles) > 96 || !decimalPattern.MatchString(rubles) {
		return 0, fmt.Errorf("paytochkabank: invalid decimal amount %q", rubles)
	}
	if i := strings.IndexAny(rubles, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(rubles[i+1:])
		if err != nil || exponent < -30 || exponent > 30 {
			return 0, fmt.Errorf("paytochkabank: amount exponent out of range")
		}
	}
	r, ok := new(big.Rat).SetString(rubles)
	if !ok {
		return 0, fmt.Errorf("paytochkabank: invalid decimal amount")
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() {
		return 0, fmt.Errorf("paytochkabank: amount has a fraction smaller than one kopeck")
	}
	if !r.Num().IsInt64() {
		return 0, fmt.Errorf("paytochkabank: amount overflows int64 kopecks")
	}
	return Money(r.Num().Int64()), nil
}

// rubleMoney is used only in wire models for endpoints returning or accepting
// decimal rubles. Numeric strings are also accepted in responses and webhooks.
type rubleMoney Money

func (m rubleMoney) MarshalJSON() ([]byte, error) {
	return []byte(Money(m).Rubles()), nil
}

func (m *rubleMoney) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	var raw string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &raw); err != nil {
			return err
		}
	} else {
		raw = string(b)
	}
	amount, err := ParseMoney(raw)
	if err != nil {
		return err
	}
	*m = rubleMoney(amount)
	return nil
}

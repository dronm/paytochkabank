package paytochkabank

import (
	"encoding/json"
	"math"
	"testing"
)

func TestMoneyExactConversions(t *testing.T) {
	tests := []struct {
		input     string
		want      Money
		formatted string
	}{
		{"0", 0, "0.00"},
		{"0.01", 1, "0.01"},
		{"1234.56", 123456, "1234.56"},
		{"-0.01", -1, "-0.01"},
		{"12.3400", 1234, "12.34"},
		{"1.234e2", 12340, "123.40"},
		{"92233720368547758.07", Money(math.MaxInt64), "92233720368547758.07"},
		{"-92233720368547758.08", Money(math.MinInt64), "-92233720368547758.08"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseMoney(tt.input)
			if err != nil || got != tt.want || got.Rubles() != tt.formatted {
				t.Fatalf("ParseMoney(%q) = %v, %v; want %s", tt.input, got, err, tt.formatted)
			}
			wire, err := json.Marshal(rubleMoney(got))
			if err != nil || string(wire) != tt.formatted {
				t.Fatalf("wire = %s, %v", wire, err)
			}
			for _, input := range []string{string(wire), `"` + string(wire) + `"`} {
				var decoded rubleMoney
				if err := json.Unmarshal([]byte(input), &decoded); err != nil || Money(decoded) != tt.want {
					t.Fatalf("decode %s = %v, %v", input, decoded, err)
				}
			}
		})
	}
}

func TestMoneyRejectsLossAndOverflow(t *testing.T) {
	for _, input := range []string{"", " 1.00", "1,23", "1/2", "NaN", "null", "1.001", "0.00001", "92233720368547758.08", "-92233720368547758.09", "1e999999999", "1e-100", ".50", "1."} {
		if _, err := ParseMoney(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, input := range []string{"null", `"1.001"`, "1.001", "true", "{}"} {
		var money rubleMoney
		if err := json.Unmarshal([]byte(input), &money); err == nil {
			t.Errorf("wire accepted %q", input)
		}
	}
}

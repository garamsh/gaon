package api

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
)

// Quantity is an amount in the form `500m` or `4Gi`: a decimal number and an
// optional suffix of m, k, M, G, T, Ki, Mi, Gi or Ti. Decoding JSON accepts
// the string form and a plain JSON number, and rejects any other form.
type Quantity string

var quantityPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`)

// suffixFactors maps each suffix to the amount it multiplies the number by.
var suffixFactors = map[string]*big.Rat{
	"":   big.NewRat(1, 1),
	"m":  big.NewRat(1, 1000),
	"k":  big.NewRat(1e3, 1),
	"M":  big.NewRat(1e6, 1),
	"G":  big.NewRat(1e9, 1),
	"T":  big.NewRat(1e12, 1),
	"Ki": big.NewRat(1<<10, 1),
	"Mi": big.NewRat(1<<20, 1),
	"Gi": big.NewRat(1<<30, 1),
	"Ti": big.NewRat(1<<40, 1),
}

// ParseQuantity returns s as a Quantity, or an error if s is not one.
func ParseQuantity(s string) (Quantity, error) {
	if !quantityPattern.MatchString(s) {
		return "", fmt.Errorf("quantity %q is not a number with an optional m, k, M, G, T, Ki, Mi, Gi or Ti suffix", s)
	}
	return Quantity(s), nil
}

// Value returns the exact amount q stands for. The empty Quantity, which is
// what an omitted field holds, is zero.
func (q Quantity) Value() (*big.Rat, error) {
	if q == "" {
		return new(big.Rat), nil
	}
	m := quantityPattern.FindStringSubmatch(string(q))
	if m == nil {
		return nil, fmt.Errorf("quantity %q is not a number with an optional m, k, M, G, T, Ki, Mi, Gi or Ti suffix", string(q))
	}
	n, ok := new(big.Rat).SetString(m[1])
	if !ok {
		return nil, fmt.Errorf("quantity %q is not a number", string(q))
	}
	return n.Mul(n, suffixFactors[m[2]]), nil
}

// UnmarshalJSON decodes a quantity from a JSON string or a JSON number.
func (q *Quantity) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("quantity: %w", err)
		}
	}
	parsed, err := ParseQuantity(s)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

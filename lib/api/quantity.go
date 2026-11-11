package api

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Quantity is an amount in the form `500m` or `4Gi`: a decimal number and an
// optional suffix of m, k, M, G, T, Ki, Mi, Gi or Ti. Decoding JSON rejects
// any other form.
type Quantity string

var quantityPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(m|k|M|G|T|Ki|Mi|Gi|Ti)?$`)

// ParseQuantity returns s as a Quantity, or an error if s is not one.
func ParseQuantity(s string) (Quantity, error) {
	if !quantityPattern.MatchString(s) {
		return "", fmt.Errorf("quantity %q is not a number with an optional m, k, M, G, T, Ki, Mi, Gi or Ti suffix", s)
	}
	return Quantity(s), nil
}

func (q *Quantity) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("quantity: %w", err)
	}
	parsed, err := ParseQuantity(s)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

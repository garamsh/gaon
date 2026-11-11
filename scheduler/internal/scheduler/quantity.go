package scheduler

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

// suffixes lists each quantity suffix with its factor; the two-letter ones
// come first so that Mi is not read as i after M.
var suffixes = []struct {
	suffix string
	factor int64
}{
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
	{"k", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
}

// parseQuantity returns the exact amount q stands for. An empty quantity is
// zero: a Node that reports none offers none.
func parseQuantity(q api.Quantity) (*big.Rat, error) {
	if q == "" {
		return new(big.Rat), nil
	}
	if _, err := api.ParseQuantity(string(q)); err != nil {
		return nil, err
	}
	digits, factor := string(q), big.NewRat(1, 1)
	if rest, ok := strings.CutSuffix(digits, "m"); ok {
		digits, factor = rest, big.NewRat(1, 1000)
	} else {
		for _, s := range suffixes {
			if rest, ok := strings.CutSuffix(digits, s.suffix); ok {
				digits, factor = rest, big.NewRat(s.factor, 1)
				break
			}
		}
	}
	n, ok := new(big.Rat).SetString(digits)
	if !ok {
		return nil, fmt.Errorf("quantity %q is not a number", q)
	}
	return n.Mul(n, factor), nil
}

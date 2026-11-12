package microvms

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/garamsh/gaon/lib/api"
)

var suffixes = []struct {
	suffix string
	scale  *big.Rat
}{
	{"Ki", big.NewRat(1<<10, 1)}, {"Mi", big.NewRat(1<<20, 1)}, {"Gi", big.NewRat(1<<30, 1)}, {"Ti", big.NewRat(1<<40, 1)},
	{"m", big.NewRat(1, 1000)}, {"k", big.NewRat(1000, 1)}, {"M", big.NewRat(1000000, 1)},
	{"G", big.NewRat(1000000000, 1)}, {"T", big.NewRat(1000000000000, 1)},
}

// value returns q as an exact number.
func value(q api.Quantity) (*big.Rat, error) {
	s := string(q)
	scale := big.NewRat(1, 1)
	for _, x := range suffixes {
		if strings.HasSuffix(s, x.suffix) {
			s, scale = strings.TrimSuffix(s, x.suffix), x.scale
			break
		}
	}
	n, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("quantity %q is not a number", q)
	}
	return n.Mul(n, scale), nil
}

// cpus returns q as a whole number of vCPUs, rounded up.
func cpus(q api.Quantity) (uint32, error) {
	v, err := value(q)
	if err != nil {
		return 0, err
	}
	whole := new(big.Int).Div(new(big.Int).Add(v.Num(), new(big.Int).Sub(v.Denom(), big.NewInt(1))), v.Denom())
	if !whole.IsUint64() || whole.Uint64() > math.MaxUint32 {
		return 0, fmt.Errorf("cpu %q is out of range", q)
	}
	return uint32(whole.Uint64()), nil
}

// byteCount returns q as a whole number of bytes, rounded up.
func byteCount(q api.Quantity) (uint64, error) {
	v, err := value(q)
	if err != nil {
		return 0, err
	}
	whole := new(big.Int).Div(new(big.Int).Add(v.Num(), new(big.Int).Sub(v.Denom(), big.NewInt(1))), v.Denom())
	if !whole.IsUint64() {
		return 0, fmt.Errorf("quantity %q is out of range", q)
	}
	return whole.Uint64(), nil
}

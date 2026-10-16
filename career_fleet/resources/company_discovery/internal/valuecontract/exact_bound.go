package valuecontract

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// exactBoundNumber preserves JSON numeric tokens, comparing coefficient/order
// without float conversion, quantization, exponent expansion or a numeric cap.
// Existing Go numeric values use their own JSON representation; numeric text
// and non-finite/opaque values never become numbers through this boundary.
func exactBoundNumber(value any) (boundDecimal, error) {
	if n, ok := value.(json.Number); ok {
		return parseBoundDecimal(n.String())
	}
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		raw, err := json.Marshal(value)
		if err != nil {
			return boundDecimal{}, err
		}
		return parseBoundDecimal(string(raw))
	default:
		return boundDecimal{}, fmt.Errorf("numeric bound observation required")
	}
}

type boundDecimal struct {
	sign   int
	digits string
	order  *big.Int
}

func parseBoundDecimal(s string) (boundDecimal, error) {
	var d boundDecimal
	if s == "" {
		return d, fmt.Errorf("empty number")
	}
	i := 0
	sign := 1
	if s[i] == '-' {
		sign = -1
		i++
		if i == len(s) {
			return d, fmt.Errorf("number sign")
		}
	}
	start := i
	if s[i] == '0' {
		i++
	} else {
		if s[i] < '1' || s[i] > '9' {
			return d, fmt.Errorf("number integer")
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	boundDecimalAt := i - start
	if i < len(s) && s[i] == '.' {
		i++
		begin := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == begin {
			return d, fmt.Errorf("number fraction")
		}
	}
	end := i
	exponent := "0"
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		begin := i
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		digitsStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == digitsStart {
			return d, fmt.Errorf("number exponent")
		}
		exponent = s[begin:i]
	}
	if i != len(s) {
		return d, fmt.Errorf("number syntax")
	}
	order, ok := new(big.Int).SetString(exponent, 10)
	if !ok {
		return d, fmt.Errorf("number exponent")
	}
	digits := strings.ReplaceAll(s[start:end], ".", "")
	leading := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = strings.TrimRight(strings.TrimLeft(digits, "0"), "0")
	if digits == "" {
		return boundDecimal{order: new(big.Int)}, nil
	}
	order.Add(order, big.NewInt(int64(boundDecimalAt-leading)))
	return boundDecimal{sign: sign, digits: digits, order: order}, nil
}
func (d boundDecimal) integral() bool {
	return d.sign == 0 || d.order.Cmp(big.NewInt(int64(len(d.digits)))) >= 0
}
func compareBoundDecimal(a, b boundDecimal) int {
	if a.sign != b.sign {
		if a.sign < b.sign {
			return -1
		}
		return 1
	}
	if a.sign == 0 {
		return 0
	}
	if n := a.order.Cmp(b.order); n != 0 {
		return a.sign * n
	}
	for i := 0; i < len(a.digits) || i < len(b.digits); i++ {
		x, y := byte('0'), byte('0')
		if i < len(a.digits) {
			x = a.digits[i]
		}
		if i < len(b.digits) {
			y = b.digits[i]
		}
		if x < y {
			return -a.sign
		}
		if x > y {
			return a.sign
		}
	}
	return 0
}

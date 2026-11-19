package core

import (
	"errors"
	"fmt"
)

// MaxRound bounds the doubling so int64 cents can never overflow.
const MaxRound = 50

var ErrPriceOverflow = errors.New("price beyond supported range")

// PriceForRound returns the price in US cents for the given spotlight round.
// Round 0 is the founding post and is free. Round r >= 1 costs $1 * 2^(r-1):
// $1, $2, $4, $8, $16, $32 ...
func PriceForRound(round int) (int64, error) {
	if round < 0 || round > MaxRound {
		return 0, ErrPriceOverflow
	}
	if round == 0 {
		return 0, nil
	}
	return int64(100) << (round - 1), nil
}

// FormatUSD renders cents as "$16" or "$12.50".
func FormatUSD(cents int64) string {
	if cents%100 == 0 {
		return fmt.Sprintf("$%s", groupThousands(cents/100))
	}
	return fmt.Sprintf("$%s.%02d", groupThousands(cents/100), cents%100)
}

func groupThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	pre := len(s) % 3
	if pre > 0 {
		out = append(out, s[:pre]...)
	}
	for i := pre; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}

// EgoReceiptHeadline is the approved card sentence. Only the dollar amount is
// substituted for other paid tiers. The $16 card reads exactly:
// "I paid $16 to make you read this. It worked."
func EgoReceiptHeadline(paidCents int64) string {
	if paidCents <= 0 {
		return "I took the first spotlight for free."
	}
	return fmt.Sprintf("I paid %s to make you read this. It worked.", FormatUSD(paidCents))
}

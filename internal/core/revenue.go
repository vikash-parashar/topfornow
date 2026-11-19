package core

import (
	"context"
	"time"
)

// Fee model used for the owner's earnings estimate. Dodo Payments public
// pricing (checked 19 Nov 2026): 4% + $0.40 per payment, +1.5% for
// international cards, $1 per refund, $30 per dispute. The provider's own
// payout report is the source of truth; these numbers are an estimate.
const (
	FeeBps          = 400
	FeeFixedCents   = 40
	IntlExtraBps    = 150
	RefundFeeCents  = 100
	DisputeFeeCents = 3000
)

// capturedStatuses are payment statuses where money was actually taken.
const capturedStatuses = `('succeeded','refund_pending','refunded','refund_failed')`

// Money is one period's earnings summary, all in integer cents.
type Money struct {
	Payments     int   // captured payments
	GrossCents   int64 // money collected
	RefundCents  int64 // money returned to buyers
	RefundCount  int
	Disputes     int
	FeeCents     int64 // estimated provider fees (domestic rate)
	FeeMaxCents  int64 // estimated fees if every card were international
	NetCents     int64 // gross - refunds - fees - refund fees - dispute fees
	NetMinCents  int64 // same, with the international fee rate
	BiggestCents int64
}

// AvgCents is the average captured payment.
func (m Money) AvgCents() int64 {
	if m.Payments == 0 {
		return 0
	}
	return m.GrossCents / int64(m.Payments)
}

// DayPoint is one bar of the revenue chart.
type DayPoint struct {
	Day        time.Time
	GrossCents int64
	NetCents   int64
	Payments   int
}

// Revenue is the owner dashboard money view.
type Revenue struct {
	Today, Week, Month, All Money
	Days                    []DayPoint // last 30 days, oldest first, in the owner's time zone
	MaxDayCents             int64
	Location                *time.Location
	IncludesTest            bool
	PendingPayouts          int64 // net all-time; Dodo pays out from $50
	CheckoutsStarted30d     int
	PaidPosts30d            int
}

// ConversionPct is paid posts / checkouts started over 30 days.
func (r *Revenue) ConversionPct() int {
	if r.CheckoutsStarted30d == 0 {
		return 0
	}
	return r.PaidPosts30d * 100 / r.CheckoutsStarted30d
}

func (m *Money) finish() {
	m.FeeCents = int64(m.Payments)*FeeFixedCents + m.GrossCents*FeeBps/10000
	m.FeeMaxCents = int64(m.Payments)*FeeFixedCents + m.GrossCents*(FeeBps+IntlExtraBps)/10000
	other := m.RefundCents + int64(m.RefundCount)*RefundFeeCents + int64(m.Disputes)*DisputeFeeCents
	m.NetCents = m.GrossCents - m.FeeCents - other
	m.NetMinCents = m.GrossCents - m.FeeMaxCents - other
}

// moneySince sums captured payments created at or after since (zero = all time).
func (s *Service) moneySince(ctx context.Context, since time.Time, includeTest bool) (Money, error) {
	var m Money
	err := s.DB.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount_cents),0), coalesce(sum(refunded_cents),0),
		count(*) FILTER (WHERE refunded_cents > 0), count(*) FILTER (WHERE dispute_status IS NOT NULL),
		coalesce(max(amount_cents),0)
		FROM payments WHERE status IN `+capturedStatuses+` AND created_at >= $1 AND ($2 OR NOT is_test)`,
		since, includeTest).Scan(&m.Payments, &m.GrossCents, &m.RefundCents, &m.RefundCount, &m.Disputes, &m.BiggestCents)
	m.finish()
	return m, err
}

// RevenueReport builds the owner's earnings view. includeTest should be true
// only outside live mode, so test money never mixes with real money.
func (s *Service) RevenueReport(ctx context.Context, loc *time.Location, includeTest bool) (*Revenue, error) {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	r := &Revenue{Location: loc, IncludesTest: includeTest}
	var err error
	if r.Today, err = s.moneySince(ctx, midnight, includeTest); err != nil {
		return nil, err
	}
	if r.Week, err = s.moneySince(ctx, midnight.AddDate(0, 0, -6), includeTest); err != nil {
		return nil, err
	}
	if r.Month, err = s.moneySince(ctx, midnight.AddDate(0, 0, -29), includeTest); err != nil {
		return nil, err
	}
	if r.All, err = s.moneySince(ctx, time.Time{}, includeTest); err != nil {
		return nil, err
	}
	r.PendingPayouts = r.All.NetCents

	rows, err := s.DB.Query(ctx, `SELECT d::date, coalesce(sum(p.amount_cents),0), coalesce(sum(p.refunded_cents),0),
		count(p.id), count(p.id) FILTER (WHERE p.refunded_cents > 0)
		FROM generate_series(($1::timestamptz AT TIME ZONE $2)::date - 29, ($1::timestamptz AT TIME ZONE $2)::date, interval '1 day') d
		LEFT JOIN payments p ON (p.created_at AT TIME ZONE $2)::date = d::date
			AND p.status IN `+capturedStatuses+` AND ($3 OR NOT p.is_test)
		GROUP BY d ORDER BY d`, now, loc.String(), includeTest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day time.Time
		var m Money
		if err := rows.Scan(&day, &m.GrossCents, &m.RefundCents, &m.Payments, &m.RefundCount); err != nil {
			return nil, err
		}
		m.finish()
		r.Days = append(r.Days, DayPoint{Day: day, GrossCents: m.GrossCents, NetCents: m.NetCents, Payments: m.Payments})
		if m.GrossCents > r.MaxDayCents {
			r.MaxDayCents = m.GrossCents
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	since := midnight.AddDate(0, 0, -29)
	s.DB.QueryRow(ctx, `SELECT count(*) FROM events WHERE type = 'checkout_start' AND NOT is_bot AND ts >= $1`, since).Scan(&r.CheckoutsStarted30d)
	s.DB.QueryRow(ctx, `SELECT count(*) FROM posts WHERE round IS NOT NULL AND price_cents > 0 AND published_at >= $1 AND ($2 OR NOT is_test)`,
		since, includeTest).Scan(&r.PaidPosts30d)
	return r, nil
}

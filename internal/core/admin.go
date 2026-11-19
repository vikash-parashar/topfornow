package core

import (
	"context"
	"fmt"
	"time"
)

// ListPosts returns posts filtered by status (newest first).
func (s *Service) ListPosts(ctx context.Context, status string, limit int) ([]*Post, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+postCols+` FROM posts p WHERE ($1 = '' OR p.status = $1) ORDER BY p.id DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Approve lets a held draft proceed to checkout.
func (s *Service) Approve(ctx context.Context, id int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status = 'approved' WHERE id = $1 AND status IN ('needs_review','rejected')`, id)
	return err
}

// Reject blocks a draft before payment.
func (s *Service) Reject(ctx context.Context, id int64, note string) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status = 'rejected', review_notes = $2 WHERE id = $1 AND status IN ('needs_review','approved')`, id, note)
	return err
}

// Remove hides abusive published content. The round stays consumed and the
// price does not go back down; the post page shows that it was removed.
func (s *Service) Remove(ctx context.Context, id int64, reason string) error {
	_, err := s.DB.Exec(ctx, `UPDATE posts SET status = 'removed', removed_at = now(), removed_reason = $2,
		dethroned_at = coalesce(dethroned_at, CASE WHEN round IS NOT NULL THEN now() END) WHERE id = $1`, id, reason)
	if err == nil {
		s.DB.Exec(ctx, `UPDATE reports SET status = 'actioned' WHERE post_id = $1 AND status = 'open'`, id)
	}
	return err
}

// ReportRow is an open report.
type ReportRow struct {
	ID       int64
	PostID   int64
	Slug     string
	Headline string
	Reason   string
	Count    int
	At       time.Time
}

// OpenReports groups open reports by post.
func (s *Service) OpenReports(ctx context.Context) ([]ReportRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT max(r.id), p.id, p.slug, p.headline, string_agg(DISTINCT r.reason, '; '), count(*), max(r.created_at)
		FROM reports r JOIN posts p ON p.id = r.post_id WHERE r.status = 'open' GROUP BY p.id ORDER BY count(*) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReportRow
	for rows.Next() {
		var r ReportRow
		if err := rows.Scan(&r.ID, &r.PostID, &r.Slug, &r.Headline, &r.Reason, &r.Count, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DismissReports closes reports for a post.
func (s *Service) DismissReports(ctx context.Context, postID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE reports SET status = 'dismissed' WHERE post_id = $1 AND status = 'open'`, postID)
	return err
}

// PaymentRow is a payment for the admin ledger.
type PaymentRow struct {
	ID                int64
	ProviderPaymentID string
	ReservationID     string
	PostSlug          string
	Round             *int
	AmountCents       int64
	Currency          string
	Status, Outcome   string
	RefundedCents     int64
	RefundID          string
	Dispute           string
	IsTest            bool
	CreatedAt         time.Time
}

// Payments lists recent payments.
func (s *Service) Payments(ctx context.Context, limit int) ([]PaymentRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT pm.id, pm.provider_payment_id, coalesce(pm.reservation_id::text,''), coalesce(p.slug,''), r.round,
		pm.amount_cents, pm.currency, pm.status, pm.outcome, pm.refunded_cents, coalesce(pm.provider_refund_id,''), coalesce(pm.dispute_status,''), pm.is_test, pm.created_at
		FROM payments pm LEFT JOIN posts p ON p.id = pm.post_id LEFT JOIN reservations r ON r.id = pm.reservation_id
		ORDER BY pm.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		var r PaymentRow
		if err := rows.Scan(&r.ID, &r.ProviderPaymentID, &r.ReservationID, &r.PostSlug, &r.Round, &r.AmountCents, &r.Currency,
			&r.Status, &r.Outcome, &r.RefundedCents, &r.RefundID, &r.Dispute, &r.IsTest, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Reconciliation is the ledger health check.
type Reconciliation struct {
	RoundsPublished      int
	PublishedPosts       int
	PaidPublishedPosts   int
	PublishedCents       int64 // sum of price of paid published posts
	PublishedPaymentCent int64 // sum of succeeded payments whose outcome is published
	RefundedCents        int64
	PendingRefunds       int
	FailedRefunds        int
	OpenDisputes         int
	AbandonedHolds       int
	Problems             []string
}

// Reconcile checks the invariants between spotlight, posts and payments.
func (s *Service) Reconcile(ctx context.Context) (*Reconciliation, error) {
	r := &Reconciliation{}
	q := s.DB.QueryRow(ctx, `SELECT
		(SELECT rounds_published FROM spotlight WHERE id = 1),
		(SELECT count(*) FROM posts WHERE round IS NOT NULL),
		(SELECT count(*) FROM posts WHERE round IS NOT NULL AND price_cents > 0),
		(SELECT coalesce(sum(price_cents),0) FROM posts WHERE round IS NOT NULL AND price_cents > 0),
		(SELECT coalesce(sum(amount_cents),0) FROM payments WHERE outcome LIKE 'published%'),
		(SELECT coalesce(sum(refunded_cents),0) FROM payments),
		(SELECT count(*) FROM payments WHERE status = 'refund_pending'),
		(SELECT count(*) FROM payments WHERE status = 'refund_failed'),
		(SELECT count(*) FROM payments WHERE dispute_status IN ('dispute.opened','dispute.challenged')),
		(SELECT count(*) FROM reservations r WHERE status IN ('expired','cancelled') AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.reservation_id = r.id))`)
	if err := q.Scan(&r.RoundsPublished, &r.PublishedPosts, &r.PaidPublishedPosts, &r.PublishedCents, &r.PublishedPaymentCent,
		&r.RefundedCents, &r.PendingRefunds, &r.FailedRefunds, &r.OpenDisputes, &r.AbandonedHolds); err != nil {
		return nil, err
	}
	if r.RoundsPublished != r.PublishedPosts {
		r.Problems = append(r.Problems, fmt.Sprintf("spotlight says %d rounds but %d posts hold a round", r.RoundsPublished, r.PublishedPosts))
	}
	// Rounds must be contiguous 0..n-1 and each paid round must have exactly
	// one published payment of at least its price.
	rows, err := s.DB.Query(ctx, `SELECT p.round, p.slug, p.price_cents,
		(SELECT count(*) FROM payments pm WHERE pm.post_id = p.id AND pm.outcome LIKE 'published%'),
		(SELECT coalesce(max(pm.amount_cents),0) FROM payments pm WHERE pm.post_id = p.id AND pm.outcome LIKE 'published%')
		FROM posts p WHERE p.round IS NOT NULL ORDER BY p.round`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	expect := 0
	for rows.Next() {
		var round, n int
		var slug string
		var price, paid int64
		if err := rows.Scan(&round, &slug, &price, &n, &paid); err != nil {
			return nil, err
		}
		if round != expect {
			r.Problems = append(r.Problems, fmt.Sprintf("round gap: expected %d, found %d", expect, round))
		}
		expect = round + 1
		want, _ := PriceForRound(round)
		if price != want {
			r.Problems = append(r.Problems, fmt.Sprintf("round %d (%s) price %d, rule says %d", round, slug, price, want))
		}
		if round > 0 && (n != 1 || paid < price) {
			r.Problems = append(r.Problems, fmt.Sprintf("round %d (%s) has %d published payments (max %d cents)", round, slug, n, paid))
		}
	}
	if r.FailedRefunds > 0 {
		r.Problems = append(r.Problems, fmt.Sprintf("%d refunds failed at the provider: refund them by hand in the dashboard", r.FailedRefunds))
	}
	return r, rows.Err()
}

// PurgeTestData deletes every row labelled as test data. It refuses when the
// spotlight holds real (non-test) posts mixed with test rounds, because that
// would break round numbering; reset in that case is a manual decision.
func (s *Service) PurgeTestData(ctx context.Context) (map[string]int64, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var real int
	tx.QueryRow(ctx, `SELECT count(*) FROM posts WHERE round IS NOT NULL AND NOT is_test`).Scan(&real)
	var testRounds int
	tx.QueryRow(ctx, `SELECT count(*) FROM posts WHERE round IS NOT NULL AND is_test`).Scan(&testRounds)
	if real > 0 && testRounds > 0 {
		return nil, fmt.Errorf("real and test rounds are mixed; not purging automatically")
	}
	out := map[string]int64{}
	stmts := []struct{ name, sql string }{
		{"events", `DELETE FROM events WHERE is_test`},
		{"votes", `DELETE FROM votes WHERE is_test`},
		{"reports", `DELETE FROM reports WHERE is_test`},
		{"webhook_events", `DELETE FROM webhook_events WHERE is_test`},
		{"payments", `DELETE FROM payments WHERE is_test`},
		{"reservations", `DELETE FROM reservations WHERE is_test`},
		{"sim_sessions", `DELETE FROM sim_sessions`},
		{"spotlight_reset", `UPDATE spotlight SET rounds_published = 0, current_post_id = NULL WHERE (SELECT count(*) FROM posts WHERE round IS NOT NULL AND NOT is_test) = 0`},
		{"posts", `DELETE FROM posts WHERE is_test AND NOT EXISTS (SELECT 1 FROM posts c WHERE c.reply_to_id = posts.id AND NOT c.is_test)`},
	}
	tx.Exec(ctx, `UPDATE posts SET reply_to_id = NULL WHERE is_test AND reply_to_id IS NOT NULL`)
	for _, st := range stmts {
		tag, err := tx.Exec(ctx, st.sql)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", st.name, err)
		}
		out[st.name] = tag.RowsAffected()
	}
	return out, tx.Commit(ctx)
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PaymentEvent is a verified, provider-neutral payment or refund event.
type PaymentEvent struct {
	Provider      string
	EventID       string // unique per delivery source (webhook-id, or a synthetic id)
	Type          string // payment.succeeded, payment.failed, payment.processing, payment.cancelled, refund.succeeded, refund.failed, dispute.*
	PaymentID     string
	SessionID     string
	ReservationID string // from metadata
	AmountCents   int64  // total charged, or refund amount for refund events
	Currency      string
	RefundID      string
	Raw           json.RawMessage
}

// ApplyResult describes what an event did.
type ApplyResult struct {
	Duplicate bool
	Outcome   string
	PostID    int64
}

// ApplyPaymentEvent is idempotent: replaying any event, or receiving the same
// payment through two different event ids, changes nothing the second time.
func (s *Service) ApplyPaymentEvent(ctx context.Context, ev PaymentEvent) (*ApplyResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	raw := ev.Raw
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var evRow int64
	err = tx.QueryRow(ctx, `INSERT INTO webhook_events (provider, event_id, event_type, payload, is_test) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (provider, event_id) DO NOTHING RETURNING id`, ev.Provider, ev.EventID, ev.Type, raw, s.Cfg.IsTest).Scan(&evRow)
	if errors.Is(err, pgx.ErrNoRows) {
		return &ApplyResult{Duplicate: true, Outcome: "duplicate_event"}, nil
	}
	if err != nil {
		return nil, err
	}

	// Serialize every money event behind the spotlight lock.
	var rounds int
	if err := tx.QueryRow(ctx, `SELECT rounds_published FROM spotlight WHERE id = 1 FOR UPDATE`).Scan(&rounds); err != nil {
		return nil, err
	}

	res := &ApplyResult{}
	switch ev.Type {
	case "payment.succeeded":
		err = s.onSucceeded(ctx, tx, ev, rounds, res)
	case "payment.processing":
		err = s.onPending(ctx, tx, ev, "processing", res)
	case "payment.failed", "payment.cancelled":
		st := "failed"
		if ev.Type == "payment.cancelled" {
			st = "cancelled"
		}
		err = s.onPending(ctx, tx, ev, st, res)
	case "refund.succeeded":
		tag, e := tx.Exec(ctx, `UPDATE payments SET status = 'refunded', refunded_cents = refunded_cents + $3,
			provider_refund_id = coalesce(provider_refund_id, nullif($4,'')), updated_at = now()
			WHERE provider = $1 AND provider_payment_id = $2 AND status <> 'refunded'`, ev.Provider, ev.PaymentID, ev.AmountCents, ev.RefundID)
		err = e
		res.Outcome = "refund_recorded"
		if e == nil && tag.RowsAffected() == 0 {
			res.Outcome = "refund_ignored"
		}
	case "refund.failed":
		_, err = tx.Exec(ctx, `UPDATE payments SET status = 'refund_failed', updated_at = now() WHERE provider = $1 AND provider_payment_id = $2`, ev.Provider, ev.PaymentID)
		res.Outcome = "refund_failed"
	default:
		if len(ev.Type) > 8 && ev.Type[:8] == "dispute." {
			_, err = tx.Exec(ctx, `UPDATE payments SET dispute_status = $3, updated_at = now() WHERE provider = $1 AND provider_payment_id = $2`, ev.Provider, ev.PaymentID, ev.Type)
			res.Outcome = "dispute_recorded"
		} else {
			res.Outcome = "ignored"
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE webhook_events SET result = $2 WHERE id = $1`, evRow, res.Outcome); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.Log.Info("payment event", "type", ev.Type, "payment", ev.PaymentID, "outcome", res.Outcome)
	if len(res.Outcome) > 7 && res.Outcome[:7] == "refund_" && res.Outcome != "refund_recorded" && res.Outcome != "refund_ignored" && res.Outcome != "refund_failed" {
		s.kickRefunds()
	}
	return res, nil
}

func (s *Service) kickRefunds() {
	select {
	case s.RefundKick <- struct{}{}:
	default:
	}
}

func (s *Service) findHold(ctx context.Context, tx pgx.Tx, ev PaymentEvent) (*Hold, error) {
	if ev.ReservationID != "" && len(ev.ReservationID) == 36 {
		h, err := scanHold(tx.QueryRow(ctx, `SELECT `+holdCols+` FROM reservations WHERE id = $1::uuid FOR UPDATE`, ev.ReservationID))
		if err == nil || !errors.Is(err, ErrNotFound) {
			return h, err
		}
	}
	if ev.SessionID != "" {
		return scanHold(tx.QueryRow(ctx, `SELECT `+holdCols+` FROM reservations WHERE provider_session_id = $1 FOR UPDATE`, ev.SessionID))
	}
	return nil, ErrNotFound
}

func (s *Service) onPending(ctx context.Context, tx pgx.Tx, ev PaymentEvent, status string, res *ApplyResult) error {
	h, err := s.findHold(ctx, tx, ev)
	var holdID *string
	var postID *int64
	if err == nil {
		holdID, postID = &h.ID, &h.PostID
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	// Never downgrade a payment that already succeeded.
	if _, err := tx.Exec(ctx, `INSERT INTO payments (provider, provider_payment_id, reservation_id, post_id, amount_cents, currency, status, outcome, is_test)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$7,$8)
		ON CONFLICT (provider, provider_payment_id) DO UPDATE SET status = EXCLUDED.status, outcome = EXCLUDED.outcome, updated_at = now()
		WHERE payments.status IN ('processing','failed','cancelled')`,
		ev.Provider, ev.PaymentID, holdID, postID, ev.AmountCents, ev.Currency, status, s.Cfg.IsTest); err != nil {
		return err
	}
	if status == "processing" && h != nil && h.Status == "active" {
		// Async methods: keep the round reserved while the bank decides.
		if _, err := tx.Exec(ctx, `UPDATE reservations SET expires_at = greatest(expires_at, $2), updated_at = now() WHERE id = $1::uuid`,
			h.ID, s.Now().Add(2*time.Hour)); err != nil {
			return err
		}
	}
	res.Outcome = status
	return nil
}

func (s *Service) onSucceeded(ctx context.Context, tx pgx.Tx, ev PaymentEvent, rounds int, res *ApplyResult) error {
	var existing string
	err := tx.QueryRow(ctx, `SELECT status FROM payments WHERE provider = $1 AND provider_payment_id = $2 FOR UPDATE`, ev.Provider, ev.PaymentID).Scan(&existing)
	if err == nil && existing != "processing" && existing != "failed" && existing != "cancelled" {
		res.Duplicate, res.Outcome = true, "duplicate_payment"
		return nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	h, herr := s.findHold(ctx, tx, ev)
	if herr != nil && !errors.Is(herr, ErrNotFound) {
		return herr
	}
	outcome, status := "", "succeeded"
	var holdID *string
	var postID *int64
	switch {
	case h == nil:
		outcome, status = "refund_unknown_reservation", "refund_pending"
	default:
		holdID, postID = &h.ID, &h.PostID
		switch {
		case ev.Currency != "USD" || ev.AmountCents < h.PriceCents:
			outcome, status = "refund_amount_mismatch", "refund_pending"
		case h.Status == "paid":
			outcome, status = "refund_duplicate_charge", "refund_pending"
		case h.Status == "cancelled" || h.Status == "lost" || rounds != h.Round:
			outcome, status = "refund_round_closed", "refund_pending"
		default:
			// h.Status is active or expired and its round is still open:
			// the buyer paid the price shown for a round nobody else took.
			if err := s.publishTx(ctx, tx, h.PostID, h.Round, h.PriceCents); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'paid', updated_at = now() WHERE id = $1::uuid`, h.ID); err != nil {
				return err
			}
			// Anyone else holding the same round (possible only after this
			// hold expired) loses it; if they pay anyway they are refunded.
			if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'lost', updated_at = now() WHERE status = 'active' AND id <> $1::uuid`, h.ID); err != nil {
				return err
			}
			outcome = "published"
			res.PostID = h.PostID
		}
		if outcome != "published" && h.Status == "active" && rounds == h.Round && outcome != "refund_duplicate_charge" {
			// e.g. wrong amount: free the round for others.
			tx.Exec(ctx, `UPDATE reservations SET status = 'cancelled', updated_at = now() WHERE id = $1::uuid`, h.ID)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payments (provider, provider_payment_id, reservation_id, post_id, amount_cents, currency, status, outcome, is_test)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (provider, provider_payment_id) DO UPDATE SET status = EXCLUDED.status, outcome = EXCLUDED.outcome,
		amount_cents = EXCLUDED.amount_cents, reservation_id = coalesce(payments.reservation_id, EXCLUDED.reservation_id),
		post_id = coalesce(payments.post_id, EXCLUDED.post_id), updated_at = now()`,
		ev.Provider, ev.PaymentID, holdID, postID, ev.AmountCents, ev.Currency, status, outcome, s.Cfg.IsTest); err != nil {
		return err
	}
	if outcome == "published" {
		tx.Exec(ctx, `INSERT INTO events (type, post_id, is_test) VALUES ('payment', $1, $2)`, h.PostID, s.Cfg.IsTest)
	}
	res.Outcome = outcome
	return nil
}

// ProcessRefunds asks the provider to refund every payment queued for refund.
// Safe to call repeatedly: a payment with a provider refund id is skipped,
// and retries back off.
func (s *Service) ProcessRefunds(ctx context.Context) (int, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, provider_payment_id, outcome FROM payments
		WHERE status = 'refund_pending' AND provider_refund_id IS NULL AND refund_attempts < 8
		AND (refund_requested_at IS NULL OR refund_requested_at < now() - make_interval(mins => 2 * refund_attempts))
		ORDER BY id LIMIT 20`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id      int64
		pid, oc string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.pid, &it.oc); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	done := 0
	for _, it := range items {
		refundID, err := s.Provider.Refund(ctx, it.pid, "TopForNow automatic refund: "+it.oc)
		if err != nil {
			s.Log.Error("refund failed", "payment", it.pid, "err", err)
			s.DB.Exec(ctx, `UPDATE payments SET refund_attempts = refund_attempts + 1, refund_requested_at = now() WHERE id = $1`, it.id)
			continue
		}
		s.DB.Exec(ctx, `UPDATE payments SET provider_refund_id = $2, refund_attempts = refund_attempts + 1, refund_requested_at = now(), updated_at = now() WHERE id = $1`, it.id, refundID)
		done++
	}
	return done, nil
}

// RunRefundWorker processes refunds when kicked and once a minute.
func (s *Service) RunRefundWorker(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if _, err := s.ProcessRefunds(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("refund worker", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.RefundKick:
		}
	}
}

// AdminRefund queues a refund for a succeeded payment. The post stays where it
// is; the owner decides separately whether to remove it.
func (s *Service) AdminRefund(ctx context.Context, paymentRowID int64) error {
	tag, err := s.DB.Exec(ctx, `UPDATE payments SET status = 'refund_pending', outcome = outcome || '+admin_refund', updated_at = now()
		WHERE id = $1 AND status = 'succeeded'`, paymentRowID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("payment %d is not refundable", paymentRowID)
	}
	s.kickRefunds()
	return nil
}

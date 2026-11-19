package pay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/vikash-parashar/topfornow/internal/core"
)

// Sim is the built-in sandbox payment provider. It never moves money. It
// renders its own checkout page and delivers Dodo-shaped webhooks, signed with
// the sandbox secret, to the app's real webhook endpoint.
type Sim struct {
	DB         *pgxpool.Pool
	BaseURL    string // public URL used in checkout links
	WebhookURL string // where to deliver webhooks (usually http://127.0.0.1:PORT/webhooks/dodo)
	Secret     string // whsec_...
	Log        *slog.Logger
	HTTP       *http.Client
}

// Name implements core.Provider. The simulator pretends to be Dodo so the
// exact same webhook code path is exercised.
func (s *Sim) Name() string { return "dodo" }

func rid(prefix string) string {
	b := make([]byte, 10)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// CreateCheckout implements core.Provider.
func (s *Sim) CreateCheckout(ctx context.Context, r core.CheckoutRequest) (core.Checkout, error) {
	id := rid("cks_sim_")
	md, _ := json.Marshal(map[string]string{
		"reservation_id": r.ReservationID,
		"round":          strconv.Itoa(r.Round),
		"price_cents":    strconv.FormatInt(r.PriceCents, 10),
		"return_url":     r.ReturnURL,
		"cancel_url":     r.CancelURL,
		"description":    r.Description,
	})
	if _, err := s.DB.Exec(ctx, `INSERT INTO sim_sessions (id, amount_cents, metadata) VALUES ($1,$2,$3)`, id, r.PriceCents, md); err != nil {
		return core.Checkout{}, err
	}
	return core.Checkout{SessionID: id, URL: s.BaseURL + "/sandbox/checkout/" + id}, nil
}

// SimSession is a stored sandbox checkout.
type SimSession struct {
	ID          string
	AmountCents int64
	Meta        map[string]string
	Status      string
	PaymentID   string
}

// Session loads a sandbox session.
func (s *Sim) Session(ctx context.Context, id string) (*SimSession, error) {
	ss := &SimSession{}
	var md []byte
	var pid *string
	if err := s.DB.QueryRow(ctx, `SELECT id, amount_cents, metadata, status, payment_id FROM sim_sessions WHERE id = $1`, id).
		Scan(&ss.ID, &ss.AmountCents, &md, &ss.Status, &pid); err != nil {
		return nil, err
	}
	json.Unmarshal(md, &ss.Meta)
	if pid != nil {
		ss.PaymentID = *pid
	}
	return ss, nil
}

// Act simulates the buyer's action on the hosted checkout.
// action: "pay", "decline", "pay_async" (processing then succeeded).
// deliveries: how many times to deliver the final webhook (2 = duplicate).
// delay: wait before delivering (simulates a slow provider).
func (s *Sim) Act(ctx context.Context, sessionID, action string, deliveries int, delay time.Duration) (string, error) {
	ss, err := s.Session(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if ss.Status == "paid" {
		return ss.PaymentID, fmt.Errorf("session already paid")
	}
	paymentID := rid("pay_sim_")
	status := "succeeded"
	switch action {
	case "decline":
		status = "failed"
	case "pay", "pay_async":
	default:
		return "", fmt.Errorf("unknown action")
	}
	newStatus := "paid"
	if status == "failed" {
		newStatus = "open"
	}
	if _, err := s.DB.Exec(ctx, `UPDATE sim_sessions SET status = $2, payment_id = CASE WHEN $2 = 'paid' THEN $3 ELSE payment_id END WHERE id = $1`,
		sessionID, newStatus, paymentID); err != nil {
		return "", err
	}
	p := DodoPayment{PaymentID: paymentID, Status: status, TotalAmount: ss.AmountCents, Currency: "USD",
		Metadata: map[string]string{"reservation_id": ss.Meta["reservation_id"], "round": ss.Meta["round"], "price_cents": ss.Meta["price_cents"]},
		CheckoutSessionID: ss.ID}
	if deliveries < 1 {
		deliveries = 1
	}
	go func() {
		bg := context.Background()
		if action == "pay_async" {
			pp := p
			pp.Status = "processing"
			s.deliver(bg, rid("msg_sim_"), "payment.processing", pp)
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		msgID := rid("msg_sim_")
		for i := 0; i < deliveries; i++ {
			// Same webhook-id each time, exactly like a provider retry.
			s.deliver(bg, msgID, "payment."+status, p)
		}
	}()
	return paymentID, nil
}

// Refund implements core.Provider.
func (s *Sim) Refund(ctx context.Context, paymentID, reason string) (string, error) {
	refundID := rid("ref_sim_")
	var amount int64
	s.DB.QueryRow(ctx, `SELECT amount_cents FROM sim_sessions WHERE payment_id = $1`, paymentID).Scan(&amount)
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.deliver(context.Background(), rid("msg_sim_"), "refund.succeeded",
			DodoPayment{PaymentID: paymentID, RefundID: refundID, Amount: amount, Currency: "USD", Status: "succeeded"})
	}()
	return refundID, nil
}

// GetPayment mirrors Dodo's GET /payments/{id} for sandbox sessions.
func (s *Sim) GetPayment(ctx context.Context, id string) (*DodoPayment, error) {
	var sid string
	var amount int64
	var md []byte
	if err := s.DB.QueryRow(ctx, `SELECT id, amount_cents, metadata FROM sim_sessions WHERE payment_id = $1`, id).Scan(&sid, &amount, &md); err != nil {
		return nil, err
	}
	var meta map[string]string
	json.Unmarshal(md, &meta)
	return &DodoPayment{PaymentID: id, Status: "succeeded", TotalAmount: amount, Currency: "USD",
		Metadata: map[string]string{"reservation_id": meta["reservation_id"]}, CheckoutSessionID: sid}, nil
}

// Deliver signs and posts one webhook. Exported for replay tests.
func (s *Sim) Deliver(ctx context.Context, msgID, typ string, p DodoPayment) error {
	return s.deliver(ctx, msgID, typ, p)
}

func (s *Sim) deliver(ctx context.Context, msgID, typ string, p DodoPayment) error {
	body, _ := json.Marshal(map[string]any{
		"business_id": "bus_sandbox", "type": typ, "timestamp": time.Now().UTC(), "data": p,
	})
	wh, err := standardwebhooks.NewWebhook(s.Secret)
	if err != nil {
		return err
	}
	now := time.Now()
	sig, err := wh.Sign(msgID, now, body)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", msgID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("webhook-signature", sig)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		s.Log.Error("sim webhook delivery", "err", err)
		return err
	}
	resp.Body.Close()
	s.Log.Info("sim webhook delivered", "type", typ, "msg", msgID, "status", resp.StatusCode)
	return nil
}

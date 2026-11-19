// Package pay talks to Dodo Payments (test or live mode) and provides a
// built-in sandbox simulator that sends Dodo-shaped, Standard-Webhooks-signed
// events through the very same webhook endpoint.
package pay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"

	"github.com/vikash-parashar/topfornow/internal/core"
)

// Modes.
const (
	ModeSim      = "sandbox"   // built-in simulator, no external calls
	ModeDodoTest = "dodo_test" // Dodo test mode: real API, no real money
	ModeDodoLive = "dodo_live" // real charges
)

// Dodo is a minimal Dodo Payments client.
type Dodo struct {
	BaseURL   string // https://test.dodopayments.com or https://live.dodopayments.com
	APIKey    string
	ProductID string // a one-time, pay-what-you-want product (tax inclusive)
	HTTP      *http.Client
}

// Name implements core.Provider.
func (d *Dodo) Name() string { return "dodo" }

func (d *Dodo) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("dodo %s %s: %d %s", method, path, resp.StatusCode, truncate(string(b), 300))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CreateCheckout opens a hosted checkout for exactly the reserved amount.
func (d *Dodo) CreateCheckout(ctx context.Context, r core.CheckoutRequest) (core.Checkout, error) {
	body := map[string]any{
		"product_cart": []map[string]any{{"product_id": d.ProductID, "quantity": 1, "amount": r.PriceCents}},
		"return_url":   r.ReturnURL,
		"cancel_url":   r.CancelURL,
		"metadata": map[string]string{
			"reservation_id": r.ReservationID,
			"round":          strconv.Itoa(r.Round),
			"price_cents":    strconv.FormatInt(r.PriceCents, 10),
			"purpose":        "spotlight_placement",
		},
		"allowed_payment_method_types": []string{"credit", "debit", "apple_pay", "google_pay"},
		"customization":                map[string]any{"show_order_details": true, "theme": "dark"},
	}
	var out struct {
		SessionID   string `json:"session_id"`
		CheckoutURL string `json:"checkout_url"`
	}
	if err := d.do(ctx, http.MethodPost, "/checkouts", body, &out); err != nil {
		return core.Checkout{}, err
	}
	if out.CheckoutURL == "" {
		return core.Checkout{}, errors.New("dodo returned no checkout_url")
	}
	return core.Checkout{SessionID: out.SessionID, URL: out.CheckoutURL}, nil
}

// Refund refunds a whole payment.
func (d *Dodo) Refund(ctx context.Context, paymentID, reason string) (string, error) {
	var out struct {
		RefundID string `json:"refund_id"`
	}
	err := d.do(ctx, http.MethodPost, "/refunds", map[string]any{"payment_id": paymentID, "reason": reason}, &out)
	return out.RefundID, err
}

// DodoPayment is the part of Dodo's Payment object we use.
type DodoPayment struct {
	PaymentID         string            `json:"payment_id"`
	Status            string            `json:"status"`
	TotalAmount       int64             `json:"total_amount"`
	Currency          string            `json:"currency"`
	Metadata          map[string]string `json:"metadata"`
	CheckoutSessionID string            `json:"checkout_session_id"`
	// refund events
	RefundID string `json:"refund_id"`
	Amount   int64  `json:"amount"`
}

// GetPayment fetches a payment server-to-server (used when the buyer comes
// back before the webhook, and by the admin reconcile button).
func (d *Dodo) GetPayment(ctx context.Context, id string) (*DodoPayment, error) {
	var p DodoPayment
	if err := d.do(ctx, http.MethodGet, "/payments/"+id, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Envelope is a Dodo webhook body.
type Envelope struct {
	BusinessID string          `json:"business_id"`
	Type       string          `json:"type"`
	Timestamp  time.Time       `json:"timestamp"`
	Data       json.RawMessage `json:"data"`
}

// ParseWebhook verifies the Standard Webhooks signature, then converts the
// body into a provider-neutral event. Unsigned or stale requests fail.
func ParseWebhook(secret string, body []byte, h http.Header) (core.PaymentEvent, error) {
	wh, err := standardwebhooks.NewWebhook(secret)
	if err != nil {
		return core.PaymentEvent{}, fmt.Errorf("bad webhook secret: %w", err)
	}
	if err := wh.Verify(body, h); err != nil {
		return core.PaymentEvent{}, fmt.Errorf("signature: %w", err)
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return core.PaymentEvent{}, err
	}
	var p DodoPayment
	if err := json.Unmarshal(env.Data, &p); err != nil {
		return core.PaymentEvent{}, err
	}
	return ToEvent(h.Get("webhook-id"), env.Type, &p, body), nil
}

// ToEvent maps a Dodo payment/refund object to a core event.
func ToEvent(eventID, typ string, p *DodoPayment, raw []byte) core.PaymentEvent {
	ev := core.PaymentEvent{
		Provider:      "dodo",
		EventID:       eventID,
		Type:          typ,
		PaymentID:     p.PaymentID,
		SessionID:     p.CheckoutSessionID,
		ReservationID: p.Metadata["reservation_id"],
		AmountCents:   p.TotalAmount,
		Currency:      p.Currency,
		RefundID:      p.RefundID,
		Raw:           raw,
	}
	if len(typ) > 7 && typ[:7] == "refund." {
		ev.AmountCents = p.Amount
	}
	return ev
}

// StatusToType maps a fetched payment status to the event type it implies.
func StatusToType(status string) string {
	switch status {
	case "succeeded":
		return "payment.succeeded"
	case "failed":
		return "payment.failed"
	case "cancelled":
		return "payment.cancelled"
	case "processing", "requires_customer_action", "requires_capture", "requires_confirmation":
		return "payment.processing"
	}
	return ""
}

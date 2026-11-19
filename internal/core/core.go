// Package core holds the spotlight rules: drafts, holds, payments,
// publication, refunds and votes. Every money-moving change runs inside a
// Postgres transaction that locks the single spotlight row.
package core

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Kinds of post.
const (
	KindOpinion = "opinion"
	KindCreator = "creator"
	KindProduct = "product"
)

// Post is a spotlight entry.
type Post struct {
	ID            int64
	Slug          string
	Kind          string
	Headline      string
	Body          string
	LinkURL       string
	LinkHost      string
	EmbedProvider string
	EmbedRef      string
	AuthorName    string
	ContactEmail  string
	ReplyToID     *int64
	Status        string
	ReviewNotes   string
	Round         *int
	PriceCents    int64
	PublishedAt   *time.Time
	DethronedAt   *time.Time
	RemovedAt     *time.Time
	RemovedReason string
	EditToken     string
	IsTest        bool
	CreatedAt     time.Time

	// Filled by queries that aggregate.
	Up, Down int
	ReplyTo  *Post
}

// TimeOnTop is how long the post held (or has held) the spotlight.
func (p *Post) TimeOnTop(now time.Time) time.Duration {
	if p.PublishedAt == nil {
		return 0
	}
	end := now
	if p.DethronedAt != nil {
		end = *p.DethronedAt
	}
	return end.Sub(*p.PublishedAt)
}

// OnTop reports whether this post holds the spotlight now.
func (p *Post) OnTop() bool {
	return p.Status == "published" && p.PublishedAt != nil && p.DethronedAt == nil
}

// Hold is a reservation.
type Hold struct {
	ID                string
	PostID            int64
	Round             int
	PriceCents        int64
	Status            string
	Provider          string
	ProviderSessionID string
	CheckoutURL       string
	ExpiresAt         time.Time
	CreatedAt         time.Time
}

// State is the public spotlight state.
type State struct {
	RoundsPublished int
	Current         *Post
	NextRound       int
	NextPriceCents  int64
	ActiveHold      *Hold
	OverLimit       bool
}

// Checkout is what a payment provider returns for a new session.
type Checkout struct {
	SessionID string
	URL       string
}

// CheckoutRequest is what core asks a provider for.
type CheckoutRequest struct {
	ReservationID string
	Round         int
	PriceCents    int64
	Description   string
	ReturnURL     string
	CancelURL     string
}

// Provider is the subset of a payment provider that core needs.
type Provider interface {
	Name() string
	CreateCheckout(ctx context.Context, req CheckoutRequest) (Checkout, error)
	Refund(ctx context.Context, paymentID string, reason string) (refundID string, err error)
}

// Config for the service.
type Config struct {
	HoldDuration     time.Duration // exclusive checkout window
	MaxCheckoutCents int64         // provider per-transaction ceiling
	IsTest           bool          // label everything as test data
	BaseURL          string
}

// Service implements the game.
type Service struct {
	DB       *pgxpool.Pool
	Provider Provider
	Cfg      Config
	Log      *slog.Logger
	Now      func() time.Time
	// RefundKick is signalled after a commit that queued a refund.
	RefundKick chan struct{}
}

// New returns a service.
func New(db *pgxpool.Pool, p Provider, cfg Config, log *slog.Logger) *Service {
	if cfg.HoldDuration == 0 {
		cfg.HoldDuration = 15 * time.Minute
	}
	return &Service{DB: db, Provider: p, Cfg: cfg, Log: log, Now: time.Now, RefundKick: make(chan struct{}, 1)}
}

// Errors returned to the web layer.
var (
	ErrNotFound    = errors.New("not found")
	ErrNotApproved = errors.New("this post is waiting for review and cannot be paid for yet")
	ErrAlreadyDone = errors.New("this post is already published")
	ErrOverLimit   = errors.New("the next price is above the card processor's per-payment limit")
)

// StaleQuoteError means the displayed price no longer applies.
type StaleQuoteError struct{ Quoted, Current int64 }

func (e *StaleQuoteError) Error() string {
	return fmt.Sprintf("price changed from %s to %s", FormatUSD(e.Quoted), FormatUSD(e.Current))
}

// HeldError means someone else holds the checkout window.
type HeldError struct{ Until time.Time }

func (e *HeldError) Error() string { return "another buyer is checking out" }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newSlug() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// NewUUID returns a random v4 UUID string.
func NewUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

const postCols = `p.id, p.slug, p.kind, p.headline, p.body, coalesce(p.link_url,''), coalesce(p.link_host,''),
	coalesce(p.embed_provider,''), coalesce(p.embed_ref,''), p.author_name, coalesce(p.contact_email,''), p.reply_to_id,
	p.status, p.review_notes, p.round, coalesce(p.price_cents,0), p.published_at, p.dethroned_at, p.removed_at,
	coalesce(p.removed_reason,''), p.edit_token, p.is_test, p.created_at`

func scanPost(row pgx.Row, extra ...any) (*Post, error) {
	p := &Post{}
	dest := []any{&p.ID, &p.Slug, &p.Kind, &p.Headline, &p.Body, &p.LinkURL, &p.LinkHost,
		&p.EmbedProvider, &p.EmbedRef, &p.AuthorName, &p.ContactEmail, &p.ReplyToID,
		&p.Status, &p.ReviewNotes, &p.Round, &p.PriceCents, &p.PublishedAt, &p.DethronedAt, &p.RemovedAt,
		&p.RemovedReason, &p.EditToken, &p.IsTest, &p.CreatedAt}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

// DraftInput is a validated submission.
type DraftInput struct {
	Kind, Headline, Body, LinkURL, LinkHost, EmbedProvider, EmbedRef string
	AuthorName, ContactEmail                                         string
	ReplyToID                                                        *int64
	ReviewReason                                                     string // non-empty means hold for a human
	Visitor                                                          string
}

// CreateDraft stores a submission. Moderation happens here, before payment.
func (s *Service) CreateDraft(ctx context.Context, in DraftInput) (*Post, error) {
	status := "approved"
	if in.ReviewReason != "" {
		status = "needs_review"
	}
	row := s.DB.QueryRow(ctx, `INSERT INTO posts (slug, kind, headline, body, link_url, link_host, embed_provider, embed_ref,
		author_name, contact_email, reply_to_id, status, review_notes, edit_token, visitor, is_test)
		VALUES ($1,$2,$3,$4,nullif($5,''),nullif($6,''),nullif($7,''),nullif($8,''),$9,nullif($10,''),$11,$12,$13,$14,$15,$16)
		RETURNING id`,
		newSlug(), in.Kind, in.Headline, in.Body, in.LinkURL, in.LinkHost, in.EmbedProvider, in.EmbedRef,
		in.AuthorName, in.ContactEmail, in.ReplyToID, status, in.ReviewReason, randToken(16), in.Visitor, s.Cfg.IsTest)
	var id int64
	if err := row.Scan(&id); err != nil {
		return nil, err
	}
	return s.PostByID(ctx, id)
}

// PostByID loads a post with vote totals.
func (s *Service) PostByID(ctx context.Context, id int64) (*Post, error) {
	return s.onePost(ctx, `p.id = $1`, id)
}

// PostBySlug loads a post with vote totals.
func (s *Service) PostBySlug(ctx context.Context, slug string) (*Post, error) {
	return s.onePost(ctx, `p.slug = $1`, slug)
}

// PostByToken loads a post by its private edit token.
func (s *Service) PostByToken(ctx context.Context, token string) (*Post, error) {
	if len(token) != 32 {
		return nil, ErrNotFound
	}
	return s.onePost(ctx, `p.edit_token = $1`, token)
}

func (s *Service) onePost(ctx context.Context, where string, arg any) (*Post, error) {
	q := `SELECT ` + postCols + `,
		(SELECT count(*) FROM votes v WHERE v.post_id = p.id AND v.value = 1),
		(SELECT count(*) FROM votes v WHERE v.post_id = p.id AND v.value = -1)
		FROM posts p WHERE ` + where
	var up, down int
	p, err := scanPost(s.DB.QueryRow(ctx, q, arg), &up, &down)
	if err != nil {
		return nil, err
	}
	p.Up, p.Down = up, down
	if p.ReplyToID != nil {
		if parent, err := s.onePost(ctx, `p.id = $1`, *p.ReplyToID); err == nil {
			parent.ReplyTo = nil
			p.ReplyTo = parent
		}
	}
	return p, nil
}

// History returns published posts newest first, excluding the current one.
func (s *Service) History(ctx context.Context, limit int) ([]*Post, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+postCols+`,
		(SELECT count(*) FROM votes v WHERE v.post_id = p.id AND v.value = 1),
		(SELECT count(*) FROM votes v WHERE v.post_id = p.id AND v.value = -1)
		FROM posts p WHERE p.round IS NOT NULL AND p.dethroned_at IS NOT NULL
		ORDER BY p.round DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		var up, down int
		p, err := scanPost(rows, &up, &down)
		if err != nil {
			return nil, err
		}
		p.Up, p.Down = up, down
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetState returns the current spotlight and the next price.
func (s *Service) GetState(ctx context.Context) (*State, error) {
	st := &State{}
	var cur *int64
	if err := s.DB.QueryRow(ctx, `SELECT rounds_published, current_post_id FROM spotlight WHERE id = 1`).Scan(&st.RoundsPublished, &cur); err != nil {
		return nil, err
	}
	st.NextRound = st.RoundsPublished
	price, err := PriceForRound(st.NextRound)
	if err != nil {
		st.OverLimit = true
	}
	st.NextPriceCents = price
	if s.Cfg.MaxCheckoutCents > 0 && price > s.Cfg.MaxCheckoutCents {
		st.OverLimit = true
	}
	if cur != nil {
		p, err := s.PostByID(ctx, *cur)
		if err != nil {
			return nil, err
		}
		st.Current = p
	}
	h, err := s.activeHold(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	st.ActiveHold = h
	return st, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const holdCols = `id::text, post_id, round, price_cents, status, provider, coalesce(provider_session_id,''), coalesce(checkout_url,''), expires_at, created_at`

func scanHold(row pgx.Row) (*Hold, error) {
	h := &Hold{}
	if err := row.Scan(&h.ID, &h.PostID, &h.Round, &h.PriceCents, &h.Status, &h.Provider, &h.ProviderSessionID, &h.CheckoutURL, &h.ExpiresAt, &h.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return h, nil
}

func (s *Service) activeHold(ctx context.Context, q querier) (*Hold, error) {
	h, err := scanHold(q.QueryRow(ctx, `SELECT `+holdCols+` FROM reservations WHERE status = 'active' AND expires_at > $1`, s.Now()))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return h, err
}

// HoldByID loads a reservation.
func (s *Service) HoldByID(ctx context.Context, id string) (*Hold, error) {
	if len(id) != 36 {
		return nil, ErrNotFound
	}
	return scanHold(s.DB.QueryRow(ctx, `SELECT `+holdCols+` FROM reservations WHERE id = $1::uuid`, id))
}

// LatestHoldForPost returns the newest reservation of a post, if any.
func (s *Service) LatestHoldForPost(ctx context.Context, postID int64) (*Hold, error) {
	return scanHold(s.DB.QueryRow(ctx, `SELECT `+holdCols+` FROM reservations WHERE post_id = $1 ORDER BY created_at DESC LIMIT 1`, postID))
}

// StartResult tells the web layer where to send the buyer.
type StartResult struct {
	Published   bool   // founding post published for free
	CheckoutURL string // provider checkout
	Hold        *Hold
}

// StartCheckout reserves the next round for this draft at exactly the quoted
// price, then opens a provider checkout for that price.
func (s *Service) StartCheckout(ctx context.Context, editToken string, quotedCents int64, visitor string) (*StartResult, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var rounds int
	if err := tx.QueryRow(ctx, `SELECT rounds_published FROM spotlight WHERE id = 1 FOR UPDATE`).Scan(&rounds); err != nil {
		return nil, err
	}
	post, err := scanPost(tx.QueryRow(ctx, `SELECT `+postCols+` FROM posts p WHERE p.edit_token = $1 FOR UPDATE`, editToken))
	if err != nil {
		return nil, err
	}
	switch post.Status {
	case "approved":
	case "published", "removed":
		return nil, ErrAlreadyDone
	default:
		return nil, ErrNotApproved
	}
	price, err := PriceForRound(rounds)
	if err != nil {
		return nil, ErrOverLimit
	}
	if price != quotedCents {
		return nil, &StaleQuoteError{Quoted: quotedCents, Current: price}
	}
	now := s.Now()
	if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'expired', updated_at = now() WHERE status = 'active' AND expires_at <= $1`, now); err != nil {
		return nil, err
	}
	if h, err := s.activeHold(ctx, tx); err != nil {
		return nil, err
	} else if h != nil {
		if h.PostID == post.ID && h.Round == rounds && h.CheckoutURL != "" {
			// Same buyer came back (refresh, back button): reuse the session.
			tx.Commit(ctx)
			return &StartResult{CheckoutURL: h.CheckoutURL, Hold: h}, nil
		}
		return nil, &HeldError{Until: h.ExpiresAt}
	}

	if price == 0 {
		// The founding post: free, exactly once (round 0 is unique).
		if err := s.publishTx(ctx, tx, post.ID, rounds, 0); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		s.Log.Info("founding post published", "post", post.Slug)
		return &StartResult{Published: true}, nil
	}
	if s.Cfg.MaxCheckoutCents > 0 && price > s.Cfg.MaxCheckoutCents {
		return nil, ErrOverLimit
	}

	hold := &Hold{ID: NewUUID(), PostID: post.ID, Round: rounds, PriceCents: price, Status: "active",
		Provider: s.Provider.Name(), ExpiresAt: now.Add(s.Cfg.HoldDuration), CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO reservations (id, post_id, round, price_cents, status, provider, visitor, expires_at, is_test)
		VALUES ($1::uuid, $2, $3, $4, 'active', $5, $6, $7, $8)`,
		hold.ID, hold.PostID, hold.Round, hold.PriceCents, hold.Provider, visitor, hold.ExpiresAt, s.Cfg.IsTest); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	co, err := s.Provider.CreateCheckout(ctx, CheckoutRequest{
		ReservationID: hold.ID,
		Round:         hold.Round,
		PriceCents:    hold.PriceCents,
		Description:   fmt.Sprintf("TopForNow spotlight placement (round %d)", hold.Round),
		ReturnURL:     s.Cfg.BaseURL + "/checkout/return?r=" + hold.ID,
		CancelURL:     s.Cfg.BaseURL + "/draft/" + editToken,
	})
	if err != nil {
		s.DB.Exec(ctx, `UPDATE reservations SET status = 'cancelled', updated_at = now() WHERE id = $1::uuid`, hold.ID)
		return nil, fmt.Errorf("could not open checkout: %w", err)
	}
	hold.ProviderSessionID, hold.CheckoutURL = co.SessionID, co.URL
	if _, err := s.DB.Exec(ctx, `UPDATE reservations SET provider_session_id = $2, checkout_url = $3, updated_at = now() WHERE id = $1::uuid`,
		hold.ID, co.SessionID, co.URL); err != nil {
		return nil, err
	}
	s.Log.Info("hold created", "hold", hold.ID, "round", hold.Round, "price", hold.PriceCents)
	return &StartResult{CheckoutURL: co.URL, Hold: hold}, nil
}

// CancelHold releases the buyer's own active hold (they pressed back/cancel).
func (s *Service) CancelHold(ctx context.Context, editToken string) error {
	_, err := s.DB.Exec(ctx, `UPDATE reservations r SET status = 'cancelled', updated_at = now()
		FROM posts p WHERE r.post_id = p.id AND p.edit_token = $1 AND r.status = 'active'
		AND NOT EXISTS (SELECT 1 FROM payments pm WHERE pm.reservation_id = r.id AND pm.status IN ('processing','succeeded'))`, editToken)
	return err
}

// publishTx puts a post on top for a round. Caller holds the spotlight lock.
func (s *Service) publishTx(ctx context.Context, tx pgx.Tx, postID int64, round int, paidCents int64) error {
	now := s.Now()
	if _, err := tx.Exec(ctx, `UPDATE posts SET dethroned_at = $1 WHERE id = (SELECT current_post_id FROM spotlight WHERE id = 1) AND dethroned_at IS NULL`, now); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE posts SET status = 'published', round = $2, price_cents = $3, published_at = $4 WHERE id = $1 AND status = 'approved'`,
		postID, round, paidCents, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("post %d not publishable", postID)
	}
	tag, err = tx.Exec(ctx, `UPDATE spotlight SET rounds_published = rounds_published + 1, current_post_id = $1, updated_at = $2
		WHERE id = 1 AND rounds_published = $3`, postID, now, round)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("round %d already taken", round)
	}
	_, err = tx.Exec(ctx, `INSERT INTO events (type, post_id, is_test) VALUES ('publish', $1, $2)`, postID, s.Cfg.IsTest)
	return err
}

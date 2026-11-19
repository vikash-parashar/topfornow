package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vikash-parashar/topfornow/internal/core"
	"github.com/vikash-parashar/topfornow/internal/db"
	"github.com/vikash-parashar/topfornow/internal/pay"
)

// Run with TEST_DATABASE_URL pointing at a throwaway database. The test
// drops and recreates the public schema.
func setup(t *testing.T) (*httptest.Server, *Server, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("VERBOSE") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	srv := &Server{Mode: pay.ModeSim, Secret: []byte("test-secret-test-secret-test-secret!"), AdminPassword: "owner-pw", Log: log}
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	base := ts.URL
	sim := &pay.Sim{DB: pool, BaseURL: base, WebhookURL: base + "/webhooks/dodo", Secret: "whsec_dGVzdC1zYW5kYm94LXNlY3JldC0xMjM0NTY3OA==", Log: log, HTTP: &http.Client{Timeout: 5 * time.Second}}
	srv.Sim, srv.Fetcher, srv.WebhookSecret, srv.BaseURL = sim, sim, sim.Secret, base
	srv.Svc = core.New(pool, sim, core.Config{HoldDuration: 15 * time.Minute, MaxCheckoutCents: 500000, IsTest: true, BaseURL: base}, log)
	h, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = h
	go srv.Svc.RunRefundWorker(ctx)
	t.Cleanup(func() { ts.Close(); pool.Close() })
	return ts, srv, pool
}

type browser struct {
	t    *testing.T
	c    *http.Client
	base string
}

func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (b *browser) do(method, path string, form url.Values) (*http.Response, string) {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := path
	if strings.HasPrefix(path, "/") {
		u = b.base + path
	}
	req, _ := http.NewRequest(method, u, body)
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/604.1")
	req.Header.Set("Accept-Language", "en")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	bs, _ := io.ReadAll(resp.Body)
	return resp, string(bs)
}

var csrfRe = regexp.MustCompile(`name="csrf" content="([0-9a-f]+)"`)

func (b *browser) csrf() string {
	_, html := b.do("GET", "/rules", nil)
	m := csrfRe.FindStringSubmatch(html)
	if m == nil {
		b.t.Fatal("no csrf")
	}
	return m[1]
}

var draftRe = regexp.MustCompile(`/draft/([0-9a-f]{32})`)

// submit creates a draft and returns its edit token.
func (b *browser) submit(kind, headline, link, author string) string {
	b.t.Helper()
	resp, html := b.do("POST", "/submit", url.Values{"csrf": {b.csrf()}, "kind": {kind}, "headline": {headline}, "link": {link}, "author": {author}, "agree": {"yes"}})
	m := draftRe.FindStringSubmatch(resp.Request.URL.Path)
	if m == nil {
		b.t.Fatalf("submit failed: %d %s", resp.StatusCode, firstMatch(html, `class="error"[^>]*>([^<]+)`))
	}
	return m[1]
}

func firstMatch(s, re string) string {
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// checkout presses pay at the quoted price. Returns final response.
func (b *browser) checkout(token string, quoted int64) (*http.Response, string) {
	return b.do("POST", "/draft/"+token+"/checkout", url.Values{"csrf": {b.csrf()}, "quoted_cents": {fmt.Sprint(quoted)}})
}

func (b *browser) state() map[string]any {
	_, body := b.do("GET", "/api/state", nil)
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	return m
}

func nextPrice(b *browser) int64 { return int64(b.state()["next_price_cents"].(float64)) }

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var simRe = regexp.MustCompile(`/sandbox/checkout/(cks_sim_[0-9a-f]+)`)

// buy runs the full paid flow: draft -> hold -> sandbox checkout -> pay.
func (b *browser) buy(kind, headline, link, author string, price int64) string {
	b.t.Helper()
	tok := b.submit(kind, headline, link, author)
	resp, _ := b.checkout(tok, price)
	m := simRe.FindStringSubmatch(resp.Request.URL.Path)
	if m == nil {
		b.t.Fatalf("expected sandbox checkout for %s, landed on %s", headline, resp.Request.URL)
	}
	resp, _ = b.do("POST", "/sandbox/checkout/"+m[1], url.Values{"csrf": {b.csrf()}, "action": {"pay"}, "deliveries": {"1"}})
	if !strings.HasPrefix(resp.Request.URL.Path, "/checkout/return") {
		b.t.Fatalf("expected return page, got %s", resp.Request.URL)
	}
	return tok
}

func TestAcceptance(t *testing.T) {
	ts, srv, pool := setup(t)
	ctx := context.Background()
	alice := newBrowser(t, ts.URL)  // founding opinion
	voter1 := newBrowser(t, ts.URL) // free voters
	voter2 := newBrowser(t, ts.URL)

	// 1. A free opinion starts on top.
	if p := nextPrice(alice); p != 0 {
		t.Fatalf("founding price should be 0, got %d", p)
	}
	tokOpinion := alice.submit("opinion", "Pineapple belongs on pizza", "", "Alice")
	resp, html := alice.checkout(tokOpinion, 0)
	if !strings.Contains(resp.Request.URL.String(), "n=founding") || !strings.Contains(html, "I took the first spotlight for free.") {
		t.Fatalf("founding publish failed: %s", resp.Request.URL)
	}
	opinion, _ := srv.Svc.PostByToken(ctx, tokOpinion)
	if *opinion.Round != 0 || opinion.PriceCents != 0 {
		t.Fatal("founding post round/price wrong")
	}
	// Free post cannot be taken twice: a second founding attempt is stale.
	tokSneaky := voter1.submit("opinion", "Me too, for free", "", "Sneaky")
	if resp, _ := voter1.checkout(tokSneaky, 0); resp.StatusCode != 409 {
		t.Fatalf("second free post should be refused with stale quote, got %d", resp.StatusCode)
	}

	// Votes on the opinion.
	for _, v := range []*browser{voter1, voter2} {
		if r, _ := v.do("POST", "/vote", url.Values{"csrf": {v.csrf()}, "slug": {opinion.Slug}, "value": {"1"}}); r.StatusCode != 200 {
			t.Fatalf("vote failed %d", r.StatusCode)
		}
	}
	voter2.do("POST", "/vote", url.Values{"csrf": {voter2.csrf()}, "slug": {opinion.Slug}, "value": {"-1"}}) // changes mind
	if np := nextPrice(alice); np != 100 {
		t.Fatalf("after founding, next price must be $1, got %d", np)
	}

	// 2. A $1 creator video replaces it.
	creator := newBrowser(t, ts.URL)
	tokVideo := creator.buy("creator", "Watch me fold a fitted sheet", "https://youtu.be/dQw4w9WgXcQ", "SheetTok", 100)
	waitFor(t, "video published", func() bool { return nextPrice(alice) == 200 })
	video, _ := srv.Svc.PostByToken(ctx, tokVideo)
	voter1.do("POST", "/vote", url.Values{"csrf": {voter1.csrf()}, "slug": {video.Slug}, "value": {"1"}})
	voter1.do("GET", "/p/"+video.Slug, nil)
	voter1.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	voter1.do("GET", "/go/"+video.Slug, nil)
	voter1.c.CheckRedirect = nil

	// 3. A $2 product replaces the video.
	company := newBrowser(t, ts.URL)
	tokProduct := company.buy("product", "Invoicing for freelancers in 30 seconds", "https://example.com/?utm_source=spam&plan=free", "Acme", 200)
	waitFor(t, "product published", func() bool { return nextPrice(alice) == 400 })
	product, _ := srv.Svc.PostByToken(ctx, tokProduct)

	// 4. Next price is $4 and prior posts keep their own votes and results.
	opinion, _ = srv.Svc.PostBySlug(ctx, opinion.Slug)
	video, _ = srv.Svc.PostBySlug(ctx, video.Slug)
	if opinion.Up != 1 || opinion.Down != 1 || video.Up != 1 || video.Down != 0 {
		t.Fatalf("votes not retained: opinion %d/%d video %d/%d", opinion.Up, opinion.Down, video.Up, video.Down)
	}
	if opinion.DethronedAt == nil || video.DethronedAt == nil || product.DethronedAt != nil {
		t.Fatal("dethroned timestamps wrong")
	}
	res, _ := srv.Svc.ResultsFor(ctx, video)
	if res.OutboundClick != 1 || res.PostViews != 1 {
		t.Fatalf("video results not retained: %+v", res)
	}
	t.Logf("after $2: next price %d, opinion %d/%d, video %d/%d clicks=%d", nextPrice(alice), opinion.Up, opinion.Down, video.Up, video.Down, res.OutboundClick)

	// 5. Replay the $2 payment's webhook (same webhook-id) and the same
	// payment under a new webhook-id: nothing changes.
	var pid string
	var amount int64
	pool.QueryRow(ctx, `SELECT provider_payment_id, amount_cents FROM payments WHERE post_id = $1`, product.ID).Scan(&pid, &amount)
	var sessID string
	var resID string
	pool.QueryRow(ctx, `SELECT id, metadata->>'reservation_id' FROM sim_sessions WHERE payment_id = $1`, pid).Scan(&sessID, &resID)
	var origMsg string
	pool.QueryRow(ctx, `SELECT event_id FROM webhook_events WHERE payload->'data'->>'payment_id' = $1 AND event_type = 'payment.succeeded'`, pid).Scan(&origMsg)
	dp := pay.DodoPayment{PaymentID: pid, Status: "succeeded", TotalAmount: amount, Currency: "USD", Metadata: map[string]string{"reservation_id": resID}, CheckoutSessionID: sessID}
	if origMsg == "" {
		t.Fatal("original webhook not stored")
	}
	srv.Sim.Deliver(ctx, origMsg, "payment.succeeded", dp)
	srv.Sim.Deliver(ctx, "msg_replay_new_id", "payment.succeeded", dp)
	// Forged (unsigned) webhook is rejected.
	r, _ := http.Post(ts.URL+"/webhooks/dodo", "application/json", strings.NewReader(`{"type":"payment.succeeded","data":{}}`))
	if r.StatusCode != 401 {
		t.Fatalf("unsigned webhook accepted: %d", r.StatusCode)
	}
	if np := nextPrice(alice); np != 400 {
		t.Fatalf("replay changed price to %d", np)
	}
	var npay int
	pool.QueryRow(ctx, `SELECT count(*) FROM payments WHERE provider_payment_id = $1`, pid).Scan(&npay)
	if npay != 1 {
		t.Fatalf("replay created %d payment rows", npay)
	}

	// 6. Competing checkouts at $4.
	buyerA, buyerB := newBrowser(t, ts.URL), newBrowser(t, ts.URL)
	tokA := buyerA.submit("product", "Buyer A's app", "https://a.example.com", "A")
	tokB := buyerB.submit("opinion", "Buyer B says hi", "", "B")
	respA, _ := buyerA.checkout(tokA, 400)
	mA := simRe.FindStringSubmatch(respA.Request.URL.Path)
	if mA == nil {
		t.Fatal("A should get checkout")
	}
	if respB, body := buyerB.checkout(tokB, 400); respB.StatusCode != 409 || !strings.Contains(body, "Someone is checking out") {
		t.Fatalf("B should be told A holds the round, got %d", respB.StatusCode)
	}
	// A's browser refresh reuses the same session (no second hold).
	if again, _ := buyerA.checkout(tokA, 400); !strings.Contains(again.Request.URL.Path, mA[1]) {
		t.Fatal("A's retry should reuse the same checkout")
	}
	// A's card is declined first: nothing published, hold kept.
	buyerA.do("POST", "/sandbox/checkout/"+mA[1], url.Values{"csrf": {buyerA.csrf()}, "action": {"decline"}})
	time.Sleep(300 * time.Millisecond)
	if np := nextPrice(alice); np != 400 {
		t.Fatalf("failed payment changed price: %d", np)
	}
	// A walks away (closed browser) and the hold expires.
	pool.Exec(ctx, `UPDATE reservations SET expires_at = now() - interval '1 second' WHERE status = 'active'`)
	respB, _ := buyerB.checkout(tokB, 400)
	mB := simRe.FindStringSubmatch(respB.Request.URL.Path)
	if mB == nil {
		t.Fatalf("B should get checkout after A's hold expired: %s", respB.Request.URL)
	}
	// A comes back and pays late on the old session, browser closed (webhook
	// only). The round is still open, so A's paid round publishes, and B's
	// hold is marked lost.
	buyerA.do("POST", "/sandbox/checkout/"+mA[1], url.Values{"csrf": {buyerA.csrf()}, "action": {"pay"}, "skip_return_verify": {"1"}})
	waitFor(t, "late A publishes", func() bool { return nextPrice(alice) == 800 })
	// B pays too, at the $4 shown: must be refunded in full automatically.
	buyerB.do("POST", "/sandbox/checkout/"+mB[1], url.Values{"csrf": {buyerB.csrf()}, "action": {"pay"}})
	waitFor(t, "B refunded", func() bool {
		var st string
		pool.QueryRow(ctx, `SELECT p.status FROM payments p JOIN reservations r ON r.id = p.reservation_id JOIN posts x ON x.id = r.post_id WHERE x.edit_token = $1`, tokB).Scan(&st)
		return st == "refunded"
	})
	if np := nextPrice(alice); np != 800 {
		t.Fatalf("B's payment changed price: %d", np)
	}
	postB, _ := srv.Svc.PostByToken(ctx, tokB)
	if postB.Round != nil {
		t.Fatal("B must not be published")
	}

	// 7. Stale quote: someone holding a $4 quote after the price moved.
	tokStale := buyerB.submit("opinion", "Stale quote test", "", "B")
	if resp, body := buyerB.checkout(tokStale, 400); resp.StatusCode != 409 || !strings.Contains(body, "The price just changed") {
		t.Fatalf("stale quote not detected: %d", resp.StatusCode)
	}

	// 8. Twenty buyers press pay at $8 at the same moment: exactly one hold.
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, _ := srv.Svc.CreateDraft(ctx, core.DraftInput{Kind: "opinion", Headline: fmt.Sprintf("racer %d", i), AuthorName: "racer"})
			if r, err := srv.Svc.StartCheckout(ctx, p.EditToken, 800, "v"); err == nil && r.CheckoutURL != "" {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if got != 1 {
		t.Fatalf("expected exactly one hold among racers, got %d", got)
	}
	pool.Exec(ctx, `UPDATE reservations SET status = 'cancelled' WHERE status = 'active'`)

	// 9. Continue the sequence: $8, then $16.
	tok8 := newBrowser(t, ts.URL).buy("opinion", "Tabs are better than spaces", "", "Indent Club", 800)
	waitFor(t, "$8 published", func() bool { return nextPrice(alice) == 1600 })
	sharer := newBrowser(t, ts.URL)
	tok16 := sharer.buy("product", "The $16 test product", "https://sixteen.example.com", "Sixteen Inc", 1600)
	waitFor(t, "$16 published", func() bool { return nextPrice(alice) == 3200 })
	p16, _ := srv.Svc.PostByToken(ctx, tok16)
	_, draftHTML := sharer.do("GET", "/draft/"+tok16, nil)
	const want = "I paid $16 to make you read this. It worked."
	if !strings.Contains(draftHTML, "Share my Ego Receipt") || !strings.Contains(draftHTML, want) {
		t.Fatal("ego receipt share block missing")
	}
	if core.EgoReceiptHeadline(p16.PriceCents) != want {
		t.Fatalf("receipt headline wrong: %q", core.EgoReceiptHeadline(p16.PriceCents))
	}
	_ = tok8

	// 10. Replace once more at $32, then open the $16 shared link.
	newBrowser(t, ts.URL).buy("creator", "After sixteen", "https://www.instagram.com/p/Cabc12345/", "IG Person", 3200)
	waitFor(t, "$32 published", func() bool { return nextPrice(alice) == 6400 })
	reader := newBrowser(t, ts.URL)
	resp, postHTML := reader.do("GET", "/p/"+p16.Slug+"?ref=receipt", nil)
	if resp.StatusCode != 200 || !strings.Contains(postHTML, "The $16 test product") || !strings.Contains(postHTML, "See what's on top now") ||
		!strings.Contains(postHTML, "/receipt/"+p16.Slug+"?format=og") {
		t.Fatalf("$16 share link broken after replacement: %d %s", resp.StatusCode, postHTML)
	}
	if !strings.Contains(postHTML, "og:description\" content=\"I paid $16 to make you read this. It worked.") {
		t.Fatal("social preview description must carry the $16 sentence")
	}
	img, _ := reader.do("GET", "/receipt/"+p16.Slug+"?dl=1", nil)
	if img.Header.Get("Content-Type") != "image/png" || !strings.Contains(img.Header.Get("Content-Disposition"), "ego-receipt-16.png") {
		t.Fatalf("receipt download wrong: %v", img.Header)
	}
	// Reader can still vote on the replaced $16 post.
	if r, _ := reader.do("POST", "/vote", url.Values{"csrf": {reader.csrf()}, "slug": {p16.Slug}, "value": {"1"}}); r.StatusCode != 200 {
		t.Fatal("cannot vote on replaced post")
	}
	// Outbound link is tracked and tagged.
	reader.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r2, _ := reader.do("GET", "/go/"+p16.Slug, nil)
	if loc := r2.Header.Get("Location"); !strings.HasPrefix(loc, "https://sixteen.example.com") || !strings.Contains(loc, "utm_source=topfornow") {
		t.Fatalf("outbound redirect wrong: %s", loc)
	}
	reader.c.CheckRedirect = nil

	// 11. Admin is protected.
	anon := newBrowser(t, ts.URL)
	anon.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if r, _ := anon.do("GET", "/admin", nil); r.StatusCode != 303 {
		t.Fatalf("admin not protected: %d", r.StatusCode)
	}
	if r, _ := anon.do("POST", "/admin/login", url.Values{"csrf": {anon.csrf()}, "password": {"wrong"}}); r.StatusCode != 401 {
		t.Fatal("wrong password accepted")
	}
	anon.c.CheckRedirect = nil
	if _, body := anon.do("POST", "/admin/login", url.Values{"csrf": {anon.csrf()}, "password": {"owner-pw"}}); !strings.Contains(body, "All invariants hold") {
		t.Fatal("admin dashboard / reconciliation not clean")
	}

	// 12. Ledger consistency.
	rec, _ := srv.Svc.Reconcile(ctx)
	if len(rec.Problems) > 0 {
		t.Fatalf("reconcile problems: %v", rec.Problems)
	}
	var rounds, posts int
	pool.QueryRow(ctx, `SELECT rounds_published FROM spotlight`).Scan(&rounds)
	pool.QueryRow(ctx, `SELECT count(*) FROM posts WHERE round IS NOT NULL`).Scan(&posts)
	t.Logf("rounds=%d published=%d paid sum=%d refunded=%d next=%d", rounds, posts, rec.PublishedPaymentCent, rec.RefundedCents, nextPrice(alice))
	if rounds != 7 || rec.PublishedPaymentCent != 100+200+400+800+1600+3200 || rec.RefundedCents != 400 {
		t.Fatalf("ledger mismatch: rounds %d, paid %d, refunded %d", rounds, rec.PublishedPaymentCent, rec.RefundedCents)
	}
}

func TestDuplicateWebhooksInParallel(t *testing.T) {
	ts, srv, pool := setup(t)
	ctx := context.Background()
	b := newBrowser(t, ts.URL)
	tok := b.submit("opinion", "founding", "", "f")
	b.checkout(tok, 0)
	tok2 := b.submit("opinion", "paid once", "", "p")
	resp, _ := b.checkout(tok2, 100)
	m := simRe.FindStringSubmatch(resp.Request.URL.Path)
	ss, _ := srv.Sim.Session(ctx, m[1])
	dp := pay.DodoPayment{PaymentID: "pay_parallel", Status: "succeeded", TotalAmount: 100, Currency: "USD", Metadata: map[string]string{"reservation_id": ss.Meta["reservation_id"]}, CheckoutSessionID: ss.ID}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			srv.Sim.Deliver(ctx, fmt.Sprintf("msg_par_%d", i%3), "payment.succeeded", dp)
		}(i)
	}
	wg.Wait()
	var rounds, payments int
	pool.QueryRow(ctx, `SELECT rounds_published FROM spotlight`).Scan(&rounds)
	pool.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&payments)
	if rounds != 2 || payments != 1 {
		t.Fatalf("parallel duplicates: rounds %d payments %d", rounds, payments)
	}
	if p := nextPrice(b); p != 200 {
		t.Fatalf("price %d", p)
	}
}

func TestPriceRule(t *testing.T) {
	want := []int64{0, 100, 200, 400, 800, 1600, 3200, 6400}
	for r, w := range want {
		if got, _ := core.PriceForRound(r); got != w {
			t.Fatalf("round %d: %d", r, got)
		}
	}
	if core.EgoReceiptHeadline(1600) != "I paid $16 to make you read this. It worked." {
		t.Fatal("headline")
	}
	if core.EgoReceiptHeadline(51200) != "I paid $512 to make you read this. It worked." || core.FormatUSD(102400) != "$1,024" {
		t.Fatal("format")
	}
}

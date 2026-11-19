package web

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vikash-parashar/topfornow/internal/core"
	"github.com/vikash-parashar/topfornow/internal/pay"
	"github.com/vikash-parashar/topfornow/internal/safety"
)

// CardData is what the post card partial needs.
type CardData struct {
	P       *core.Post
	Big     bool
	MyVote  int
	CSRF    string
	Now     time.Time
	Preview bool // draft preview: no voting
}

type homeData struct {
	State       *core.State
	Card        CardData
	History     []CardData
	NextPrice   int64
	HoldUntil   *time.Time
	Longest     *core.Post
	TotalRounds int
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Svc.GetState(ctx)
	if err != nil {
		s.fail(w, r, 500, "Could not load the spotlight.")
		return
	}
	hist, _ := s.Svc.History(ctx, 12)
	v := visitorFrom(r)
	ids := []int64{}
	if st.Current != nil {
		ids = append(ids, st.Current.ID)
	}
	for _, p := range hist {
		ids = append(ids, p.ID)
	}
	mine, _ := s.Svc.MyVotes(ctx, v, ids)
	csrf := s.csrfToken(v)
	now := time.Now()
	d := homeData{State: st, NextPrice: st.NextPriceCents, TotalRounds: st.RoundsPublished}
	if st.Current != nil {
		d.Card = CardData{P: st.Current, Big: true, MyVote: mine[st.Current.ID], CSRF: csrf, Now: now}
	}
	var longest *core.Post
	for _, p := range hist {
		d.History = append(d.History, CardData{P: p, MyVote: mine[p.ID], CSRF: csrf, Now: now})
		if longest == nil || p.TimeOnTop(now) > longest.TimeOnTop(now) {
			longest = p
		}
	}
	if st.Current != nil && (longest == nil || st.Current.TimeOnTop(now) > longest.TimeOnTop(now)) {
		longest = st.Current
	}
	d.Longest = longest
	if st.ActiveHold != nil {
		d.HoldUntil = &st.ActiveHold.ExpiresAt
	}
	bot := isBot(r)
	src := s.source(w, r)
	s.Svc.Track(ctx, core.EvVisit, v, 0, src, r.URL.Path, bot)
	if st.Current != nil && st.Current.Status == "published" {
		s.Svc.Track(ctx, core.EvSpotView, v, st.Current.ID, src, r.URL.Path, bot)
	}
	desc := "One public top spot. Free to read and vote. Taking it costs " + core.FormatUSD(st.NextPriceCents) + " right now, and the price doubles every time."
	s.render(w, r, "home", 200, Page{Title: "TopForNow: one spot, everyone wants it", Description: desc, Canonical: s.BaseURL + "/", Data: d})
}

func (s *Server) rules(w http.ResponseWriter, r *http.Request) {
	st, _ := s.Svc.GetState(r.Context())
	s.render(w, r, "rules", 200, Page{Title: "Rules, terms and privacy · TopForNow", Data: st})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	hist, _ := s.Svc.History(r.Context(), 200)
	now := time.Now()
	var cards []CardData
	csrf := s.csrfToken(visitorFrom(r))
	for _, p := range hist {
		cards = append(cards, CardData{P: p, CSRF: csrf, Now: now})
	}
	s.render(w, r, "history", 200, Page{Title: "Every past top post · TopForNow", Data: cards})
}

// ---- submission ----

type submitData struct {
	Kind, Headline, Body, Link, Author, Email string
	ReplyTo                                   *core.Post
	Error                                     string
	Price                                     int64
	Founding                                  bool
}

func (s *Server) submitForm(w http.ResponseWriter, r *http.Request) {
	st, _ := s.Svc.GetState(r.Context())
	d := submitData{Kind: r.URL.Query().Get("kind"), Price: st.NextPriceCents, Founding: st.NextRound == 0}
	if d.Kind == "" {
		d.Kind = core.KindOpinion
	}
	if slug := r.URL.Query().Get("reply_to"); slug != "" {
		if p, err := s.Svc.PostBySlug(r.Context(), slug); err == nil && p.Round != nil {
			d.ReplyTo = p
		}
	}
	s.render(w, r, "submit", 200, Page{Title: "Take the spotlight · TopForNow", Data: d})
}

func (s *Server) submitPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.checkCSRF(r) {
		s.fail(w, r, 400, "Your session expired. Go back, refresh the page and try again.")
		return
	}
	st, _ := s.Svc.GetState(ctx)
	d := submitData{
		Kind: r.FormValue("kind"), Headline: r.FormValue("headline"), Body: r.FormValue("body"),
		Link: r.FormValue("link"), Author: r.FormValue("author"), Email: r.FormValue("email"),
		Price: st.NextPriceCents, Founding: st.NextRound == 0,
	}
	var replyID *int64
	if slug := r.FormValue("reply_to"); slug != "" {
		if p, err := s.Svc.PostBySlug(ctx, slug); err == nil {
			d.ReplyTo, replyID = p, &p.ID
		}
	}
	bad := func(msg string) {
		d.Error = msg
		s.render(w, r, "submit", 422, Page{Title: "Take the spotlight · TopForNow", Data: d})
	}
	if r.FormValue("website") != "" { // honeypot
		bad("Something went wrong. Please try again.")
		return
	}
	if d.Kind != core.KindOpinion && d.Kind != core.KindCreator && d.Kind != core.KindProduct {
		bad("Choose what you are posting.")
		return
	}
	headline, err := safety.Text(d.Headline, 120)
	if err != nil || len(headline) < 3 {
		bad("Write one line between 3 and 120 characters.")
		return
	}
	body, err := safety.Text(d.Body, 200)
	if err != nil {
		bad("Keep the description under 200 characters.")
		return
	}
	author, err := safety.Text(d.Author, 40)
	if err != nil || author == "" {
		bad("Add the name to show as the person or company who featured this (up to 40 characters).")
		return
	}
	email := strings.TrimSpace(d.Email)
	if email != "" && (len(email) > 120 || !strings.Contains(email, "@")) {
		bad("That email address does not look right. It is optional.")
		return
	}
	if r.FormValue("agree") != "yes" {
		bad("Please confirm the rules.")
		return
	}
	var link safety.Link
	if strings.TrimSpace(d.Link) != "" || d.Kind != core.KindOpinion {
		link, err = safety.CheckLink(d.Link)
		if err != nil {
			bad("Link: " + err.Error() + ".")
			return
		}
	}
	if d.Kind == core.KindCreator && link.EmbedProvider == "" {
		bad("For creator posts, paste a YouTube video/Short or a public Instagram post/Reel link. For any other link, choose Product.")
		return
	}
	review := safety.NeedsReview(headline, body, author)
	p, err := s.Svc.CreateDraft(ctx, core.DraftInput{
		Kind: d.Kind, Headline: headline, Body: body, LinkURL: link.URL, LinkHost: link.Host,
		EmbedProvider: link.EmbedProvider, EmbedRef: link.EmbedRef, AuthorName: author, ContactEmail: email,
		ReplyToID: replyID, ReviewReason: review, Visitor: visitorFrom(r),
	})
	if err != nil {
		s.Log.Error("create draft", "err", err)
		bad("Could not save your post. Please try again.")
		return
	}
	http.Redirect(w, r, "/draft/"+p.EditToken, http.StatusSeeOther)
}

type draftData struct {
	Post      *core.Post
	Card      CardData
	State     *core.State
	Price     int64
	NextAfter int64
	Hold      *core.Hold
	MineHeld  bool
	Notice    string
	Results   *core.Results
	ShareURL  string
	ShareText string
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Svc.PostByToken(ctx, r.PathValue("token"))
	if err != nil {
		s.fail(w, r, 404, "This draft link is not valid.")
		return
	}
	st, err := s.Svc.GetState(ctx)
	if err != nil {
		s.fail(w, r, 500, "Could not load the spotlight.")
		return
	}
	d := draftData{Post: p, State: st, Price: st.NextPriceCents, Notice: r.URL.Query().Get("n"),
		Card: CardData{P: p, Big: true, Preview: true, Now: time.Now()}}
	d.NextAfter, _ = core.PriceForRound(st.NextRound + 1)
	if st.ActiveHold != nil && st.ActiveHold.PostID == p.ID {
		d.Hold, d.MineHeld = st.ActiveHold, true
	} else if st.ActiveHold != nil {
		d.Hold = st.ActiveHold
	}
	if p.Round != nil {
		d.Results, _ = s.Svc.ResultsFor(ctx, p)
		d.ShareURL = s.BaseURL + "/p/" + p.Slug + "?ref=receipt"
		d.ShareText = core.EgoReceiptHeadline(p.PriceCents)
	}
	s.render(w, r, "draft", 200, Page{Title: "Your post · TopForNow", NoIndex: true, Data: d})
}

func (s *Server) checkout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := r.PathValue("token")
	if !s.checkCSRF(r) {
		s.fail(w, r, 400, "Your session expired. Refresh the page and try again.")
		return
	}
	quoted, err := strconv.ParseInt(r.FormValue("quoted_cents"), 10, 64)
	if err != nil || quoted < 0 {
		s.fail(w, r, 400, "Missing price.")
		return
	}
	p, err := s.Svc.PostByToken(ctx, token)
	if err != nil {
		s.fail(w, r, 404, "This draft link is not valid.")
		return
	}
	s.Svc.Track(ctx, core.EvCheckout, visitorFrom(r), p.ID, s.source(w, r), r.URL.Path, isBot(r))
	res, err := s.Svc.StartCheckout(ctx, token, quoted, visitorFrom(r))
	var stale *core.StaleQuoteError
	var held *core.HeldError
	switch {
	case errors.As(err, &stale):
		s.render(w, r, "stale", 409, Page{Title: "The price changed · TopForNow", NoIndex: true, Data: map[string]any{"Stale": stale, "Token": token}})
		return
	case errors.As(err, &held):
		s.render(w, r, "held", 409, Page{Title: "Someone is checking out · TopForNow", NoIndex: true,
			Data: map[string]any{"Until": held.Until, "Token": token, "Price": quoted}})
		return
	case errors.Is(err, core.ErrOverLimit):
		s.fail(w, r, 409, "The next price is above the per-payment limit of our card processor, so online checkout is closed for this round. Contact the owner through the Rules page to arrange it.")
		return
	case errors.Is(err, core.ErrNotApproved):
		http.Redirect(w, r, "/draft/"+token+"?n=review", http.StatusSeeOther)
		return
	case errors.Is(err, core.ErrAlreadyDone):
		http.Redirect(w, r, "/draft/"+token, http.StatusSeeOther)
		return
	case err != nil:
		s.Log.Error("checkout", "err", err)
		s.fail(w, r, 502, "Checkout could not be opened. Nothing was charged. Please try again in a minute.")
		return
	}
	if res.Published {
		http.Redirect(w, r, "/draft/"+token+"?n=founding", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, res.CheckoutURL, http.StatusSeeOther)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		s.fail(w, r, 400, "Session expired.")
		return
	}
	s.Svc.CancelHold(r.Context(), r.PathValue("token"))
	http.Redirect(w, r, "/draft/"+r.PathValue("token")+"?n=cancelled", http.StatusSeeOther)
}

// checkoutReturn is where the provider sends the buyer back. It never
// publishes on its own word: it either waits for the signed webhook or
// verifies the payment server-to-server with the provider API.
func (s *Server) checkoutReturn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := s.Svc.HoldByID(ctx, r.URL.Query().Get("r"))
	if err != nil {
		s.fail(w, r, 404, "Unknown checkout.")
		return
	}
	if pid := r.URL.Query().Get("payment_id"); pid != "" && h.Status != "paid" && s.Fetcher != nil {
		if p, err := s.Fetcher.GetPayment(ctx, pid); err == nil && p.Metadata["reservation_id"] == h.ID {
			if typ := pay.StatusToType(p.Status); typ != "" {
				ev := pay.ToEvent("fetch:"+pid+":"+p.Status, typ, p, nil)
				if ev.ReservationID == "" {
					ev.ReservationID = h.ID
				}
				s.Svc.ApplyPaymentEvent(ctx, ev)
			}
		} else if err != nil {
			s.Log.Warn("return verify", "err", err)
		}
	}
	post, _ := s.Svc.PostByID(ctx, h.PostID)
	s.render(w, r, "return", 200, Page{Title: "Confirming payment · TopForNow", NoIndex: true, Data: map[string]any{"Hold": h, "Post": post}})
}

type holdStatus struct {
	Hold          string `json:"hold"`
	Payment       string `json:"payment"`
	Outcome       string `json:"outcome"`
	Published     bool   `json:"published"`
	State         string `json:"state"`
	Message       string `json:"message"`
	Redirect      string `json:"redirect,omitempty"`
	ExpiresInSecs int    `json:"expires_in_secs"`
}

func (s *Server) holdStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := s.Svc.HoldByID(ctx, r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	out := holdStatus{Hold: h.Status, ExpiresInSecs: int(time.Until(h.ExpiresAt).Seconds())}
	var pstatus, outcome string
	s.Svc.DB.QueryRow(ctx, `SELECT status, outcome FROM payments WHERE reservation_id = $1::uuid ORDER BY id DESC LIMIT 1`, h.ID).Scan(&pstatus, &outcome)
	out.Payment, out.Outcome = pstatus, outcome
	post, _ := s.Svc.PostByID(ctx, h.PostID)
	switch {
	case h.Status == "paid":
		out.State, out.Published = "published", true
		out.Message = "Payment confirmed. Your post is on top."
		if post != nil {
			out.Redirect = "/draft/" + post.EditToken + "?n=paid"
		}
	case strings.HasPrefix(outcome, "refund_") || pstatus == "refunded" || pstatus == "refund_pending":
		out.State = "refunding"
		out.Message = "Another payment for this round confirmed before yours, or the amount did not match. Your payment is being refunded in full automatically. Nothing else is needed from you."
		if pstatus == "refunded" {
			out.Message = "Your payment was refunded in full."
		}
	case pstatus == "failed" || pstatus == "cancelled":
		out.State = "failed"
		out.Message = "The payment did not go through. Nothing was charged. You can try again while your hold lasts."
	case pstatus == "processing":
		out.State = "processing"
		out.Message = "Your bank is still processing the payment. Your spot stays reserved while it does."
	case h.Status == "active":
		out.State = "waiting"
		out.Message = "Waiting for the payment provider to confirm. This usually takes a few seconds."
	default:
		out.State = "closed"
		out.Message = "This checkout is no longer open and no payment was recorded. You were not charged."
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) apiState(w http.ResponseWriter, r *http.Request) {
	st, err := s.Svc.GetState(r.Context())
	if err != nil {
		http.Error(w, "error", 500)
		return
	}
	out := map[string]any{"rounds_published": st.RoundsPublished, "next_round": st.NextRound,
		"next_price_cents": st.NextPriceCents, "held": st.ActiveHold != nil, "over_limit": st.OverLimit, "mode": s.Mode}
	if st.Current != nil {
		out["current"] = map[string]any{"slug": st.Current.Slug, "kind": st.Current.Kind, "headline": st.Current.Headline,
			"round": st.Current.Round, "paid_cents": st.Current.PriceCents, "up": st.Current.Up, "down": st.Current.Down}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// ---- public post pages ----

type postData struct {
	Card      CardData
	Results   *core.Results
	State     *core.State
	ShareURL  string
	ShareText string
	Replies   []*core.Post
}

func (s *Server) loadPublicPost(w http.ResponseWriter, r *http.Request) (*core.Post, bool) {
	p, err := s.Svc.PostBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || p.Round == nil {
		s.fail(w, r, 404, "This post does not exist or was never published.")
		return nil, false
	}
	return p, true
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.loadPublicPost(w, r)
	if !ok {
		return
	}
	st, _ := s.Svc.GetState(ctx)
	v := visitorFrom(r)
	mine, _ := s.Svc.MyVotes(ctx, v, []int64{p.ID})
	res, _ := s.Svc.ResultsFor(ctx, p)
	src := s.source(w, r)
	s.Svc.Track(ctx, core.EvVisit, v, 0, src, r.URL.Path, isBot(r))
	if p.Status == "published" {
		s.Svc.Track(ctx, core.EvPostView, v, p.ID, src, r.URL.Path, isBot(r))
	}
	d := postData{Card: CardData{P: p, Big: true, MyVote: mine[p.ID], CSRF: s.csrfToken(v), Now: time.Now()},
		Results: res, State: st, ShareURL: s.BaseURL + "/p/" + p.Slug + "?ref=share", ShareText: shareText(p)}
	rows, _ := s.Svc.DB.Query(ctx, `SELECT slug, headline FROM posts WHERE reply_to_id = $1 AND status = 'published' ORDER BY round`, p.ID)
	for rows.Next() {
		rp := &core.Post{}
		rows.Scan(&rp.Slug, &rp.Headline)
		d.Replies = append(d.Replies, rp)
	}
	rows.Close()
	title := p.Headline + " · TopForNow"
	desc := core.EgoReceiptHeadline(p.PriceCents) + " Read it, vote for free, or take the spot."
	if p.Status == "removed" {
		title, desc = "Removed post · TopForNow", "This post was removed by a moderator."
	}
	s.render(w, r, "post", 200, Page{Title: title, Description: desc, OGImage: s.BaseURL + "/receipt/" + p.Slug + "?format=og",
		Canonical: s.BaseURL + "/p/" + p.Slug, Data: d})
}

func shareText(p *core.Post) string {
	if p.Kind == core.KindOpinion {
		return fmt.Sprintf("“%s” Agree or disagree? It's on TopForNow.", p.Headline)
	}
	return fmt.Sprintf("“%s” on TopForNow. Worth it?", p.Headline)
}

func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPublicPost(w, r)
	if !ok {
		return
	}
	res, err := s.Svc.ResultsFor(r.Context(), p)
	if err != nil {
		s.fail(w, r, 500, "Could not load results.")
		return
	}
	st, _ := s.Svc.GetState(r.Context())
	s.render(w, r, "results", 200, Page{Title: "Results: " + p.Headline + " · TopForNow", OGImage: s.BaseURL + "/receipt/" + p.Slug + "?format=og",
		Canonical: s.BaseURL + "/p/" + p.Slug + "/results", Data: map[string]any{"P": p, "R": res, "State": st, "Now": time.Now()}})
}

func (s *Server) outbound(w http.ResponseWriter, r *http.Request) {
	p, err := s.Svc.PostBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || p.LinkURL == "" || p.Status != "published" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.Svc.Track(r.Context(), core.EvClick, visitorFrom(r), p.ID, s.source(w, r), r.URL.Path, isBot(r))
	u, err := url.Parse(p.LinkURL)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if p.EmbedProvider == "" { // don't decorate YouTube/Instagram links
		q := u.Query()
		q.Set("utm_source", "topfornow")
		q.Set("utm_medium", "spotlight")
		if p.Round != nil {
			q.Set("utm_campaign", "round-"+strconv.Itoa(*p.Round))
		}
		u.RawQuery = q.Encode()
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) vote(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wantsJSON := r.Header.Get("Accept") == "application/json"
	if !s.checkCSRF(r) {
		http.Error(w, "session expired, refresh the page", 400)
		return
	}
	if isBot(r) {
		http.Error(w, "votes need a regular browser", 403)
		return
	}
	p, err := s.Svc.PostBySlug(ctx, r.FormValue("slug"))
	if err != nil || p.Status != "published" {
		http.Error(w, "post not found", 404)
		return
	}
	val, _ := strconv.Atoi(r.FormValue("value"))
	if val != 1 && val != -1 && val != 0 {
		http.Error(w, "bad vote", 400)
		return
	}
	up, down, err := s.Svc.Vote(ctx, p.ID, visitorFrom(r), s.ipHash(r), val)
	if errors.Is(err, core.ErrVoteLimit) {
		http.Error(w, err.Error(), 429)
		return
	}
	if err != nil {
		http.Error(w, "could not vote", 500)
		return
	}
	s.Svc.Track(ctx, core.EvVote, visitorFrom(r), p.ID, s.source(w, r), "/vote", false)
	if wantsJSON {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"up": up, "down": down, "mine": val})
		return
	}
	back := r.Referer()
	if back == "" || !strings.HasPrefix(back, s.BaseURL) {
		back = "/p/" + p.Slug
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// beacon records client-observed events: video_play (from the YouTube
// player API) and share (Web Share / copy link).
func (s *Server) beacon(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		w.WriteHeader(204)
		return
	}
	typ := r.FormValue("type")
	if typ != core.EvVideoPlay && typ != core.EvShare && typ != core.EvReceiptView {
		w.WriteHeader(204)
		return
	}
	if p, err := s.Svc.PostBySlug(r.Context(), r.FormValue("slug")); err == nil {
		s.Svc.Track(r.Context(), typ, visitorFrom(r), p.ID, s.source(w, r), "/beacon", isBot(r))
	}
	w.WriteHeader(204)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		s.fail(w, r, 400, "Session expired.")
		return
	}
	p, err := s.Svc.PostBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		s.fail(w, r, 404, "Not found.")
		return
	}
	reason := r.FormValue("reason")
	switch reason {
	case "spam", "scam", "hate", "sexual", "violence", "illegal", "other":
	default:
		reason = "other"
	}
	s.Svc.Report(r.Context(), p.ID, reason, visitorFrom(r))
	s.render(w, r, "error", 200, Page{Title: "Thanks for the report", NoIndex: true, Data: "Thanks. The owner reviews every report and removes posts that break the rules."})
}

// ---- RSS so free visitors can follow every change of the top spot ----

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	posts, _ := s.Svc.History(r.Context(), 50)
	if st, err := s.Svc.GetState(r.Context()); err == nil && st.Current != nil {
		posts = append([]*core.Post{st.Current}, posts...)
	}
	type item struct {
		Title   string `xml:"title"`
		Link    string `xml:"link"`
		GUID    string `xml:"guid"`
		PubDate string `xml:"pubDate"`
		Desc    string `xml:"description"`
	}
	type channel struct {
		Title string `xml:"title"`
		Link  string `xml:"link"`
		Desc  string `xml:"description"`
		Items []item `xml:"item"`
	}
	ch := channel{Title: "TopForNow: every new top post", Link: s.BaseURL + "/", Desc: "A new item each time someone takes the spotlight."}
	for _, p := range posts {
		if p.PublishedAt == nil || p.Status != "published" {
			continue
		}
		ch.Items = append(ch.Items, item{Title: fmt.Sprintf("Round %d (%s): %s", *p.Round, core.FormatUSD(p.PriceCents), p.Headline),
			Link: s.BaseURL + "/p/" + p.Slug, GUID: s.BaseURL + "/p/" + p.Slug, PubDate: p.PublishedAt.UTC().Format(time.RFC1123Z),
			Desc: "Featured by " + p.AuthorName})
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"rss"`
		Version string   `xml:"version,attr"`
		Channel channel  `xml:"channel"`
	}{Version: "2.0", Channel: ch})
}

// ---- webhook ----

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", 400)
		return
	}
	ev, err := pay.ParseWebhook(s.WebhookSecret, body, r.Header)
	if err != nil {
		s.Log.Warn("webhook rejected", "err", err)
		http.Error(w, "invalid signature", 401)
		return
	}
	res, err := s.Svc.ApplyPaymentEvent(r.Context(), ev)
	if err != nil {
		s.Log.Error("webhook apply", "err", err, "type", ev.Type)
		http.Error(w, "retry later", 500) // provider will retry
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// ---- sandbox checkout (simulator only) ----

func (s *Server) simCheckout(w http.ResponseWriter, r *http.Request) {
	ss, err := s.Sim.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, 404, "Unknown sandbox checkout.")
		return
	}
	s.render(w, r, "simcheckout", 200, Page{Title: "Sandbox checkout · TopForNow", NoIndex: true, Data: ss})
}

func (s *Server) simAct(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		s.fail(w, r, 400, "Session expired.")
		return
	}
	id := r.PathValue("id")
	ss, err := s.Sim.Session(r.Context(), id)
	if err != nil {
		s.fail(w, r, 404, "Unknown sandbox checkout.")
		return
	}
	action := r.FormValue("action")
	if action == "close" {
		http.Redirect(w, r, ss.Meta["cancel_url"], http.StatusSeeOther)
		return
	}
	deliveries, _ := strconv.Atoi(r.FormValue("deliveries"))
	delay, _ := strconv.Atoi(r.FormValue("delay"))
	if delay > 60 {
		delay = 60
	}
	pid, err := s.Sim.Act(r.Context(), id, action, deliveries, time.Duration(delay)*time.Second)
	if err != nil {
		s.fail(w, r, 409, "Sandbox: "+err.Error())
		return
	}
	ret := ss.Meta["return_url"]
	if action == "decline" {
		ret += "&status=failed"
	} else {
		ret += "&status=succeeded"
	}
	// Like Dodo, append the payment id. The return page treats it only as a
	// hint and verifies it server-side.
	if r.FormValue("skip_return_verify") != "1" {
		ret += "&payment_id=" + url.QueryEscape(pid)
	}
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

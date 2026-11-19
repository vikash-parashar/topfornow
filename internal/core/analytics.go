package core

import (
	"context"
	"time"
)

// Event types recorded for the funnel:
// visit -> spot_view / post_view -> vote -> outbound_click -> checkout_start -> payment -> publish.
// video_play is recorded only when the YouTube player itself reports playback.
const (
	EvVisit       = "visit"
	EvSpotView    = "spot_view"
	EvPostView    = "post_view"
	EvVote        = "vote"
	EvClick       = "outbound_click"
	EvVideoPlay   = "video_play"
	EvShare       = "share"
	EvCheckout    = "checkout_start"
	EvReceiptView = "receipt_download"
)

// Track records an analytics event. Errors are logged, never surfaced.
func (s *Service) Track(ctx context.Context, typ, visitor string, postID int64, source, path string, isBot bool) {
	var pid any
	if postID > 0 {
		pid = postID
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO events (type, visitor, post_id, source, path, is_bot, is_test) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		typ, visitor, pid, source, path, isBot, s.Cfg.IsTest); err != nil {
		s.Log.Warn("track", "err", err)
	}
}

// Results are the measured numbers for one post.
type Results struct {
	TimeOnTop     time.Duration
	SpotViews     int // unique visitor-hours on the homepage while this post was on top
	PostViews     int // unique visitor-hours on the post's own page
	OutboundClick int // unique visitors who clicked through our redirect
	ClicksTotal   int
	VideoPlays    int // unique visitors whose YouTube player reported "playing"
	Shares        int
	Up, Down      int
	Replies       int
}

// ResultsFor computes a post's results from raw events (bots excluded).
func (s *Service) ResultsFor(ctx context.Context, p *Post) (*Results, error) {
	r := &Results{TimeOnTop: p.TimeOnTop(s.Now()), Up: p.Up, Down: p.Down}
	err := s.DB.QueryRow(ctx, `SELECT
		count(DISTINCT (visitor, date_trunc('hour', ts))) FILTER (WHERE type = 'spot_view'),
		count(DISTINCT (visitor, date_trunc('hour', ts))) FILTER (WHERE type = 'post_view'),
		count(DISTINCT visitor) FILTER (WHERE type = 'outbound_click'),
		count(*) FILTER (WHERE type = 'outbound_click'),
		count(DISTINCT visitor) FILTER (WHERE type = 'video_play'),
		count(*) FILTER (WHERE type = 'share')
		FROM events WHERE post_id = $1 AND NOT is_bot`, p.ID).
		Scan(&r.SpotViews, &r.PostViews, &r.OutboundClick, &r.ClicksTotal, &r.VideoPlays, &r.Shares)
	if err != nil {
		return nil, err
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM posts WHERE reply_to_id = $1 AND status = 'published'`, p.ID).Scan(&r.Replies)
	return r, nil
}

// FunnelRow is one traffic source's funnel.
type FunnelRow struct {
	Source                                               string
	Visitors, Voters, Clickers, Checkouts, Payments, Pubs int
}

// Funnel groups unique visitors by first-touch source over a window.
func (s *Service) Funnel(ctx context.Context, since time.Time) ([]FunnelRow, error) {
	rows, err := s.DB.Query(ctx, `WITH firsts AS (
			SELECT DISTINCT ON (visitor) visitor, coalesce(nullif(source,''),'direct') AS source
			FROM events WHERE type = 'visit' AND NOT is_bot AND ts >= $1 AND visitor <> '' ORDER BY visitor, ts)
		SELECT f.source,
			count(DISTINCT f.visitor),
			count(DISTINCT e.visitor) FILTER (WHERE e.type = 'vote'),
			count(DISTINCT e.visitor) FILTER (WHERE e.type = 'outbound_click'),
			count(DISTINCT e.visitor) FILTER (WHERE e.type = 'checkout_start'),
			count(DISTINCT p.id) FILTER (WHERE p.status = 'published' AND p.price_cents > 0),
			count(DISTINCT p.id) FILTER (WHERE p.status = 'published')
		FROM firsts f
		LEFT JOIN events e ON e.visitor = f.visitor AND e.ts >= $1
		LEFT JOIN posts p ON p.visitor = f.visitor AND p.published_at >= $1
		GROUP BY f.source ORDER BY 2 DESC LIMIT 30`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FunnelRow
	for rows.Next() {
		var r FunnelRow
		if err := rows.Scan(&r.Source, &r.Visitors, &r.Voters, &r.Clickers, &r.Checkouts, &r.Payments, &r.Pubs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Traffic summarises human vs bot events over a window.
type Traffic struct {
	HumanVisitors, BotHits, Votes, Clicks, VideoPlays, Checkouts int
}

// TrafficSince returns totals.
func (s *Service) TrafficSince(ctx context.Context, since time.Time) (*Traffic, error) {
	t := &Traffic{}
	err := s.DB.QueryRow(ctx, `SELECT
		count(DISTINCT visitor) FILTER (WHERE NOT is_bot AND type = 'visit'),
		count(*) FILTER (WHERE is_bot),
		count(*) FILTER (WHERE type = 'vote' AND NOT is_bot),
		count(*) FILTER (WHERE type = 'outbound_click' AND NOT is_bot),
		count(*) FILTER (WHERE type = 'video_play' AND NOT is_bot),
		count(*) FILTER (WHERE type = 'checkout_start' AND NOT is_bot)
		FROM events WHERE ts >= $1`, since).Scan(&t.HumanVisitors, &t.BotHits, &t.Votes, &t.Clicks, &t.VideoPlays, &t.Checkouts)
	return t, err
}

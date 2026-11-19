// Package web serves TopForNow's pages, webhooks, images and admin.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vikash-parashar/topfornow/internal/core"
	"github.com/vikash-parashar/topfornow/internal/pay"
)

//go:embed templates/*.html
var tmplFS embed.FS

//go:embed static/*
var staticFS embed.FS

// PaymentFetcher verifies a payment server-to-server.
type PaymentFetcher interface {
	GetPayment(ctx context.Context, id string) (*pay.DodoPayment, error)
}

// Server holds dependencies.
type Server struct {
	Svc           *core.Service
	Sim           *pay.Sim
	Fetcher       PaymentFetcher
	Mode          string
	WebhookSecret string
	Secret        []byte
	AdminPassword string
	AdminUser     string
	OwnerTZ       *time.Location
	BaseURL       string
	TrustProxy    bool
	Log           *slog.Logger

	pages   map[string]*template.Template
	limiter *limiter
}

// IsLive reports whether real money moves.
func (s *Server) IsLive() bool { return s.Mode == pay.ModeDodoLive }

// Handler builds the router.
func (s *Server) Handler() (http.Handler, error) {
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.limiter = newLimiter()
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServer(http.FS(static)), 3600)))
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /rules", s.rules)
	mux.HandleFunc("GET /terms", s.rules)
	mux.HandleFunc("GET /privacy", s.rules)
	mux.HandleFunc("GET /history", s.history)
	mux.HandleFunc("GET /feed.xml", s.feed)
	mux.HandleFunc("GET /submit", s.submitForm)
	mux.HandleFunc("POST /submit", s.limit(10, s.submitPost))
	mux.HandleFunc("GET /draft/{token}", s.draft)
	mux.HandleFunc("POST /draft/{token}/checkout", s.limit(20, s.checkout))
	mux.HandleFunc("POST /draft/{token}/cancel", s.cancel)
	mux.HandleFunc("GET /checkout/return", s.checkoutReturn)
	mux.HandleFunc("GET /api/hold/{id}", s.holdStatus)
	mux.HandleFunc("GET /api/state", s.apiState)
	mux.HandleFunc("GET /p/{slug}", s.post)
	mux.HandleFunc("GET /p/{slug}/results", s.results)
	mux.HandleFunc("POST /p/{slug}/report", s.limit(10, s.report))
	mux.HandleFunc("GET /go/{slug}", s.outbound)
	mux.HandleFunc("POST /vote", s.limit(60, s.vote))
	mux.HandleFunc("POST /beacon", s.limit(60, s.beacon))
	mux.HandleFunc("GET /receipt/{slug}", s.receiptImage)
	mux.HandleFunc("GET /receipt/preview/{dollars}", s.receiptPreview)
	mux.HandleFunc("GET /og/home.png", s.homeImage)
	mux.HandleFunc("POST /webhooks/dodo", s.webhook)
	mux.HandleFunc("GET /healthz", s.health)
	if s.Sim != nil {
		mux.HandleFunc("GET /sandbox/checkout/{id}", s.simCheckout)
		mux.HandleFunc("POST /sandbox/checkout/{id}", s.simAct)
	}
	s.adminRoutes(mux)
	return s.middleware(mux), nil
}

func cacheFor(h http.Handler, secs int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(secs))
		h.ServeHTTP(w, r)
	})
}

var funcs = template.FuncMap{
	"usd":     core.FormatUSD,
	"dur":     humanDur,
	"receipt": core.EgoReceiptHeadline,
	"iso":     func(t *time.Time) string { if t == nil { return "" }; return t.UTC().Format(time.RFC3339) },
	"date":    func(t *time.Time) string { if t == nil { return "" }; return t.UTC().Format("2 Jan 2006, 15:04 UTC") },
	"kindLabel": func(k string) string {
		switch k {
		case core.KindOpinion:
			return "Opinion"
		case core.KindCreator:
			return "Creator"
		case core.KindProduct:
			return "Product"
		}
		return k
	},
	"upLabel": func(k string) string {
		switch k {
		case core.KindCreator:
			return "Worth watching"
		case core.KindProduct:
			return "Would try it"
		}
		return "Agree"
	},
	"downLabel": func(k string) string {
		switch k {
		case core.KindCreator, core.KindProduct:
			return "Not for me"
		}
		return "Disagree"
	},
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"pct": func(a, b int) int {
		if a+b == 0 {
			return 0
		}
		return a * 100 / (a + b)
	},
	"deref": func(i *int) int { if i == nil { return -1 }; return *i },
	"ladder": priceLadder,
	"today":  func() string { return time.Now().UTC().Format("Mon 2 Jan 2006") },
	"barH": func(c, max int64) int64 {
		if max <= 0 || c <= 0 {
			return 0
		}
		h := c * 80 / max
		if h < 2 {
			h = 2
		}
		return h
	},
	"barY": func(h int64) int64 { return 90 - h },
	"barX": func(i int) int { return 6 + i*10 },
	"neg":  func(c int64) int64 { return -c },
	"pos":  func(c int64) int64 { if c < 0 { return 0 }; return c },
	"half":   func(c int64) int64 { return c / 2 },
	"double": func(c int64) int64 { if c == 0 { return 100 }; return c * 2 },
	"add":    func(a, b int) int { return a + b },
}

func humanDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 48*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func (s *Server) parseTemplates() error {
	s.pages = map[string]*template.Template{}
	names, err := fs.Glob(tmplFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if base == "layout" || base == "partials" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(tmplFS, "templates/layout.html", "templates/partials.html", n)
		if err != nil {
			return fmt.Errorf("template %s: %w", n, err)
		}
		s.pages[base] = t
	}
	return nil
}

// Page is the data every page gets.
type Page struct {
	Title       string
	Description string
	OGImage     string
	Canonical   string
	Mode        string
	Live        bool
	CSRF        string
	BaseURL     string
	NoIndex     bool
	Data        any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, p Page) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "template missing", 500)
		return
	}
	p.Mode, p.Live, p.BaseURL = s.Mode, s.IsLive(), s.BaseURL
	p.CSRF = s.csrfToken(visitorFrom(r))
	if p.OGImage == "" {
		p.OGImage = s.BaseURL + "/og/home.png"
	}
	if p.Description == "" {
		p.Description = "One public top spot. Anyone can take it. Each replacement costs double the last. Voting is free."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, p); err != nil {
		s.Log.Error("render", "page", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, r, "error", status, Page{Title: "Something went wrong", NoIndex: true, Data: msg})
}

// ---- cookies, visitor identity, CSRF ----

type ctxKey int

const visitorKey ctxKey = 1

func (s *Server) sign(v string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))[:24]
}

func (s *Server) csrfToken(visitor string) string { return s.sign("csrf:" + visitor) }

func (s *Server) checkCSRF(r *http.Request) bool {
	tok := r.FormValue("csrf")
	if tok == "" {
		tok = r.Header.Get("X-CSRF")
	}
	want := s.csrfToken(visitorFrom(r))
	return subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

func visitorFrom(r *http.Request) string {
	v, _ := r.Context().Value(visitorKey).(string)
	return v
}

func (s *Server) ensureVisitor(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if c, err := r.Cookie("tfn_v"); err == nil {
		if i := strings.LastIndexByte(c.Value, '.'); i > 0 {
			id, sig := c.Value[:i], c.Value[i+1:]
			if hmac.Equal([]byte(sig), []byte(s.sign("v:"+id))) {
				return r.WithContext(context.WithValue(r.Context(), visitorKey, id)), false
			}
		}
	}
	id := core.NewUUID()[:18]
	http.SetCookie(w, &http.Cookie{Name: "tfn_v", Value: id + "." + s.sign("v:"+id), Path: "/", MaxAge: 400 * 24 * 3600,
		HttpOnly: true, Secure: strings.HasPrefix(s.BaseURL, "https://"), SameSite: http.SameSiteLaxMode})
	return r.WithContext(context.WithValue(r.Context(), visitorKey, id)), true
}

// source is the first-touch attribution: ?ref, ?utm_source or referrer host.
func (s *Server) source(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("tfn_src"); err == nil && c.Value != "" {
		return c.Value
	}
	src := firstNonEmpty(r.URL.Query().Get("utm_source"), r.URL.Query().Get("ref"))
	if src == "" {
		if ref, err := url.Parse(r.Referer()); err == nil && ref.Host != "" {
			own, _ := url.Parse(s.BaseURL)
			if own == nil || ref.Host != own.Host {
				src = strings.TrimPrefix(ref.Hostname(), "www.")
			}
		}
	}
	src = cleanSource(src)
	if src == "" {
		src = "direct"
	}
	http.SetCookie(w, &http.Cookie{Name: "tfn_src", Value: src, Path: "/", MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	return src
}

var srcClean = regexp.MustCompile(`[^a-z0-9._-]`)

func cleanSource(s string) string {
	s = srcClean.ReplaceAllString(strings.ToLower(s), "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
			return ip
		}
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipHash is a daily-salted hash so raw IPs are never stored.
func (s *Server) ipHash(r *http.Request) string {
	return s.sign("ip:" + time.Now().UTC().Format("2006-01-02") + ":" + s.clientIP(r))[:16]
}

var botUA = regexp.MustCompile(`(?i)bot|crawl|spider|slurp|preview|facebookexternalhit|embedly|curl|wget|python|go-http|java/|headless|httpclient|okhttp|axios|node-fetch|scrapy|phantom|lighthouse|pingdom|uptime|monitor`)

func isBot(r *http.Request) bool {
	ua := r.UserAgent()
	return ua == "" || botUA.MatchString(ua) || r.Header.Get("Accept-Language") == ""
}

// ---- middleware ----

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https://i.ytimg.com; "+
			"script-src 'self' https://www.youtube.com https://s.ytimg.com; "+
			"frame-src https://www.youtube-nocookie.com https://www.youtube.com https://www.instagram.com; "+
			"style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		if strings.HasPrefix(s.BaseURL, "https://") {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") && !strings.HasPrefix(r.URL.Path, "/webhooks/") && r.URL.Path != "/healthz" {
			r, _ = s.ensureVisitor(w, r)
		}
		sw := &statusWriter{ResponseWriter: w, code: 200}
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic", "err", rec, "path", r.URL.Path)
				http.Error(w, "internal error", 500)
			}
		}()
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/static/") {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.code, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(c int) { w.code = c; w.ResponseWriter.WriteHeader(c) }

// ---- rate limiting (per IP, per minute, in memory) ----

type limiter struct {
	mu   sync.Mutex
	hits map[string]*bucket
}

type bucket struct {
	n     int
	reset time.Time
}

func newLimiter() *limiter {
	l := &limiter{hits: map[string]*bucket{}}
	go func() {
		for range time.Tick(5 * time.Minute) {
			l.mu.Lock()
			now := time.Now()
			for k, b := range l.hits {
				if now.After(b.reset) {
					delete(l.hits, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

func (l *limiter) allow(key string, perMin int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.hits[key]
	if b == nil || now.After(b.reset) {
		b = &bucket{reset: now.Add(time.Minute)}
		l.hits[key] = b
	}
	b.n++
	return b.n <= perMin
}

func (s *Server) limit(perMin int, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(r.URL.Path+"|"+s.clientIP(r), perMin) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "Too many requests. Try again in a minute.", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Svc.DB.Ping(ctx); err != nil {
		http.Error(w, "db down", 503)
		return
	}
	fmt.Fprintf(w, "ok mode=%s\n", s.Mode)
}

// Rung is one step of the doubling price ladder on the homepage.
type Rung struct {
	Round int
	Cents int64
	State string // taken, next, future
}

// priceLadder shows the last few taken rounds, the next price and what comes after.
func priceLadder(published int) []Rung {
	var out []Rung
	start := published - 3
	if start < 0 {
		start = 0
	}
	for r := start; r <= published+5; r++ {
		c, err := core.PriceForRound(r)
		if err != nil {
			break
		}
		st := "future"
		if r < published {
			st = "taken"
		} else if r == published {
			st = "next"
		}
		out = append(out, Rung{Round: r, Cents: c, State: st})
	}
	return out
}

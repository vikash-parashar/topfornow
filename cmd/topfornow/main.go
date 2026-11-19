// Command topfornow runs the TopForNow web app.
//
//	topfornow serve       run migrations, then serve HTTP (default)
//	topfornow migrate     apply migrations and exit
//	topfornow purge-test  delete all rows labelled as test data
package main

import (
	_ "time/tzdata" // owner time zone works in the distroless image
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vikash-parashar/topfornow/internal/core"
	"github.com/vikash-parashar/topfornow/internal/db"
	"github.com/vikash-parashar/topfornow/internal/pay"
	"github.com/vikash-parashar/topfornow/internal/web"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cmd string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "names", applied)
	}
	if cmd == "migrate" {
		return nil
	}

	mode := env("PAYMENT_MODE", pay.ModeSim)
	port := env("PORT", "8080")
	baseURL := strings.TrimRight(env("BASE_URL", "http://localhost:"+port), "/")
	secret := os.Getenv("APP_SECRET")
	if len(secret) < 32 {
		return errors.New("APP_SECRET must be at least 32 characters (cookie signing)")
	}
	maxCents, _ := strconv.ParseInt(env("MAX_CHECKOUT_CENTS", "500000"), 10, 64)
	cfg := core.Config{HoldDuration: 15 * time.Minute, MaxCheckoutCents: maxCents, IsTest: mode != pay.ModeDodoLive, BaseURL: baseURL}
	httpc := &http.Client{Timeout: 15 * time.Second}

	srv := &web.Server{Mode: mode, Secret: []byte(secret), AdminPassword: os.Getenv("ADMIN_PASSWORD"), AdminUser: env("ADMIN_USERNAME", "owner"), OwnerTZ: ownerTZ(), BaseURL: baseURL,
		TrustProxy: env("TRUST_PROXY", "false") == "true", Log: log}
	var provider core.Provider
	switch mode {
	case pay.ModeSim:
		ws := os.Getenv("SANDBOX_WEBHOOK_SECRET")
		if ws == "" {
			// The simulator runs in this process, so a fresh random secret
			// per boot is enough and nobody outside can forge events.
			b := make([]byte, 24)
			rand.Read(b)
			ws = "whsec_" + base64.StdEncoding.EncodeToString(b)
		}
		sim := &pay.Sim{DB: pool, BaseURL: baseURL, WebhookURL: env("SANDBOX_WEBHOOK_URL", "http://127.0.0.1:"+port+"/webhooks/dodo"),
			Secret: ws, Log: log, HTTP: httpc}
		provider, srv.Sim, srv.Fetcher, srv.WebhookSecret = sim, sim, sim, ws
	case pay.ModeDodoTest, pay.ModeDodoLive:
		base := "https://test.dodopayments.com"
		if mode == pay.ModeDodoLive {
			if os.Getenv("LIVE_PAYMENTS_APPROVED") != "yes-the-owner-approved-real-charges" {
				return errors.New("refusing live mode: set LIVE_PAYMENTS_APPROVED only after the owner approves real charges")
			}
			base = "https://live.dodopayments.com"
		}
		d := &pay.Dodo{BaseURL: base, APIKey: os.Getenv("DODO_API_KEY"), ProductID: os.Getenv("DODO_PRODUCT_ID"), HTTP: httpc}
		if d.APIKey == "" || d.ProductID == "" || os.Getenv("DODO_WEBHOOK_SECRET") == "" {
			return errors.New("DODO_API_KEY, DODO_PRODUCT_ID and DODO_WEBHOOK_SECRET are required for Dodo modes")
		}
		provider, srv.Fetcher, srv.WebhookSecret = d, d, os.Getenv("DODO_WEBHOOK_SECRET")
	default:
		return fmt.Errorf("unknown PAYMENT_MODE %q", mode)
	}
	svc := core.New(pool, provider, cfg, log)
	srv.Svc = svc

	if cmd == "purge-test" {
		out, err := svc.PurgeTestData(ctx)
		log.Info("purge", "deleted", out)
		return err
	}
	if mode == pay.ModeDodoLive {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM posts WHERE is_test`).Scan(&n)
		if n > 0 {
			return fmt.Errorf("live mode refuses to start with %d test posts in the database: run `topfornow purge-test` with PAYMENT_MODE=sandbox first", n)
		}
	}

	h, err := srv.Handler()
	if err != nil {
		return err
	}
	go svc.RunRefundWorker(ctx)
	hs := &http.Server{Addr: ":" + port, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Info("listening", "port", port, "mode", mode, "base_url", baseURL)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ownerTZ is the time zone the owner dashboard groups days by (default India).
func ownerTZ() *time.Location {
	if loc, err := time.LoadLocation(env("OWNER_TZ", "Asia/Kolkata")); err == nil {
		return loc
	}
	return time.UTC
}

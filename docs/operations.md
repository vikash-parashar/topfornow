# Operations: payments, deploy, monitoring, going live

## Stack choice (recorded)

Go 1.26 standard library HTTP + `html/template`, PostgreSQL 16 via pgx, no JS framework (one small progressive-enhancement script). Reason: Vikash's home stack, one static binary, one database, cheap to host, nothing to keep patched on the front end.

## Payment provider: Dodo Payments

**Why Dodo:** checked 19 Nov 2026 in Dodo's public docs.

- Supports **unregistered businesses and individuals**; Indian individuals need **PAN** for KYC ([FAQ](https://docs.dodopayments.com/miscellaneous/faq)).
- Acts as **merchant of record**, so it handles international sales tax/VAT.
- Has **test mode** with separate API keys; webhooks follow **Standard Webhooks** signing (`webhook-id`, `webhook-timestamp`, `webhook-signature`).
- Supports pay-what-you-want one-time products with an `amount` (in cents) per checkout session (`POST /checkouts`), which lets us charge exactly the doubled price ([checkout sessions](https://docs.dodopayments.com/api-reference/checkout-sessions/create.md), [PWYW](https://docs.dodopayments.com/features/pay-what-you-want.md)).
- Outbid.lol itself checks out through Dodo today ([outbid FAQ](https://outbid.lol/faq)), after hitting Polar's acceptable-use limits at launch.
- Prohibited list ([link](https://docs.dodopayments.com/miscellaneous/prohibited-businesses-and-locations)) does not mention advertising or paid placement. It bans "any game of chance requiring payment". **TopForNow is not chance**: a fixed, displayed price buys a defined placement. Checkout describes it as "TopForNow spotlight placement".

**Underwriting questions public docs cannot answer (ask Dodo support before going live):**

1. Is a paid placement of user-supplied content (opinions, creator links, product links) acceptable, given they "often review" marketing tools and refuse human-labour services? (Merchant acceptance page.)
2. Does "game" wording on the site trigger the gambling review, even though there is no chance element or prize?
3. Is there a **per-transaction maximum** for a new merchant? The app closes checkout above `MAX_CHECKOUT_CENTS` (default $5,000) and says so; set this to Dodo's confirmed limit.
4. Payout route and fees to an Indian bank account (INR is not a native payout wallet per the FAQ; table says USD SWIFT $25).
5. Will they refund automatically-detected race losers within their refund rules (full refunds of very recent payments)?

## How checkout stays consistent

- **Integer cents everywhere.** Price for round r ≥ 1 is `100 << (r-1)`. Round 0 is free.
- **Reservation (hold):** pressing pay locks the single `spotlight` row, checks the quoted price equals the current price (else "price changed" page), and creates one **active hold** for that round for 15 minutes. A unique partial index allows only one active hold in the whole database, so a second buyer is told "someone is checking out, hold ends in m:ss".
- **Publication happens only from a verified server-side event:** a Standard-Webhooks-signed `payment.succeeded`, or a server-to-server `GET /payments/{id}` when the buyer returns. Never from the browser's word.
- **Exactly once:** `webhook_events (provider, event_id)` and `payments (provider, provider_payment_id)` are unique; `posts.round` has a unique index; the spotlight update is guarded by `rounds_published = round`. Replays, retries and parallel duplicates are no-ops (tested).
- **Late or lost payments:** if a payment arrives for a round that is still open (e.g. hold expired, nobody else paid), it publishes. If the round was already taken, the payment is marked `refund_pending` and the refund worker refunds it in full automatically (retries with back-off; failures surface in admin).
- **Amount mismatch / wrong currency / unknown reservation:** never published, refunded automatically.
- **Async payment methods:** `payment.processing` extends the hold by 2 hours so the round is kept for that buyer.
- **Failed payment:** recorded; hold stays so the buyer can retry.
- **Closed browser:** irrelevant; the webhook publishes. The return page polls and redirects to the owner page when done.
- **Admin recovery:** "Pull a payment by id" fetches from the provider and applies it if a webhook never arrived; "Retry pending refunds"; reconciliation panel checks: one post per round, contiguous rounds, each price follows the rule, each paid round has exactly one publishing payment ≥ its price.

## Test vs live

| `PAYMENT_MODE` | What happens | Labels |
|---|---|---|
| `sandbox` | Built-in simulator checkout page sends Dodo-shaped, signed webhooks to our real endpoint. No external calls. | Red "TEST MODE · payments are simulated" bar on every page; all rows `is_test` |
| `dodo_test` | Real Dodo test API and webhooks; test cards only | Red "TEST MODE · Dodo Payments test mode" bar; rows `is_test` |
| `dodo_live` | Real charges. Refuses to start unless `LIVE_PAYMENTS_APPROVED=yes-the-owner-approved-real-charges` **and** no test posts are in the database | No test bar |

Test data removal: admin "Purge test data" (sandbox/test only) or `topfornow purge-test`.

## Environment secrets (never in git)

`DATABASE_URL`, `APP_SECRET` (≥32 chars, signs cookies), `ADMIN_PASSWORD`, `BASE_URL`, `PAYMENT_MODE`, `TRUST_PROXY=true` behind Fly; Dodo modes add `DODO_API_KEY`, `DODO_PRODUCT_ID`, `DODO_WEBHOOK_SECRET`; `MAX_CHECKOUT_CENTS`.

## Deploy on Render free + Neon free (chosen, $0)

1. **Neon**: sign up at neon.tech, create a project in AWS Singapore, copy the **direct** connection string (host without `-pooler`, ends `?sslmode=require`). Direct is needed because migrations take a session advisory lock. Neon free keeps a short point-in-time restore window; also run the `pg_dump` below weekly.
2. **Render**: sign up with GitHub, New → **Blueprint** → pick `vikash-parashar/topfornow`. Render reads `render.yaml`, generates `APP_SECRET`, and asks for `DATABASE_URL`, `ADMIN_PASSWORD` and `BASE_URL` (`https://<service>.onrender.com`).
3. Every push to `main` redeploys; migrations run on boot. HTTPS is automatic. Health check `/healthz`.
4. **Rollback:** Render dashboard → service → Events → pick an earlier deploy → "Rollback".
5. **Free-tier limits:** the service sleeps after 15 idle minutes (first visit then takes ~1 minute) and has 750 free hours/month. Move to a paid instance or Fly before any real launch traffic.

## Alternative: Fly.io

Fly gives HTTPS on `*.fly.dev`, secrets, health checks, one-command rollback, and runs the Docker image as is. Postgres: Fly's own, or a free external one (Neon / Supabase) via `DATABASE_URL`.

```sh
fly launch --no-deploy --copy-config --name topfornow   # uses fly.toml
fly secrets set APP_SECRET=$(openssl rand -hex 32) ADMIN_PASSWORD='…' \
  BASE_URL=https://topfornow.fly.dev PAYMENT_MODE=sandbox DATABASE_URL='postgres://…'
fly deploy                                               # migrations run on boot (advisory-locked)
fly status && curl https://topfornow.fly.dev/healthz
```

- **Migrations:** embedded SQL, applied on start under an advisory lock, recorded in `schema_migrations`.
- **Health check / monitoring:** `/healthz` pings the DB; Fly restarts unhealthy machines; JSON logs via `fly logs`. Add a free external uptime check (e.g. UptimeRobot) on `/healthz`.
- **Rollback:** `fly releases` then `fly deploy --image <previous image>`. Migrations are additive only.
- **Backup:** managed Postgres snapshots (Neon/Supabase/Fly) plus a daily `pg_dump` you can run locally: `pg_dump "$DATABASE_URL" -Fc -f topfornow-$(date +%F).dump`.
- **Webhooks (Dodo modes):** in Dodo dashboard → Webhooks, endpoint `https://<host>/webhooks/dodo`, events `payment.*`, `refund.*`, `dispute.*`; paste the signing secret into `DODO_WEBHOOK_SECRET`.

## Going live checklist (needs Vikash)

1. Dodo account approved for live payments, with answers to the 5 questions above.
2. Live product: one-time, pay-what-you-want, **tax inclusive**, minimum $1.
3. Live API key + webhook secret set as Fly secrets.
4. `topfornow purge-test`, then `PAYMENT_MODE=dodo_live` and `LIVE_PAYMENTS_APPROVED=…`.
5. Vikash's explicit "yes, accept real payments".

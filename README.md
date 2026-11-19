# TopForNow

One public top spot. The first post was free, once. Every replacement after that costs double the last: $1, $2, $4, $8, $16, $32… Visitors read, watch and vote for free. Replaced posts keep their own page, votes and results. Buyers get an **Ego Receipt** to share.

- `docs/research.md`: Outbid.lol analysis, sourced and labelled
- `docs/economics.md`: first ten replacements, fees, stop scenarios, fallback
- `docs/launch-plan.md`: 14-day plan, venues and rules, all launch copy (Look At Me Tax)
- `docs/operations.md`: payment design, Dodo underwriting questions, deploy, backup, rollback, going live
- `docs/screenshots/`: browser acceptance run (phone + desktop); `docs/creative/`: campaign preview cards

## Run locally

```sh
docker compose up --build          # app on http://localhost:8080, sandbox payments
# or, with your own Postgres:
cp .env.example .env && set -a && . ./.env && set +a
go run ./cmd/topfornow serve
```

Owner view: `/admin` (password = `ADMIN_PASSWORD`).

## Tests

```sh
go vet ./... && go test ./...
# Full acceptance test (drops and recreates the schema of the DB you point it at):
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/topfornow_test?sslmode=disable go test ./internal/web/ -v
# Browser walk-through against a fresh sandbox server:
BASE=http://localhost:8080 OUT=docs/screenshots node scripts/browser_acceptance.js
```

The acceptance test covers: free opinion on top → $1 YouTube video → $2 product → next $4 with prior votes and results kept; webhook replay (same and new event id); unsigned webhook rejected; two buyers at the same price (second is told to wait); declined card; expired hold + late payment + competing payment refunded in full automatically; stale quote; 20 simultaneous checkouts → exactly one hold; parallel duplicate webhooks → one publication; $8 → $16 → next $32 with the exact Ego Receipt sentence; replace again and the $16 link still shows the $16 post, results, social preview and voting; outbound link tracking; admin protection; ledger reconciliation.

## Payment modes

`PAYMENT_MODE=sandbox` (built-in simulator, default), `dodo_test`, `dodo_live`. Live refuses to start without the explicit approval variable and with test data present. See `docs/operations.md`.

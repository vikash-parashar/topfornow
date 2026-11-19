# Economics: the first ten paid replacements

## Fee basis

- **Dodo Payments** standard plan: 4% + $0.40 per US card payment, **+1.5% for international cards**; $1 per refund; $30 per dispute; no monthly fee; payout via USD SWIFT for non-US businesses listed at $25, FAQ also mentions a $5 fee for payouts under $1,000; minimum payout $50. Source: [dodopayments.com/pricing](https://dodopayments.com/pricing), [FAQ](https://docs.dodopayments.com/miscellaneous/faq), read 19 Nov 2026. **Payout routes to India must be confirmed with Dodo.**
- **Assumption A1:** every buyer uses an international card: 5.5% + $0.40 (worst case for a global audience).
- **Assumption A2:** Dodo is merchant of record and handles sales tax/VAT. The product must be set "tax inclusive" so the buyer pays exactly the displayed price. Your own Indian income tax is not modelled.
- **Hosting (A3):** Fly.io small machine + small Postgres, about **$5/month** budget; domain about $12/year. Free-tier alternatives exist (see operations). Use $6/month all-in.
- **Refunds (A4):** buyers are never refunded for being replaced. Refunds happen only when two payments race for one round. Assume one race refund per ten rounds, at a low round: costs $1 refund fee + the lost processing fee of that payment (assume Dodo keeps it) ≈ **$1.62**.
- **Acquisition (A5):** zero-spend route for rounds 1–6; at most a $25 Reddit test later. Reddit's self-serve minimum is $5/day; typical CPC $0.20–$4 ([Stackmatix, 2026](https://www.stackmatix.com/blog/reddit-ads-minimum-spend)).

## Rounds 1–10

| Round | Price paid | Cumulative gross | Fee on this sale | Cumulative fees | Gross minus fees |
|---|---|---|---|---|---|
| 1 | $1 | $1 | $0.46 | $0.46 | $0.54 |
| 2 | $2 | $3 | $0.51 | $0.97 | $2.04 |
| 3 | $4 | $7 | $0.62 | $1.58 | $5.42 |
| 4 | $8 | $15 | $0.84 | $2.42 | $12.57 |
| 5 | $16 | $31 | $1.28 | $3.71 | $27.30 |
| 6 | $32 | $63 | $2.16 | $5.87 | $57.13 |
| 7 | $64 | $127 | $3.92 | $9.79 | $117.22 |
| 8 | $128 | $255 | $7.44 | $17.23 | $237.78 |
| 9 | $256 | $511 | $14.48 | $31.71 | $479.30 |
| 10 | $512 | $1,023 | $28.56 | $60.27 | $962.74 |

The founding post (round 0) is free and costs nothing but moderation time.

## Contribution if demand stops at $8, $32 or $512

Assume the stop happens within one month, one race refund, one payout.

| | Stops at $8 (rounds 1–4) | Stops at $32 (rounds 1–6) | Stops at $512 (rounds 1–10) |
|---|---|---|---|
| Gross receipts | $15.00 | $63.00 | $1,023.00 |
| Processing fees | −$2.42 | −$5.87 | −$60.27 |
| Race refund cost | −$1.62 | −$1.62 | −$1.62 |
| Hosting + domain (1 month) | −$6.00 | −$6.00 | −$6.00 |
| Payout fee (A2: $25 SWIFT, worst case) | can't pay out (below $50 minimum) | −$25.00 | −$25.00 |
| Acquisition | $0 (zero-spend) | $0 | −$25 (one Reddit test) |
| **Estimated contribution** | **+$4.96, stuck below payout minimum** | **+$24.51** | **+$905.11** |

Reading: the doubling rule only makes real money after round ~7. Before that, it is a marketing engine, not a business. One dispute ($30) erases everything up to round 5.

## Fallback: a $3 "Also on TopForNow" shelf (not built yet)

- **What:** under the main spot, a small shelf of up to 5 one-line links at a flat $3 for 24 hours, first come first served. The main spot keeps the doubling rule untouched.
- **Why this one:** it catches buyers who balk at $64+, it costs the same to run, and a flat price is easy to explain.
- **When real data justifies it:** turn it on only if, over 14 days, the main spot goes **7+ days without a replacement** while the homepage still has **300+ human visitors a week**, *and* at least **10 people start checkout and abandon** at the current price (both are in the admin funnel). Without that data the shelf just dilutes the one-spot story.

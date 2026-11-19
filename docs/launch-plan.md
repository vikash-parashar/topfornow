# TopForNow 14-day launch plan (zero followers)

Audience: indie makers and small SaaS founders who build in public. `{SITE}` = the deployed URL.
**Nothing below is posted, sent or paid for until Vikash approves it.**

## Venues and their current promotion rules

| Venue | Fit | Rule we checked (19 Nov 2026) | Status |
|---|---|---|---|
| **Hacker News, Show HN** | Developers who love weird mechanics | Must be something people can try now, ideally without signup; no landing pages; title starts "Show HN"; **don't ask friends to upvote or comment** ([showhn](https://news.ycombinator.com/showhn.html)) | Verified. TopForNow qualifies: try/vote without signup |
| **Peerlist Launchpad** | Indie makers, strong India + global community | Individual profiles only (real name/photo); product needs tagline, cover, demo link; **no DMing strangers for upvotes, no upvote begging, zero tolerance for spam** ([guidelines](https://help.peerlist.io/individual/spotlight/guidelines-faqs)) | Verified |
| **Product Hunt** | Broad maker audience | Schedule from tomorrow up to 30 days ahead; live 12:01 AM PT for a day; a hunter can post 2 launches/day ([help](https://help.producthunt.com/en/articles/17734355-3-set-your-goals-and-pick-your-launch-date)). Promotion rules are in a separate guide we could not read | Partly verified |
| **r/SideProject** (~907K members per [Hive Index](https://thehiveindex.com/communities/r-sideproject)) | "Share and get feedback" | Reddit blocked our reader; **read the sidebar rules before posting** | Unverified |
| **Indie Hackers** (site + r/indiehackers) | Exact audience; Outbid stories did well there | No official promo rules found; community norm is story-first posts with numbers | Unverified |
| **Writers who already covered Outbid**: Startup Spells, Superframeworks, the Klymentiev case study, alexcloudstar (ownthe.day) | Already explain this genre to this audience | Personal pitch, offer our data; no paid placement | Contact rules per site |

## Day by day

| Day | Action | Cost | Measure (admin funnel, `?ref=` codes) |
|---|---|---|---|
| 1 | Deploy, set domain. Vikash posts the **free founding opinion** himself (so round 0 isn't wasted on spam). Create the Peerlist profile | $0 | Site up, round 0 live |
| 2 | Write a short "I'm building an Outbid alternative where the price doubles" build-in-public post on X + Peerlist Scroll + IH, link `?ref=x` / `?ref=pl` / `?ref=ih` | $0 | Visitors, votes |
| 3 | **Personal outreach, 15 messages/day** (template below) to makers who posted "launched today" on X, Peerlist, IH. Offer: the whole spot for the current price ($1–$8) | $0 | Replies, checkout starts |
| 4 | Every time the spot changes, post it (tag the buyer, attach their Ego Receipt). This is the Outbid engine, done by hand | $0 | Shares, receipt downloads |
| 5 | 15 more DMs. Post in r/SideProject (after reading rules) `?ref=reddit-sp` | $0 | |
| 6 | Email the four writers who covered Outbid with real numbers so far | $0 | Replies |
| 7 | **Show HN** (Tuesday–Thursday, 8–10 AM US Eastern) `?ref=hn`. Stay in comments all day. Never ask for upvotes | $0 | HN visitors, votes, rounds |
| 8 | Peerlist Launchpad launch `?ref=pl-launch` | $0 | |
| 9 | Publish a numbers post: "First week: N rounds, $X, what buyers got" with results-page links | $0 | |
| 10 | **Paid test only if round ≥ 6 ($32+) has sold:** Reddit ads $5/day × 5 days on Variant A vs B `?ref=ad-a` / `?ref=ad-b` | **$25, needs approval** | CPC, checkout starts per click |
| 11–12 | 15 DMs/day; post each spot change | $0 | |
| 13 | Product Hunt launch (scheduled on day 6) `?ref=ph` | $0 | |
| 14 | Review: keep, change or stop (rules below) | $0 | |

## Finding the first ten paying customers

1. **Seed 30 named leads before launch:** makers who shipped in the last 7 days (X search "launched today", Peerlist Launchpad weekly list, IH product posts). Spreadsheet: name, product, link, date contacted, reply.
2. **Sell the cheap rounds by hand.** Rounds 1–5 cost $1–$16. Pitch them as "the cheapest public stunt you'll buy this year, with a results page". Expect 1 buyer per 10–20 personal messages **[Estimate]**.
3. **Every buyer recruits the next.** After payment the buyer sees "Share my Ego Receipt". We post the change too, tagging them. Their followers see the next price.
4. **Creators:** pitch small YouTubers/Reels makers (1k–20k subs) in the maker niche: "$4 puts your video as the only thing on our homepage".
5. **Respond links:** whenever an opinion is on top, invite the people who'd disagree: `{SITE}/submit?reply_to=<slug>`.

**Stop / expand rules**

- Stop a channel after **100 visitors with 0 checkout starts**.
- Stop the paid test if **CPC > $1** or **0 checkout starts after 50 clicks**.
- Expand a channel that produces **≥1 paid round per $25 spent** or per 30 DMs.
- If no paid round sells by day 7, change the pitch, not the price rule.

## Copy (ready to use after approval)

### Launch post (Show HN)

**Title:** Show HN: TopForNow – one spot on the internet, and the price doubles every time someone takes it

> I built a tiny game after watching Outbid.lol. There's exactly one spot on the homepage. The first post was free. Replacing it costs $1, then $2, $4, $8, $16… Each buyer pays the full displayed price and stays on top until someone pays double. Replaced posts keep their own page with votes, time on top and real results (views, clicks, and video plays only when the YouTube player reports them).
>
> Anyone can read and vote free, no signup. It can be a one-line opinion, a YouTube/Instagram post, or a product.
>
> Built solo in Go + Postgres. Checkout reserves the round for 15 minutes so two people can't pay the same price; if a payment ever lands after the round was taken, it's refunded automatically. Happy to talk about the payment edge cases.
>
> {SITE}

### Build-in-public post (X / Peerlist / IH)

> Outbid.lol showed people will pay to be seen. I made the brutal version: one spot, price doubles every time.
>
> Right now the top spot costs ${PRICE}. Next person pays double. Votes are free.
>
> Whoever's on top gets an Ego Receipt. Look At Me Tax, basically.
> {SITE}?ref=x

### Personal outreach message (send only with approval)

> Hi {first name}, saw you launched {product} this week, congrats. I run TopForNow, a one-spot homepage: whatever's on top is the only thing visitors see until someone pays double. The spot costs ${PRICE} right now (it doubles after you). You'd get a public results page with views and clicks, and your post keeps its own page after you're replaced. Want me to send the link? No pressure either way.

### Share text (after the spotlight changes, so replies hit the right post)

- Paid post: `I paid ${PAID} to make you read this. It worked. {SITE}/p/{slug}?ref=receipt`
- Free founding post: `I took the first spotlight for free. {SITE}/p/{slug}?ref=receipt`
- Visitor sharing an opinion: `“{headline}” Agree or disagree? {SITE}/p/{slug}?ref=share` (the link always opens that exact post, even after it's replaced, with a button to see the current top and a "Respond with your own post" link)

### Look At Me Tax: two ad variants (Reddit/X), both on the $16 Ego Receipt

Creative: `docs/creative/look-at-me-tax-16-portrait-PREVIEW.png` (feed) and `...-landscape-PREVIEW.png` (link card). The card headline is fixed: **I paid $16 to make you read this. It worked.** The image carries a **PREVIEW · NOT A REAL PURCHASE** stamp, because no one has bought round 5 yet. Swap in a real buyer's receipt (with their permission) once one exists.

| | Variant A: "proof" | Variant B: "your turn" |
|---|---|---|
| Caption | Look At Me Tax: $16. Receipt attached. | Look At Me Tax is due. One spot, everyone sees it, price doubles every time. |
| CTA button | See who's on top | Take the spot |
| Landing | `{SITE}/?ref=ad-a` | `{SITE}/submit?ref=ad-b` |
| Tests | Curiosity → visit and vote | Intent → checkout start |

## Zero-spend route

Days 1–9 and 11–14 cost nothing: own posts, personal messages (approved), Show HN, Peerlist, Product Hunt, writer pitches, posting every spot change. Paid ads are optional and gated on round 6 selling.

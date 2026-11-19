// Browser walk-through of the acceptance case on phone and desktop sizes.
// Usage: BASE=http://localhost:8080 OUT=docs/screenshots node scripts/browser_acceptance.js
// Expects a fresh sandbox database (PAYMENT_MODE=sandbox).
const { chromium, devices } = require("playwright");
const base = process.env.BASE || "http://localhost:8080";
const out = process.env.OUT || "docs/screenshots";
const UA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1";
const log = (...a) => console.log(...a);
const assert = (c, m) => { if (!c) { throw new Error("ASSERT: " + m); } log("  ok:", m); };

async function price(page) {
  const r = await page.request.get(base + "/api/state");
  return (await r.json()).next_price_cents;
}

async function submit(page, kind, headline, link, author) {
  await page.goto(base + "/submit");
  await page.check(`input[name=kind][value=${kind}]`);
  await page.fill("input[name=headline]", headline);
  if (link) await page.fill("input[name=link]", link);
  await page.fill("input[name=author]", author);
  await page.check("input[name=agree]");
  await page.click("button:has-text('Preview my post')");
  await page.waitForURL(/\/draft\//);
}

async function buy(page, kind, headline, link, author, shot) {
  await submit(page, kind, headline, link, author);
  if (shot) await page.screenshot({ path: `${out}/${shot}-preview.png`, fullPage: true });
  await page.click("form[action$='/checkout'] button");
  await page.waitForURL(/\/sandbox\/checkout\//);
  if (shot) await page.screenshot({ path: `${out}/${shot}-sandbox-checkout.png`, fullPage: true });
  await page.click("button[value=pay]");
  await page.waitForURL(/\/draft\/.*n=paid/, { timeout: 15000 });
  if (shot) await page.screenshot({ path: `${out}/${shot}-published.png`, fullPage: true });
}

(async () => {
  const browser = await chromium.launch();
  const phone = await browser.newContext({ ...devices["iPhone 13"], userAgent: UA, locale: "en-US" });
  const desk = await browser.newContext({ viewport: { width: 1280, height: 900 }, userAgent: UA.replace("iPhone", "Macintosh"), locale: "en-US" });
  const p = await phone.newPage();
  const d = await desk.newPage();
  const errors = [];
  for (const pg of [p, d]) pg.on("console", (m) => { if (m.type() === "error") errors.push(m.text()); });

  log("1. Free founding opinion (phone)");
  await p.goto(base + "/");
  await p.screenshot({ path: `${out}/01-phone-empty-home.png`, fullPage: true });
  assert(await p.isVisible("text=Take the first spotlight for free"), "empty home offers the free spot");
  await submit(p, "opinion", "Pineapple belongs on pizza and I will not be taking questions", "", "Vikash");
  await p.screenshot({ path: `${out}/02-phone-founding-preview.png`, fullPage: true });
  await p.click("button:has-text('Publish the founding post free')");
  await p.waitForURL(/n=founding/);
  assert(await p.isVisible("text=I took the first spotlight for free."), "free post share text shown");
  await p.screenshot({ path: `${out}/03-phone-founding-published.png`, fullPage: true });
  assert((await price(p)) === 100, "next price is $1");

  log("2. Desktop visitor votes Agree on the opinion");
  await d.goto(base + "/");
  await d.click(".card-big .vote.up");
  await d.waitForFunction(() => document.querySelector(".card-big .vote.up .n").textContent === "1");
  assert(true, "vote counted without reload");
  await d.screenshot({ path: `${out}/04-desktop-home-founding.png`, fullPage: true });

  log("3. $1 creator video (phone)");
  await buy(p, "creator", "I tried to fold a fitted sheet for 30 days", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "SheetTok", "05-phone-1usd-video");
  assert((await price(p)) === 200, "next price is $2");

  log("4. $2 product (desktop)");
  await buy(d, "product", "Invoices for freelancers in 30 seconds", "https://example.com/?utm_source=x&plan=free", "Acme Invoicing", "06-desktop-2usd-product");
  assert((await price(d)) === 400, "next price is $4");
  await d.goto(base + "/");
  await d.screenshot({ path: `${out}/07-desktop-home-after-2usd.png`, fullPage: true });
  assert(await d.isVisible("text=Take the spotlight for $4"), "button shows $4");
  const opinionVotes = await d.textContent(".past .kind-opinion .vote.up .n");
  assert(opinionVotes.trim() === "1", "replaced opinion keeps its Agree vote");

  log("5. $4, $8, $16");
  await buy(p, "opinion", "Tabs are better than spaces", "", "Indent Club", null);
  await buy(d, "product", "Watch our 60-second demo", "https://demo.example.org/", "Demo Co", null);
  await buy(p, "product", "The $16 product", "https://sixteen.example.com/", "Sixteen Inc", "08-phone-16usd");
  assert((await price(p)) === 3200, "next price is $32 after the $16 post");
  const receiptText = await p.getAttribute(".receipt-img", "alt");
  assert(receiptText.includes("I paid $16 to make you read this. It worked."), "receipt alt carries exact $16 sentence");
  const draftURL = p.url().split("?")[0];
  const link16 = await p.getAttribute(".copy", "data-url");
  const slug16 = link16.match(/\/p\/([a-z0-9]+)/)[1];
  // Download image
  const [dl] = await Promise.all([p.waitForEvent("download"), p.click("a:has-text('Download image')")]);
  assert(dl.suggestedFilename() === "topfornow-ego-receipt-16.png", "download filename " + dl.suggestedFilename());
  await dl.saveAs(`${out}/09-ego-receipt-16-download.png`);
  const og = await p.request.get(base + "/receipt/" + slug16 + "?format=og");
  require("fs").writeFileSync(`${out}/10-ego-receipt-16-social-preview.png`, await og.body());

  log("6. Replace once more ($32), then open the $16 shared link");
  await buy(d, "creator", "Reel: the most Indian thing ever", "https://www.instagram.com/reel/C9abcDEF12/", "Reel Person", null);
  assert((await price(d)) === 6400, "next price is $64");
  const reader = await browser.newContext({ ...devices["Pixel 7"], userAgent: UA, locale: "en-US" });
  const r = await reader.newPage();
  await r.goto(link16);
  await r.screenshot({ path: `${out}/11-phone-16usd-shared-link-after-replacement.png`, fullPage: true });
  assert(await r.isVisible("text=The $16 product"), "shared link still shows the $16 post");
  assert(await r.isVisible("text=See what's on top now"), "points to current spotlight");
  const ogDesc = await r.getAttribute('meta[property="og:description"]', "content");
  assert(ogDesc.startsWith("I paid $16 to make you read this. It worked."), "social preview text");
  await r.click(".card-big .vote.up");
  await r.waitForFunction(() => document.querySelector(".card-big .vote.up").classList.contains("on"));
  assert(true, "reader can vote on the replaced $16 post");
  await r.goto(base + "/p/" + slug16 + "/results");
  await r.screenshot({ path: `${out}/12-phone-16usd-results.png`, fullPage: true });

  log("7. Admin");
  await d.goto(base + "/admin");
  assert(d.url().includes("/admin/login"), "admin redirects to login");
  await d.fill("input[name=password]", process.env.ADMIN_PASSWORD || "admin-pass-123");
  await d.click("button:has-text('Sign in')");
  await d.waitForURL(/\/admin$/);
  assert(await d.isVisible("text=All invariants hold"), "reconciliation clean");
  await d.screenshot({ path: `${out}/13-desktop-admin.png`, fullPage: true });

  log("8. Phone home final");
  await p.goto(base + "/");
  await p.screenshot({ path: `${out}/14-phone-home-final.png`, fullPage: true });
  await p.screenshot({ path: `${out}/14b-phone-home-first-screen.png` });
  const overflow = await p.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);
  assert(!overflow, "no horizontal scroll on phone");
  const relevant = errors.filter((e) => !/i\.ytimg|instagram|youtube|ERR_|Failed to load resource/.test(e));
  assert(relevant.length === 0, "no app console errors " + JSON.stringify(relevant));
  await browser.close();
  log("BROWSER ACCEPTANCE PASSED");
})().catch((e) => { console.error(e); process.exit(1); });

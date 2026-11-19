package web

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/vikash-parashar/topfornow/internal/core"
)

var (
	colInk   = color.RGBA{0x14, 0x11, 0x0f, 0xff}
	colPaper = color.RGBA{0xff, 0xf6, 0xe0, 0xff}
	colAmber = color.RGBA{0xff, 0xb7, 0x03, 0xff}
	colCoral = color.RGBA{0xe8, 0x4a, 0x27, 0xff}
	colMuted = color.RGBA{0x6b, 0x5e, 0x50, 0xff}
)

var (
	faceMu    sync.Mutex
	faceCache = map[string]font.Face{}
	fontData  = map[string][]byte{"bold": gobold.TTF, "reg": goregular.TTF, "mono": gomono.TTF, "monob": gomonobold.TTF}
)

func face(name string, size float64) font.Face {
	key := fmt.Sprintf("%s-%.0f", name, size)
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faceCache[key]; ok {
		return f
	}
	ft, err := opentype.Parse(fontData[name])
	if err != nil {
		panic(err)
	}
	f, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	faceCache[key] = f
	return f
}

type canvas struct{ img *image.RGBA }

func newCanvas(w, h int, bg color.Color) *canvas {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	return &canvas{img}
}

func (c *canvas) rect(x0, y0, x1, y1 int, col color.Color) {
	draw.Draw(c.img, image.Rect(x0, y0, x1, y1), &image.Uniform{col}, image.Point{}, draw.Over)
}

func (c *canvas) text(s string, x, y int, f font.Face, col color.Color) {
	d := &font.Drawer{Dst: c.img, Src: &image.Uniform{col}, Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func width(s string, f font.Face) int { return font.MeasureString(f, s).Round() }

func (c *canvas) centered(s string, cx, y int, f font.Face, col color.Color) {
	c.text(s, cx-width(s, f)/2, y, f, col)
}

func wrap(s string, f font.Face, max int) []string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, w := range words {
		try := strings.TrimSpace(cur + " " + w)
		if width(try, f) > max && cur != "" {
			lines = append(lines, cur)
			cur = w
		} else {
			cur = try
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func (c *canvas) dashed(x0, x1, y int, col color.Color) {
	for x := x0; x < x1; x += 22 {
		c.rect(x, y, min(x+12, x1), y+3, col)
	}
}

// zigzag draws a torn-receipt edge.
func (c *canvas) zigzag(x0, x1, y, depth int, down bool, col color.Color) {
	step := depth * 2
	for x := x0; x < x1; x += step {
		for i := 0; i < step; i++ {
			h := i
			if i > depth {
				h = step - i
			}
			if down {
				c.rect(x+i, y, x+i+1, y+h, col)
			} else {
				c.rect(x+i, y-h, x+i+1, y, col)
			}
		}
	}
}

// ReceiptInput is everything printed on an Ego Receipt. It deliberately has
// no payment id and no buyer contact.
type ReceiptInput struct {
	PaidCents  int64
	Round      int
	Date       time.Time
	Headline   string
	Author     string
	Kind       string
	PostURL    string
	Preview    bool
	OG         bool // 1200x630 social preview; otherwise 1080x1350 download
}

func renderReceipt(in ReceiptInput) []byte {
	if in.OG {
		return renderReceiptOG(in)
	}
	W, H := 1080, 1350
	c := newCanvas(W, H, colInk)
	// spotlight glow
	for i := 0; i < 18; i++ {
		a := uint8(10 + i)
		c.rect(W/2-460+i*25, 0, W/2+460-i*25, 70-i*3, color.RGBA{0xff, 0xb7, 0x03, a})
	}
	px0, px1, py0, py1 := 90, W-90, 110, H-110
	c.rect(px0, py0, px1, py1, colPaper)
	c.zigzag(px0, px1, py0, 12, false, colPaper)
	c.zigzag(px0, px1, py1, 12, true, colPaper)
	inner0, inner1 := px0+60, px1-60

	y := py0 + 90
	c.centered("E G O   R E C E I P T", W/2, y, face("monob", 46), colInk)
	y += 44
	c.centered("spotlight placement · proof of attention", W/2, y, face("mono", 24), colMuted)
	y += 40
	c.dashed(inner0, inner1, y, colMuted)

	// The sentence: largest text on the card.
	big := face("bold", 84)
	lines := wrap(core.EgoReceiptHeadline(in.PaidCents), big, inner1-inner0)
	if len(lines) > 4 {
		big = face("bold", 70)
		lines = wrap(core.EgoReceiptHeadline(in.PaidCents), big, inner1-inner0)
	}
	y += 110
	for _, l := range lines {
		c.text(l, inner0, y, big, colInk)
		y += 96
	}
	y += 6
	c.dashed(inner0, inner1, y, colMuted)
	y += 62

	mono := face("mono", 30)
	item := func(k, v string) {
		c.text(k, inner0, y, mono, colMuted)
		c.text(v, inner1-width(v, mono), y, mono, colInk)
		y += 48
	}
	item("ITEM", "TopForNow spotlight")
	item("ROUND", "#"+strconv.Itoa(in.Round))
	paid := core.FormatUSD(in.PaidCents)
	if in.PaidCents == 0 {
		paid = "$0 (founding post)"
	}
	item("PAID", paid)
	item("DATE", strings.ToUpper(in.Date.UTC().Format("02 Jan 2006")))
	item("BY", truncateRunes(in.Author, 26))
	y += 6
	c.dashed(inner0, inner1, y, colMuted)
	y += 58
	quote := face("reg", 30)
	for i, l := range wrap("“"+truncateRunes(in.Headline, 110)+"”", quote, inner1-inner0) {
		if i == 3 {
			break
		}
		c.text(l, inner0, y, quote, colInk)
		y += 42
	}

	// Signature.
	sig := face("bold", 64)
	sy := py1 - 120
	c.dashed(inner0, inner1, sy-80, colMuted)
	sw := width("TopForNow", sig)
	c.text("TopForNow", W/2-sw/2, sy, sig, colInk)
	c.rect(W/2+sw/2+8, sy-14, W/2+sw/2+22, sy, colCoral)
	c.centered(strings.TrimPrefix(strings.TrimPrefix(in.PostURL, "https://"), "http://"), W/2, sy+46, face("mono", 24), colMuted)

	if in.Preview {
		previewStamp(c, W, H)
	}
	return encode(c)
}

func renderReceiptOG(in ReceiptInput) []byte {
	W, H := 1200, 630
	c := newCanvas(W, H, colInk)
	px0, px1, py0, py1 := 60, W-60, 50, H-50
	c.rect(px0, py0, px1, py1, colPaper)
	c.zigzag(px0, px1, py0, 10, false, colPaper)
	c.zigzag(px0, px1, py1, 10, true, colPaper)
	x0, x1 := px0+50, px1-50
	y := py0 + 62
	c.text("E G O   R E C E I P T", x0, y, face("monob", 34), colInk)
	r := "ROUND #" + strconv.Itoa(in.Round) + " · " + strings.ToUpper(in.Date.UTC().Format("02 Jan 2006"))
	c.text(r, x1-width(r, face("mono", 24)), y-4, face("mono", 24), colMuted)
	y += 26
	c.dashed(x0, x1, y, colMuted)
	big := face("bold", 66)
	lines := wrap(core.EgoReceiptHeadline(in.PaidCents), big, x1-x0)
	if len(lines) > 3 {
		big = face("bold", 54)
		lines = wrap(core.EgoReceiptHeadline(in.PaidCents), big, x1-x0)
	}
	y += 88
	for _, l := range lines {
		c.text(l, x0, y, big, colInk)
		y += 78
	}
	quote := face("reg", 26)
	if l := wrap("“"+truncateRunes(in.Headline, 80)+"” by "+truncateRunes(in.Author, 24), quote, x1-x0); len(l) > 0 {
		c.text(l[0], x0, y+4, quote, colMuted)
	}
	sig := face("bold", 50)
	sy := py1 - 40
	c.dashed(x0, x1, sy-66, colMuted)
	c.text("TopForNow", x1-width("TopForNow", sig)-20, sy, sig, colInk)
	c.rect(x1-16, sy-12, x1-4, sy, colCoral)
	c.text("Read it. Vote free. Or take the spot.", x0, sy-6, face("mono", 24), colMuted)
	if in.Preview {
		previewStamp(c, W, H)
	}
	return encode(c)
}

func previewStamp(c *canvas, W, H int) {
	f := face("monob", 34)
	label := "PREVIEW · NOT A REAL PURCHASE"
	w := width(label, f) + 48
	c.rect(W-w-30, 22, W-30, 78, colCoral)
	c.text(label, W-w-6, 62, f, colPaper)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func encode(c *canvas) []byte {
	var b bytes.Buffer
	png.Encode(&b, c.img)
	return b.Bytes()
}

func writePNG(w http.ResponseWriter, b []byte, download string, maxAge int) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(maxAge))
	if download != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+download+`"`)
	}
	w.Write(b)
}

// receiptImage renders a published post's Ego Receipt from the amount that
// post actually paid, never from the current price.
func (s *Server) receiptImage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSuffix(r.PathValue("slug"), ".png")
	p, err := s.Svc.PostBySlug(r.Context(), slug)
	if err != nil || p.Round == nil || p.PublishedAt == nil || p.Status == "removed" {
		http.NotFound(w, r)
		return
	}
	in := ReceiptInput{PaidCents: p.PriceCents, Round: *p.Round, Date: *p.PublishedAt, Headline: p.Headline,
		Author: p.AuthorName, Kind: p.Kind, PostURL: s.BaseURL + "/p/" + p.Slug, OG: r.URL.Query().Get("format") == "og"}
	dl := ""
	if r.URL.Query().Get("dl") == "1" {
		dl = fmt.Sprintf("topfornow-ego-receipt-%s.png", strings.TrimPrefix(core.FormatUSD(p.PriceCents), "$"))
		s.Svc.Track(r.Context(), core.EvReceiptView, visitorFrom(r), p.ID, "", r.URL.Path, isBot(r))
	}
	writePNG(w, renderReceipt(in), dl, 86400)
}

// receiptPreview renders campaign creative, always stamped PREVIEW.
func (s *Server) receiptPreview(w http.ResponseWriter, r *http.Request) {
	dollars, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("dollars"), ".png"))
	if err != nil || dollars < 1 || dollars > 1<<20 {
		http.NotFound(w, r)
		return
	}
	round := 1
	for v := 1; v < dollars; v *= 2 {
		round++
	}
	in := ReceiptInput{PaidCents: int64(dollars) * 100, Round: round, Date: time.Now(), Headline: "Your one line goes here. Everyone sees it until someone pays double.",
		Author: "You", PostURL: s.BaseURL, Preview: true, OG: r.URL.Query().Get("format") == "og"}
	writePNG(w, renderReceipt(in), "", 3600)
}

func (s *Server) homeImage(w http.ResponseWriter, r *http.Request) {
	st, err := s.Svc.GetState(r.Context())
	if err != nil {
		http.Error(w, "error", 500)
		return
	}
	W, H := 1200, 630
	c := newCanvas(W, H, colInk)
	for i := 0; i < 24; i++ {
		c.rect(W/2-520+i*20, 0, W/2+520-i*20, 300-i*12, color.RGBA{0xff, 0xb7, 0x03, uint8(6 + i)})
	}
	c.centered("TopForNow", W/2, 250, face("bold", 120), colPaper)
	c.centered("One public top spot. Read and vote free.", W/2, 340, face("reg", 40), colPaper)
	price := "Take it for " + core.FormatUSD(st.NextPriceCents)
	if st.NextPriceCents == 0 {
		price = "The first spotlight is free"
	}
	f := face("bold", 52)
	pw := width(price, f) + 80
	c.rect(W/2-pw/2, 410, W/2+pw/2, 500, colAmber)
	c.centered(price, W/2, 474, f, colInk)
	c.centered("Every replacement costs double the last.", W/2, 570, face("mono", 28), colAmber)
	writePNG(w, encode(c), "", 300)
}

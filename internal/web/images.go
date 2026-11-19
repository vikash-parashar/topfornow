package web

import (
	"bytes"
	_ "embed"
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
	colInk   = color.RGBA{0x12, 0x11, 0x10, 0xff}
	colPaper = color.RGBA{0xf7, 0xf3, 0xea, 0xff}
	colAmber = color.RGBA{0xff, 0xe1, 0x4a, 0xff}
	colCoral = color.RGBA{0xe2, 0x23, 0x1a, 0xff}
	colMuted = color.RGBA{0x6b, 0x5e, 0x50, 0xff}
)

var (
	//go:embed cardfonts/Anton-Regular.ttf
	ttfAnton []byte
	//go:embed cardfonts/Fraunces72pt-Black.ttf
	ttfFraunces []byte
	//go:embed cardfonts/Fraunces72pt-SemiBoldItalic.ttf
	ttfFrauncesIt []byte
	//go:embed cardfonts/IBMPlexMono-Regular.ttf
	ttfPlex []byte
	//go:embed cardfonts/IBMPlexMono-SemiBold.ttf
	ttfPlexB []byte
)

var (
	faceMu    sync.Mutex
	faceCache = map[string]font.Face{}
	fontData  = map[string][]byte{"bold": gobold.TTF, "reg": goregular.TTF, "mono": gomono.TTF, "monob": gomonobold.TTF,
		"anton": ttfAnton, "fraunces": ttfFraunces, "frauncesIt": ttfFrauncesIt, "plex": ttfPlex, "plexb": ttfPlexB}
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
	PaidCents int64
	Round     int
	Date      time.Time
	Headline  string
	Author    string
	Kind      string
	PostURL   string
	Preview   bool
	OG        bool // 1200x630 social preview; otherwise 1080x1350 download
}

// Ego Receipt, "Extra Edition" style: a newspaper clipping torn out of the
// TopForNow front page, taped onto ink, with the buyer's line highlighted
// and a red PAID stamp.
var (
	colNews  = color.RGBA{0xef, 0xe9, 0xdc, 0xff}
	colInk2  = color.RGBA{0x3a, 0x37, 0x32, 0xff}
	colTape  = color.NRGBA{0xff, 0xe1, 0x4a, 0xb8}
	colHiLit = color.RGBA{0xff, 0xe1, 0x4a, 0xff}
)

// rnd is a tiny deterministic generator so the same receipt always renders
// the same torn edge and paper grain.
type rnd uint32

func (r *rnd) next(n int) int {
	*r = *r*1664525 + 1013904223
	return int(uint32(*r)>>8) % n
}

// clipping draws newsprint with torn edges and a light grain.
func (c *canvas) clipping(x0, y0, x1, y1 int, seed uint32) {
	r := rnd(seed)
	c.rect(x0+10, y0+10, x1-10, y1-10, colNews)
	for x := x0 + 10; x < x1-10; x += 5 {
		c.rect(x, y0+10-r.next(10), x+5, y0+10, colNews)
		c.rect(x, y1-10, x+5, y1-10+r.next(10), colNews)
	}
	for y := y0 + 10; y < y1-10; y += 5 {
		c.rect(x0+10-r.next(10), y, x0+10, y+5, colNews)
		c.rect(x1-10, y, x1-10+r.next(10), y+5, colNews)
	}
	grain := color.NRGBA{0x12, 0x11, 0x10, 0x16}
	n := (x1 - x0) * (y1 - y0) / 260
	for i := 0; i < n; i++ {
		x, y := x0+12+r.next(x1-x0-24), y0+12+r.next(y1-y0-24)
		c.rect(x, y, x+2, y+2, grain)
	}
}

func (c *canvas) border(x0, y0, x1, y1, t int, col color.Color) {
	c.rect(x0, y0, x1, y0+t, col)
	c.rect(x0, y1-t, x1, y1, col)
	c.rect(x0, y0, x0+t, y1, col)
	c.rect(x1-t, y0, x1, y1, col)
}

func (c *canvas) doubleRule(x0, x1, y int) {
	c.rect(x0, y, x1, y+4, colInk)
	c.rect(x0, y+9, x1, y+11, colInk)
}

func (c *canvas) dotted(x0, x1, y int, col color.Color) {
	for x := x0; x < x1; x += 8 {
		c.rect(x, y, x+3, y+3, col)
	}
}

// fitFace returns the largest size of the named face at which s fits in w.
func fitFace(name, s string, w int, maxSize float64) font.Face {
	for size := maxSize; size > 10; size -= 2 {
		if f := face(name, size); width(s, f) <= w {
			return f
		}
	}
	return face(name, 10)
}

// highlighted draws wrapped lines with a yellow marker stroke under the
// lower half of each line, as on the homepage headline.
func (c *canvas) highlighted(lines []string, x, y int, f font.Face, size, lead float64) int {
	for _, l := range lines {
		w := width(l, f)
		c.rect(x-6, y-int(size*0.42), x+w+8, y+int(size*0.10), colHiLit)
		c.text(l, x, y, f, colInk)
		y += int(size * lead)
	}
	return y
}

// stamp draws the red PAID / FREE stamp: a double red border, a kicker and the amount.
func (c *canvas) stamp(x0, y0, x1, y1 int, kicker, amount string) {
	c.border(x0, y0, x1, y1, 5, colCoral)
	c.border(x0+11, y0+11, x1-11, y1-11, 2, colCoral)
	h := y1 - y0
	k := face("plexb", float64(h/9))
	c.centered(kicker, (x0+x1)/2, y0+h*28/100, k, colCoral)
	a := fitFace("anton", amount, x1-x0-50, float64(h)*0.44)
	c.centered(amount, (x0+x1)/2, y1-h*15/100, a, colCoral)
}

func receiptBits(in ReceiptInput) (headline, paid, stampKick, stampAmt, date, url string) {
	headline = core.EgoReceiptHeadline(in.PaidCents)
	paid = core.FormatUSD(in.PaidCents)
	stampKick, stampAmt = "PAID", paid
	if in.PaidCents == 0 {
		paid, stampKick, stampAmt = "$0, founding post", "FIRST", "FREE"
	}
	date = strings.ToUpper(in.Date.UTC().Format("Mon 2 Jan 2006"))
	url = strings.TrimPrefix(strings.TrimPrefix(in.PostURL, "https://"), "http://")
	return
}

func renderReceipt(in ReceiptInput) []byte {
	if in.OG {
		return renderReceiptOG(in)
	}
	W, H := 1080, 1350
	c := newCanvas(W, H, colInk)
	headline, paid, sk, sa, date, url := receiptBits(in)
	px0, py0, px1, py1 := 64, 96, W-64, H-80
	c.clipping(px0, py0, px1, py1, uint32(in.Round*7919+len(in.Headline)))
	c.rect(W/2-120, py0-26, W/2+120, py0+40, colTape)
	x0, x1 := px0+58, px1-58

	y := py0 + 74
	mono := face("plex", 22)
	c.text("VOL. I  ·  NO. "+strconv.Itoa(in.Round), x0, y, mono, colInk)
	c.text(date, x1-width(date, mono), y, mono, colInk)
	c.rect(x0, y+14, x1, y+16, colInk)
	mast := fitFace("anton", "TOPFORNOW", x1-x0, 160)
	y += 16 + int(float64(mast.Metrics().Ascent.Round())*0.97)
	c.centered("TOPFORNOW", W/2, y, mast, colInk)
	y += 20
	c.doubleRule(x0, x1, y)
	y += 46
	it := face("frauncesIt", 25)
	c.text("One spot on the internet.", x0, y, it, colInk)
	m := "Every front page costs double."
	c.text(m, x1-width(m, it), y, it, colInk)
	y += 18
	c.rect(x0, y, x1, y+2, colInk)

	y += 62
	c.text("EGO RECEIPT  ·  PAID PLACEMENT  ·  EDITION "+strconv.Itoa(in.Round), x0, y, face("plexb", 22), colCoral)

	q := face("frauncesIt", 31)
	quote := wrap("“"+truncateRunes(in.Headline, 110)+"”", q, x1-x0)
	if len(quote) > 2 {
		quote = quote[:2]
	}
	// Everything below the headline has a fixed height; the headline takes
	// the largest size that leaves room for it.
	below := 4 + 52 + len(quote)*40 + 2 + 40 + 214 + 104
	size := 100.0
	var big font.Face
	var lines []string
	for ; size >= 56; size -= 4 {
		big = face("fraunces", size)
		lines = wrap(headline, big, x1-x0-10)
		if y+int(size)+12+int(float64(len(lines))*size*1.04)-int(size*0.5)+below <= py1 {
			break
		}
	}
	y += int(size) + 12
	y = c.highlighted(lines, x0, y, big, size, 1.04) - int(size*0.5)

	y += 4
	c.dotted(x0, x1, y, colInk)
	y += 52
	for _, l := range quote {
		c.text(l, x0, y, q, colInk)
		y += 40
	}
	c.text("BY "+strings.ToUpper(truncateRunes(in.Author, 28)), x0, y+2, face("plexb", 22), colInk2)

	// Receipt box with a hard shadow, and the stamp beside it.
	by0 := y + 40
	by1 := by0 + 214
	bx1 := x0 + 520
	c.rect(x0+9, by0+9, bx1+9, by1+9, colInk)
	c.rect(x0, by0, bx1, by1, colNews)
	c.border(x0, by0, bx1, by1, 3, colInk)
	row := face("plex", 24)
	rowb := face("plexb", 24)
	ry := by0 + 48
	for _, kv := range [][2]string{{"ITEM", "The front page"}, {"ROUND", "#" + strconv.Itoa(in.Round)}, {"PAID", paid}, {"BY", truncateRunes(in.Author, 18)}} {
		c.text(kv[0], x0+24, ry, row, colInk2)
		c.text(kv[1], bx1-24-width(kv[1], rowb), ry, rowb, colInk)
		ry += 46
	}
	c.stamp(bx1+52, by0+8, x1, by1-8, sk, sa)

	fy := py1 - 64
	c.rect(x0, fy-38, x1, fy-36, colInk)
	foot := face("plex", 22)
	c.text(url, x0, fy, foot, colInk2)
	if t := "Read it. Vote free. Or take the spot."; width(url, foot)+width(t, foot)+40 <= x1-x0 {
		c.text(t, x1-width(t, foot), fy, foot, colInk2)
	} else if t := "Vote free."; width(url, foot)+width(t, foot)+40 <= x1-x0 {
		c.text(t, x1-width(t, foot), fy, foot, colInk2)
	}

	if in.Preview {
		previewStamp(c, W, H)
	}
	return encode(c)
}

func renderReceiptOG(in ReceiptInput) []byte {
	W, H := 1200, 630
	c := newCanvas(W, H, colInk)
	headline, _, sk, sa, date, url := receiptBits(in)
	px0, py0, px1, py1 := 34, 34, W-34, H-34
	c.clipping(px0, py0, px1, py1, uint32(in.Round*104729+len(in.Headline)))
	x0, x1 := px0+46, px1-46

	y := py0 + 54
	mono := face("plex", 19)
	mast := face("anton", 58)
	c.text("TOPFORNOW", x0, y+18, mast, colInk)
	r := "EGO RECEIPT · EDITION " + strconv.Itoa(in.Round) + " · " + date
	c.text(r, x1-width(r, mono), y+12, face("plexb", 19), colCoral)
	y += 34
	c.doubleRule(x0, x1, y)

	colW := x1 - x0 - 300
	size := 70.0
	big := face("fraunces", size)
	lines := wrap(headline, big, colW)
	for len(lines) > 3 && size > 46 {
		size -= 6
		big = face("fraunces", size)
		lines = wrap(headline, big, colW)
	}
	y += 20 + int(size)
	y = c.highlighted(lines, x0, y, big, size, 1.06)
	q := face("frauncesIt", 26)
	if l := wrap("“"+truncateRunes(in.Headline, 80)+"” by "+truncateRunes(in.Author, 24), q, colW); len(l) > 0 {
		c.text(l[0], x0, y+6, q, colInk2)
	}
	c.stamp(x1-250, py0+150, x1, py0+400, sk, sa)

	fy := py1 - 40
	c.rect(x0, fy-32, x1, fy-30, colInk)
	c.text(url, x0, fy, mono, colInk2)
	t := "Read it. Vote free. Or take the spot."
	c.text(t, x1-width(t, mono), fy, mono, colInk2)
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
		c.rect(W/2-520+i*20, 0, W/2+520-i*20, 300-i*12, color.NRGBA{0xe2, 0x23, 0x1a, uint8(2 + i/3)})
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

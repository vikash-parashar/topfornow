// Package safety validates links, detects approved embeds and screens text
// before anything can be paid for or published.
package safety

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Link is a normalized, safe outbound link.
type Link struct {
	URL           string
	Host          string
	EmbedProvider string // "youtube", "instagram" or ""
	EmbedRef      string // video id or post path
}

var (
	ErrBadURL      = errors.New("enter a full https:// link")
	ErrBlockedHost = errors.New("this kind of link is not allowed")
	ErrShortener   = errors.New("link shorteners are not allowed; paste the final destination link")
)

var shorteners = map[string]bool{
	"bit.ly": true, "t.co": true, "tinyurl.com": true, "goo.gl": true, "ow.ly": true,
	"is.gd": true, "buff.ly": true, "rebrand.ly": true, "cutt.ly": true, "shorturl.at": true,
	"t.ly": true, "rb.gy": true, "tiny.cc": true, "lnkd.in": true, "s.id": true, "bl.ink": true,
	"linktr.ee": false, // allowed: a landing page, not a redirector
}

// Hosts that are invites, payment requests or file drops: easy vehicles for
// scams, and impossible to review in advance.
var blockedHosts = []string{
	"discord.gg", "discord.com/invite", "t.me", "telegram.me", "chat.whatsapp.com", "wa.me",
	"paypal.me", "cash.app", "venmo.com", "mega.nz", "wetransfer.com", "anonfiles.com",
}

var blockedExt = regexp.MustCompile(`(?i)\.(exe|msi|apk|dmg|pkg|bat|cmd|scr|jar|zip|rar|7z|iso)$`)

// Tracking parameters stripped from every link, so placement cannot be used
// to launder affiliate attribution and so our own utm tag is the only one.
var trackingParams = regexp.MustCompile(`(?i)^(utm_.*|fbclid|gclid|dclid|msclkid|mc_eid|igshid|si|ref|ref_src|aff|affiliate|via)$`)

var ytID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var igPath = regexp.MustCompile(`^/(p|reel|tv)/([A-Za-z0-9_-]{5,40})/?$`)

// CheckLink normalizes raw input and rejects anything unsafe.
func CheckLink(raw string) (Link, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 500 {
		return Link{}, ErrBadURL
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Link{}, ErrBadURL
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Link{}, ErrBadURL
	}
	u.Scheme = "https"
	if u.User != nil {
		return Link{}, ErrBlockedHost
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" && u.Port() != "443" {
		return Link{}, ErrBlockedHost
	}
	if net.ParseIP(host) != nil || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || host == "localhost" || strings.HasPrefix(host, "xn--") && strings.Count(host, ".") == 1 {
		return Link{}, ErrBlockedHost
	}
	bare := strings.TrimPrefix(host, "www.")
	if shorteners[bare] {
		return Link{}, ErrShortener
	}
	hostPath := bare + strings.TrimRight(u.EscapedPath(), "/")
	for _, b := range blockedHosts {
		if bare == b || strings.HasPrefix(hostPath, b+"/") || hostPath == b {
			return Link{}, ErrBlockedHost
		}
	}
	if blockedExt.MatchString(u.Path) {
		return Link{}, ErrBlockedHost
	}
	q := u.Query()
	for k := range q {
		if trackingParams.MatchString(k) {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment = ""
	u.Host = host

	l := Link{URL: u.String(), Host: bare}
	detectEmbed(u, &l)
	return l, nil
}

func detectEmbed(u *url.URL, l *Link) {
	host := l.Host
	switch {
	case host == "youtube.com" || host == "m.youtube.com" || host == "music.youtube.com":
		if u.Path == "/watch" {
			if id := u.Query().Get("v"); ytID.MatchString(id) {
				l.EmbedProvider, l.EmbedRef = "youtube", id
			}
		} else if strings.HasPrefix(u.Path, "/shorts/") || strings.HasPrefix(u.Path, "/live/") || strings.HasPrefix(u.Path, "/embed/") {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 && ytID.MatchString(parts[1]) {
				l.EmbedProvider, l.EmbedRef = "youtube", parts[1]
			}
		}
	case host == "youtu.be":
		id := strings.Trim(u.Path, "/")
		if ytID.MatchString(id) {
			l.EmbedProvider, l.EmbedRef = "youtube", id
		}
	case host == "instagram.com":
		if m := igPath.FindStringSubmatch(u.Path); m != nil {
			l.EmbedProvider, l.EmbedRef = "instagram", m[1]+"/"+m[2]
		}
	}
}

// A deliberately short list. Anything matching goes to manual review rather
// than being rejected, so the owner decides.
var reviewWords = regexp.MustCompile(`(?i)\b(nigg\w*|fag\w*|kike|retard\w*|rape\w*|kill (yourself|urself)|kys|porn\w*|xxx|nsfw|onlyfans|casino|betting|crypto ?giveaway|airdrop|guaranteed (profit|returns)|double your)\b`)

// Text cleans a one-line or short text field.
func Text(s string, max int) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == '‮' || r == '‭' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		return "", errors.New("too long")
	}
	return s, nil
}

// NeedsReview reports why text should be held for a human, or "".
func NeedsReview(fields ...string) string {
	for _, f := range fields {
		if reviewWords.MatchString(f) {
			return "contains a word on the review list"
		}
		upper, letters := 0, 0
		for _, r := range f {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
		if letters > 20 && upper*10 > letters*8 {
			return "mostly capital letters"
		}
		if strings.Count(f, "http") > 1 {
			return "contains several links"
		}
	}
	return ""
}

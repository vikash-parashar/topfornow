package safety

import "testing"

func TestCheckLink(t *testing.T) {
	cases := []struct {
		in, url, embed, ref string
		bad                 bool
	}{
		{in: "https://youtu.be/dQw4w9WgXcQ?si=abc", url: "https://youtu.be/dQw4w9WgXcQ", embed: "youtube", ref: "dQw4w9WgXcQ"},
		{in: "www.youtube.com/watch?v=dQw4w9WgXcQ&utm_source=x", url: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", embed: "youtube", ref: "dQw4w9WgXcQ"},
		{in: "https://www.youtube.com/shorts/dQw4w9WgXcQ", embed: "youtube", ref: "dQw4w9WgXcQ", url: "https://www.youtube.com/shorts/dQw4w9WgXcQ"},
		{in: "https://www.instagram.com/reel/C9abcDEF12/?igshid=zz", url: "https://www.instagram.com/reel/C9abcDEF12/", embed: "instagram", ref: "reel/C9abcDEF12"},
		{in: "https://example.com/pricing?gclid=1&plan=pro#top", url: "https://example.com/pricing?plan=pro"},
		{in: "http://Example.com", url: "https://example.com"},
		{in: "https://bit.ly/abc", bad: true},
		{in: "https://discord.gg/abc", bad: true},
		{in: "https://127.0.0.1/admin", bad: true},
		{in: "https://localhost/x", bad: true},
		{in: "javascript:alert(1)", bad: true},
		{in: "ftp://example.com", bad: true},
		{in: "https://user:pw@example.com", bad: true},
		{in: "https://example.com:8080/", bad: true},
		{in: "https://example.com/setup.exe", bad: true},
		{in: "https://t.me/channel", bad: true},
	}
	for _, c := range cases {
		l, err := CheckLink(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%s: expected rejection, got %+v", c.in, l)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.in, err)
			continue
		}
		if l.URL != c.url || l.EmbedProvider != c.embed || l.EmbedRef != c.ref {
			t.Errorf("%s: got %+v", c.in, l)
		}
	}
}

func TestReview(t *testing.T) {
	if NeedsReview("Pineapple belongs on pizza") != "" {
		t.Fatal("normal opinion flagged")
	}
	if NeedsReview("Visit our casino now") == "" {
		t.Fatal("casino not flagged")
	}
	if NeedsReview("THIS IS THE BEST PRODUCT EVER MADE BUY IT") == "" {
		t.Fatal("shouting not flagged")
	}
	if s, _ := Text("  hello\n\tworld ‮ ", 50); s != "hello world" {
		t.Fatalf("got %q", s)
	}
}

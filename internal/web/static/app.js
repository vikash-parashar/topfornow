// TopForNow: progressive enhancement only. Every action works without JS.
(function () {
  "use strict";
  var csrf = (document.querySelector('meta[name="csrf"]') || {}).content || "";

  function beacon(type, slug) {
    var body = new URLSearchParams({ type: type, slug: slug || "", csrf: csrf });
    if (navigator.sendBeacon) navigator.sendBeacon("/beacon", body);
    else fetch("/beacon", { method: "POST", body: body, keepalive: true });
  }
  function toast(msg) {
    var t = document.createElement("div");
    t.className = "toast"; t.textContent = msg; t.setAttribute("role", "status");
    document.body.appendChild(t);
    setTimeout(function () { t.remove(); }, 2200);
  }
  function fmt(sec) {
    if (sec < 60) return Math.floor(sec) + "s";
    if (sec < 3600) return Math.floor(sec / 60) + "m";
    if (sec < 172800) return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
    return Math.floor(sec / 86400) + "d " + Math.floor((sec % 86400) / 3600) + "h";
  }

  // Live "time on top" and countdowns.
  function tick() {
    var now = Date.now();
    document.querySelectorAll("[data-since]").forEach(function (el) {
      el.textContent = fmt((now - Date.parse(el.dataset.since)) / 1000);
    });
    document.querySelectorAll("time[data-until]").forEach(function (el) {
      var s = Math.max(0, (Date.parse(el.dataset.until) - now) / 1000);
      el.textContent = s > 0 ? "in " + Math.floor(s / 60) + ":" + String(Math.floor(s % 60)).padStart(2, "0") : "now (refresh)";
    });
  }
  tick(); setInterval(tick, 1000);

  // Votes without page reload.
  document.addEventListener("click", function (e) {
    var b = e.target.closest(".vote");
    if (!b) return;
    var form = b.closest("form");
    e.preventDefault();
    var box = b.closest(".votes");
    var mine = parseInt(box.dataset.mine || "0", 10);
    var v = parseInt(b.dataset.v, 10);
    var send = mine === v ? 0 : v;
    var body = new URLSearchParams({ csrf: csrf, slug: box.dataset.slug, value: String(send) });
    fetch(form.action, { method: "POST", body: body, headers: { Accept: "application/json" } })
      .then(function (r) { if (!r.ok) return r.text().then(function (t) { throw new Error(t); }); return r.json(); })
      .then(function (j) {
        box.dataset.mine = j.mine;
        box.querySelector(".up .n").textContent = j.up;
        box.querySelector(".down .n").textContent = j.down;
        box.querySelector(".up").classList.toggle("on", j.mine === 1);
        box.querySelector(".down").classList.toggle("on", j.mine === -1);
        var bar = box.querySelector(".bar");
        if (bar) bar.value = j.up + j.down ? Math.round((j.up * 100) / (j.up + j.down)) : 0;
      })
      .catch(function (err) { toast(err.message || "Could not vote"); });
  });

  // Share, copy, share-with-image.
  function copy(url, slug) {
    (navigator.clipboard ? navigator.clipboard.writeText(url) : Promise.reject())
      .then(function () { toast("Link copied"); beacon("share", slug); })
      .catch(function () { window.prompt("Copy this link", url); });
  }
  document.addEventListener("click", function (e) {
    var s = e.target.closest(".share");
    var c = e.target.closest(".copy");
    var r = e.target.closest(".share-receipt");
    if (c) { copy(c.dataset.url, c.dataset.slug); return; }
    if (s) {
      if (navigator.share) {
        navigator.share({ title: "TopForNow", text: s.dataset.text, url: s.dataset.url })
          .then(function () { beacon("share", s.dataset.slug); }).catch(function () {});
      } else copy(s.dataset.url, s.dataset.slug);
      return;
    }
    if (r) {
      var data = { title: "My Ego Receipt", text: r.dataset.text, url: r.dataset.url };
      fetch(r.dataset.img).then(function (res) { return res.blob(); }).then(function (blob) {
        var file = new File([blob], "ego-receipt.png", { type: "image/png" });
        if (navigator.canShare && navigator.canShare({ files: [file] })) data.files = [file];
        if (navigator.share) return navigator.share(data).then(function () { beacon("share", r.dataset.slug); });
        copy(r.dataset.url + "", r.dataset.slug);
      }).catch(function () {});
    }
  });

  // YouTube: load the player only when tapped, and count a play only when
  // the player itself reports PLAYING.
  var ytReady = null;
  function loadYT() {
    if (ytReady) return ytReady;
    ytReady = new Promise(function (resolve, reject) {
      window.onYouTubeIframeAPIReady = function () { resolve(window.YT); };
      var s = document.createElement("script");
      s.src = "https://www.youtube.com/iframe_api";
      s.onerror = reject;
      document.head.appendChild(s);
      setTimeout(function () { reject(new Error("timeout")); }, 8000);
    });
    return ytReady;
  }
  document.addEventListener("click", function (e) {
    var btn = e.target.closest(".yt-load");
    if (!btn) return;
    var box = btn.closest(".embed-yt");
    var id = box.dataset.yt, slug = box.dataset.slug, counted = false;
    loadYT().then(function (YT) {
      var holder = document.createElement("div");
      box.innerHTML = ""; box.appendChild(holder);
      new YT.Player(holder, {
        videoId: id, host: "https://www.youtube-nocookie.com", width: "100%", height: "100%",
        playerVars: { autoplay: 1, playsinline: 1, rel: 0 },
        events: {
          onStateChange: function (ev) { if (ev.data === 1 && !counted) { counted = true; beacon("video_play", slug); } },
          onError: function () { box.innerHTML = '<p class="embed-fallback">This video can\'t be embedded. Use the link below.</p>'; }
        }
      });
    }).catch(function () {
      var f = document.createElement("iframe");
      f.src = "https://www.youtube-nocookie.com/embed/" + id + "?autoplay=1&playsinline=1";
      f.allow = "autoplay; encrypted-media; picture-in-picture"; f.allowFullscreen = true; f.title = "YouTube video";
      box.innerHTML = ""; box.appendChild(f);
    });
  });

  // Checkout return page: poll until the payment is confirmed.
  var ret = document.getElementById("return");
  if (ret) {
    var msg = ret.querySelector(".status-msg"), tries = 0;
    (function poll() {
      fetch("/api/hold/" + ret.dataset.hold, { cache: "no-store" }).then(function (r) { return r.json(); }).then(function (j) {
        msg.textContent = j.message;
        if (j.redirect) { location.href = j.redirect; return; }
        if (j.state === "refunding" || j.state === "closed" || j.state === "failed") { ret.querySelector(".spinner").remove(); return; }
        tries++; setTimeout(poll, tries < 20 ? 1500 : 5000);
      }).catch(function () { setTimeout(poll, 4000); });
    })();
  }

  // Link hint on the submit form.
  var kinds = document.querySelectorAll('input[name="kind"]');
  var hint = document.querySelector(".link-hint");
  function updHint() {
    var k = (document.querySelector('input[name="kind"]:checked') || {}).value;
    if (!hint) return;
    hint.textContent = k === "creator" ? "(YouTube video/Short or public Instagram post/Reel)" : k === "product" ? "(your app, website or demo)" : "(optional)";
  }
  kinds.forEach(function (k) { k.addEventListener("change", updHint); }); updHint();
})();

package web

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vikash-parashar/topfornow/internal/core"
	"github.com/vikash-parashar/topfornow/internal/pay"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/login", s.adminLoginForm)
	mux.HandleFunc("POST /admin/login", s.limit(5, s.adminLogin))
	mux.HandleFunc("POST /admin/logout", s.adminOnly(s.adminLogout))
	mux.HandleFunc("GET /admin", s.adminOnly(s.adminDash))
	mux.HandleFunc("POST /admin/posts/{id}/{action}", s.adminOnly(s.adminPostAction))
	mux.HandleFunc("POST /admin/payments/{id}/refund", s.adminOnly(s.adminRefund))
	mux.HandleFunc("POST /admin/reconcile/fetch", s.adminOnly(s.adminFetchPayment))
	mux.HandleFunc("POST /admin/refunds/run", s.adminOnly(s.adminRunRefunds))
	mux.HandleFunc("POST /admin/test-data/purge", s.adminOnly(s.adminPurge))
}

const adminCookie = "tfn_admin"

func (s *Server) isAdmin(r *http.Request) bool {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.adminSig(parts[0]))) == 1
}

// adminSig binds the session to the expiry, the username and the password, so
// changing ADMIN_PASSWORD or ADMIN_USERNAME signs every owner out.
func (s *Server) adminSig(exp string) string {
	return s.sign("admin:" + exp + ":" + s.adminUser() + ":" + s.AdminPassword)
}

func (s *Server) adminUser() string {
	if s.AdminUser == "" {
		return "owner"
	}
	return s.AdminUser
}

func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		if s.AdminPassword == "" || !s.isAdmin(r) {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method == http.MethodPost && !s.checkCSRF(r) {
			http.Error(w, "bad csrf", 400)
			return
		}
		h(w, r)
	}
}

func (s *Server) adminLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "admin_login", 200, Page{Title: "Owner sign in · TopForNow", NoIndex: true})
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	userOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(r.FormValue("username"))), []byte(s.adminUser())) == 1
	passOK := subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(s.AdminPassword)) == 1
	if !s.checkCSRF(r) || s.AdminPassword == "" || !userOK || !passOK {
		time.Sleep(700 * time.Millisecond)
		s.render(w, r, "admin_login", 401, Page{Title: "Owner sign in · TopForNow", NoIndex: true, Data: "Wrong username or password."})
		return
	}
	exp := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: exp + "." + s.adminSig(exp), Path: "/admin",
		HttpOnly: true, Secure: strings.HasPrefix(s.BaseURL, "https://"), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: "/admin", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type adminData struct {
	State    *core.State
	Review   []*core.Post
	Reports  []core.ReportRow
	Payments []core.PaymentRow
	Recon    *core.Reconciliation
	Day      *core.Traffic
	Week     *core.Traffic
	Funnel   []core.FunnelRow
	Recent   []*core.Post
	Money    *core.Revenue
	Flash    string
}

func (s *Server) adminDash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := adminData{Flash: r.URL.Query().Get("flash")}
	d.State, _ = s.Svc.GetState(ctx)
	d.Review, _ = s.Svc.ListPosts(ctx, "needs_review", 50)
	d.Reports, _ = s.Svc.OpenReports(ctx)
	d.Payments, _ = s.Svc.Payments(ctx, 50)
	d.Recon, _ = s.Svc.Reconcile(ctx)
	d.Day, _ = s.Svc.TrafficSince(ctx, time.Now().Add(-24*time.Hour))
	d.Week, _ = s.Svc.TrafficSince(ctx, time.Now().Add(-7*24*time.Hour))
	d.Funnel, _ = s.Svc.Funnel(ctx, time.Now().Add(-30*24*time.Hour))
	d.Recent, _ = s.Svc.ListPosts(ctx, "", 30)
	var err error
	if d.Money, err = s.Svc.RevenueReport(ctx, s.OwnerTZ, !s.IsLive()); err != nil {
		s.Log.Error("revenue report", "err", err)
	}
	s.render(w, r, "admin", 200, Page{Title: "Owner · TopForNow", NoIndex: true, Data: d})
}

func back(w http.ResponseWriter, r *http.Request, flash string) {
	http.Redirect(w, r, "/admin?flash="+strings.ReplaceAll(flash, " ", "+"), http.StatusSeeOther)
}

func (s *Server) adminPostAction(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var err error
	switch r.PathValue("action") {
	case "approve":
		err = s.Svc.Approve(r.Context(), id)
	case "reject":
		err = s.Svc.Reject(r.Context(), id, r.FormValue("note"))
	case "remove":
		err = s.Svc.Remove(r.Context(), id, firstNonEmpty(r.FormValue("reason"), "broke the rules"))
	case "dismiss":
		err = s.Svc.DismissReports(r.Context(), id)
	default:
		http.Error(w, "unknown action", 400)
		return
	}
	if err != nil {
		back(w, r, "Error: "+err.Error())
		return
	}
	back(w, r, "Done: "+r.PathValue("action")+" post "+r.PathValue("id"))
}

func (s *Server) adminRefund(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Svc.AdminRefund(r.Context(), id); err != nil {
		back(w, r, "Error: "+err.Error())
		return
	}
	back(w, r, "Refund queued for payment row "+r.PathValue("id"))
}

// adminFetchPayment pulls a payment from the provider by id and applies it:
// the recovery path for a webhook that never arrived.
func (s *Server) adminFetchPayment(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimSpace(r.FormValue("payment_id"))
	if pid == "" || s.Fetcher == nil {
		back(w, r, "Enter a payment id")
		return
	}
	p, err := s.Fetcher.GetPayment(r.Context(), pid)
	if err != nil {
		back(w, r, "Provider error: "+err.Error())
		return
	}
	typ := pay.StatusToType(p.Status)
	if typ == "" {
		back(w, r, "Payment status is "+p.Status+"; nothing to apply")
		return
	}
	res, err := s.Svc.ApplyPaymentEvent(r.Context(), pay.ToEvent("admin-fetch:"+pid+":"+p.Status, typ, p, nil))
	if err != nil {
		back(w, r, "Apply error: "+err.Error())
		return
	}
	back(w, r, fmt.Sprintf("Applied %s: %s", typ, res.Outcome))
}

func (s *Server) adminRunRefunds(w http.ResponseWriter, r *http.Request) {
	n, err := s.Svc.ProcessRefunds(r.Context())
	if err != nil {
		back(w, r, "Error: "+err.Error())
		return
	}
	back(w, r, fmt.Sprintf("Requested %d refunds", n))
}

func (s *Server) adminPurge(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("confirm") != "PURGE" {
		back(w, r, "Type PURGE to confirm")
		return
	}
	out, err := s.Svc.PurgeTestData(r.Context())
	if err != nil {
		back(w, r, "Error: "+err.Error())
		return
	}
	back(w, r, fmt.Sprintf("Purged test data: %v", out))
}

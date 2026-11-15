package httpServer

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2/lang"
	"github.com/owlcms/video/internal/access"
	"github.com/owlcms/video/internal/config/replays"
	"github.com/owlcms/video/internal/logging"
)

var replayAccess *access.Service

// referrerPolicy keeps referrers within the Video origin. "no-referrer" would
// also make browsers send "Origin: null" on form posts, which the same-origin
// check of sign-in and sign-out then refuses.
const referrerPolicy = "same-origin"

func refuseForeignOrigin(w http.ResponseWriter, r *http.Request) {
	addr, _ := clientOf(r)
	logging.WarningLogger.Printf("Replays %s refused from %s: Origin %q does not match host %q",
		r.URL.EscapedPath(), addr, r.Header.Get("Origin"), r.Host)
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func SetAccess(service *access.Service) {
	replayAccess = service
}

type viewerContextKey struct{}

func setPageAccess(data *TemplateData, r *http.Request) {
	if viewer, ok := r.Context().Value(viewerContextKey{}).(access.Viewer); ok && viewer.ID != "" {
		data.Protected = true
		data.AccessName = viewer.Name
		data.AccessExpires = viewer.ExpiresAt.Local().Format(time.DateTime)
		data.AccessMessage = fmt.Sprintf(lang.Localize("Signed in as %s, valid until %s"), data.AccessName, data.AccessExpires)
		data.SignOutLabel = lang.Localize("Sign out")
	}
}

type failureEntry struct {
	start, last, blockedUntil time.Time
	count                     int
}

type refusalEntry struct {
	last    time.Time
	repeats int
}

var failures = struct {
	sync.Mutex
	entries map[string]failureEntry
	logged  map[string]refusalEntry
}{entries: make(map[string]failureEntry), logged: make(map[string]refusalEntry)}

// logRefusal reports why a replay request was refused, never the token. The same reason from the same address is
// logged at most once a minute; the repeats are counted on the next line, so a client retrying every second does not
// flood the log.
func logRefusal(addr, route, reason string) {
	failures.Lock()
	defer failures.Unlock()
	now := time.Now()
	key := addr + "\x00" + reason
	entry, exists := failures.logged[key]
	if exists && now.Sub(entry.last) < time.Minute {
		entry.repeats++
		failures.logged[key] = entry
		return
	}
	if !exists && len(failures.logged) >= 1024 {
		for k, v := range failures.logged {
			if now.Sub(v.last) >= time.Minute {
				delete(failures.logged, k)
			}
		}
	}
	repeats := ""
	if entry.repeats > 0 {
		repeats = fmt.Sprintf(" (and %d more since %s)", entry.repeats, entry.last.Format(time.TimeOnly))
	}
	logging.WarningLogger.Printf("Replays access refused from %s on %s: %s%s", addr, route, reason, repeats)
	if len(failures.logged) < 1024 {
		failures.logged[key] = refusalEntry{last: now}
	}
}

// refusalReason describes a refused credential for the log.
func refusalReason(decision access.Decision, explicit, validSyntax bool) string {
	switch {
	case decision == access.ServiceUnavailable:
		return "the access token file is unreadable"
	case explicit && !validSyntax:
		return "malformed credentials: several tokens, or an Authorization header that is not one Bearer token"
	case explicit && decision == access.ExpiredToken:
		return "token expired"
	case explicit:
		return "token not recognized: wrong, regenerated or revoked"
	case decision == access.Missing:
		return "no token"
	case decision == access.ExpiredToken:
		return "session expired with its token"
	default:
		return "session cookie not valid: token regenerated or revoked"
	}
}

func throttled(addr string, failed bool) bool {
	failures.Lock()
	defer failures.Unlock()
	now := time.Now()
	for k, v := range failures.entries {
		if now.Sub(v.last) > 2*time.Minute && now.After(v.blockedUntil) {
			delete(failures.entries, k)
		}
	}
	entry, exists := failures.entries[addr]
	if !exists && len(failures.entries) >= 1024 {
		// Avoid unbounded memory while retaining existing blocks.
		return failed
	}
	if now.Before(entry.blockedUntil) {
		return true
	}
	if !failed {
		return false
	}
	if entry.start.IsZero() || now.Sub(entry.start) >= time.Minute {
		entry.start, entry.count = now, 0
	}
	entry.count++
	entry.last = now
	if entry.count >= 20 {
		entry.blockedUntil = now.Add(time.Minute)
	}
	failures.entries[addr] = entry
	return now.Before(entry.blockedUntil)
}

func sendThrottle(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	http.Error(w, "Too many failed attempts", http.StatusTooManyRequests)
}

func credentials(r *http.Request) (string, bool, bool) {
	query := r.URL.Query()["token"]
	headers := r.Header.Values("Authorization")
	if len(query) > 1 || len(headers) > 1 {
		return "", true, false
	}
	var value string
	if len(headers) == 1 {
		parts := strings.Fields(headers[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return "", true, false
		}
		value = parts[1]
	}
	if len(query) == 1 {
		if len(headers) == 1 && value != query[0] {
			return "", true, false
		}
		value = query[0]
	}
	return value, len(headers) != 0 || len(query) != 0, true
}

func guardReplay(mode string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := access.StateOpen
		if replayAccess != nil {
			state, _ = replayAccess.ViewerState()
		}
		if state == access.StateOpen {
			if mode == "page" && r.Method == http.MethodGet && len(r.URL.Query()["token"]) > 0 {
				w.Header().Set("Cache-Control", "private, no-store")
				w.Header().Set("Referrer-Policy", referrerPolicy)
				http.Redirect(w, r, withoutToken(r.URL), http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		if mode == "page" {
			w.Header().Set("Referrer-Policy", referrerPolicy)
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		route := r.URL.EscapedPath()
		addr, _ := clientOf(r)
		if state == access.StateUnavailable {
			logRefusal(addr, route, refusalReason(access.ServiceUnavailable, false, true))
			http.Error(w, "Replays access configuration is unreadable", http.StatusServiceUnavailable)
			return
		}
		token, explicit, validSyntax := credentials(r)
		if explicit && throttled(addr, false) {
			logRefusal(addr, route, "blocked for a minute after too many failed attempts")
			sendThrottle(w)
			return
		}
		var viewer access.Viewer
		decision := access.Invalid
		if explicit && validSyntax {
			viewer, decision = replayAccess.VerifyViewer(token)
		} else if !explicit {
			cookie, err := r.Cookie(replayAccess.CookieName())
			if err == nil {
				viewer, decision = replayAccess.VerifySession(cookie.Value)
			} else {
				decision = access.Missing
			}
		}
		reason := refusalReason(decision, explicit, validSyntax)
		if decision == access.Allowed && mode == "ws" && !explicit && !sameOrigin(r, true) {
			decision = access.Invalid
			reason = fmt.Sprintf("session cookie sent from another site (Origin %q)", r.Header.Get("Origin"))
		}
		if decision != access.Allowed {
			logRefusal(addr, route, reason)
			if decision == access.ServiceUnavailable {
				http.Error(w, "Replays access configuration is unreadable", http.StatusServiceUnavailable)
				return
			}
			if explicit && throttled(addr, true) {
				sendThrottle(w)
				return
			}
			if mode == "page" {
				showTokenPage(w, r, false, withoutToken(r.URL))
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("WWW-Authenticate", `Bearer realm="owlcms-replays"`)
				http.Error(w, "Replays token missing, wrong or expired", http.StatusUnauthorized)
			}
			return
		}
		if mode == "page" && explicit && r.Method == http.MethodGet {
			if !issueCookie(w, r, viewer) {
				http.Error(w, "Unable to start session", http.StatusUnauthorized)
				return
			}
			logging.InfoLogger.Printf("Replays access: %q signed in from %s", viewer.Name, addr)
			http.Redirect(w, r, withoutToken(r.URL), http.StatusSeeOther)
			return
		}
		if mode == "ws" {
			logging.InfoLogger.Printf("Replays access: %q connected from %s", viewer.Name, addr)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerContextKey{}, viewer)))
	})
}

func withoutToken(u *url.URL) string {
	copyURL := *u
	q := copyURL.Query()
	q.Del("token")
	copyURL.RawQuery = q.Encode()
	return copyURL.RequestURI()
}

func safeNext(value string) string {
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return "/"
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return "/"
	}
	return withoutToken(u)
}

func sameOrigin(r *http.Request, required bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !required
	}
	u, err := url.Parse(origin)
	return err == nil && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" &&
		(u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host)
}

func issueCookie(w http.ResponseWriter, r *http.Request, viewer access.Viewer) bool {
	value, expiry, err := replayAccess.IssueSession(viewer.ID)
	if err != nil {
		return false
	}
	_, https := clientOf(r)
	http.SetCookie(w, &http.Cookie{Name: replayAccess.CookieName(), Value: value, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: https, Expires: expiry, MaxAge: int(time.Until(expiry).Seconds())})
	return true
}

func showTokenPage(w http.ResponseWriter, r *http.Request, failed bool, next string) {
	platform := ""
	if cfg := replays.GetCurrentConfig(); cfg != nil {
		platform = cfg.Platform
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", referrerPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	if err := templates.ExecuteTemplate(w, "token.html", struct {
		Platform   string
		Next       string
		Error      bool
		TokenLabel string
		SignIn     string
		ErrorText  string
	}{platform, safeNext(next), failed, lang.Localize("Access token"), lang.Localize("Sign in"),
		lang.Localize("Token not accepted. It may be wrong or expired.")}); err != nil {
		logging.ErrorLogger.Printf("Render token page: %v", err)
	}
}

func handleTokenSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", referrerPolicy)
	if !sameOrigin(r, false) {
		refuseForeignOrigin(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	next := safeNext(r.PostForm.Get("next"))
	if replayAccess == nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	state, _ := replayAccess.ViewerState()
	addr, _ := clientOf(r)
	route := r.URL.EscapedPath()
	if state == access.StateUnavailable {
		logRefusal(addr, route, refusalReason(access.ServiceUnavailable, true, true))
		http.Error(w, "Replays access configuration is unreadable", http.StatusServiceUnavailable)
		return
	}
	if state == access.StateOpen {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	if throttled(addr, false) {
		logRefusal(addr, route, "blocked for a minute after too many failed attempts")
		sendThrottle(w)
		return
	}
	values := r.PostForm["token"]
	var viewer access.Viewer
	decision := access.Invalid
	if len(values) == 1 {
		viewer, decision = replayAccess.VerifyViewer(values[0])
	}
	if decision != access.Allowed {
		logRefusal(addr, route, refusalReason(decision, true, len(values) == 1))
		if decision == access.ServiceUnavailable {
			http.Error(w, "Replays access configuration is unreadable", http.StatusServiceUnavailable)
		} else if throttled(addr, true) {
			sendThrottle(w)
		} else {
			showTokenPage(w, r, true, next)
		}
		return
	}
	if !issueCookie(w, r, viewer) {
		http.Error(w, "Unable to start session", http.StatusUnauthorized)
		return
	}
	logging.InfoLogger.Printf("Replays access: %q signed in from %s", viewer.Name, addr)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func handleSignOut(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, false) {
		refuseForeignOrigin(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if replayAccess != nil && replayAccess.CookieName() != "" {
		_, https := clientOf(r)
		http.SetCookie(w, &http.Cookie{Name: replayAccess.CookieName(), Path: "/", HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Secure: https, MaxAge: -1})
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

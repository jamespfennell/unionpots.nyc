// Package web serves the log's HTML interface.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"unionpots.nyc/log/internal/backup"
	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
	"unionpots.nyc/log/internal/photos"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	Store  *db.Store
	Auth   *Auth
	Backup func() backup.Status
	// Snapshots lists the stored backups (nil when backups are off).
	Snapshots func(context.Context) ([]backup.Snapshot, error)
	// LoginMessage, if set, is shown on the login page (the demo's password).
	LoginMessage string
	// DefaultPassword means no password was configured, so it's the
	// default one; a banner says so (to a logged-in user).
	DefaultPassword bool
	// Demo turns off photo uploads and shows a demo banner instead of the
	// backup ones.
	Demo bool
	// Photos stores photo files; PhotoBackup (nil when backups are off)
	// copies them to backup storage.
	Photos      *photos.Store
	PhotoBackup PhotoBackup
	// Version describes the running build ("b8e67d8 · built 4 Oct 2026"),
	// shown at the bottom of the menu.
	Version string
	Now     func() time.Time
	Log     *slog.Logger

	pages map[string]*template.Template
}

// Handler builds the routes. It panics if templates fail to parse.
func (s *Server) Handler() http.Handler {
	s.pages = mustParsePages()

	private := http.NewServeMux()
	private.Handle("GET /{$}", s.handle(s.home))
	private.Handle("GET /pieces/{id}", s.handle(s.piece))
	private.Handle("GET /pieces/{id}/edit", s.handle(s.pieceEdit))
	private.Handle("POST /pieces/{id}", s.handle(s.updatePiece))
	private.Handle("POST /pieces/{id}/events", s.handle(s.addEvent))
	private.Handle("POST /pieces/{id}/events/{eid}/delete", s.handle(s.deleteEvent))
	private.Handle("POST /pieces/{id}/measurements", s.handle(s.updateMeasurements))
	private.Handle("POST /pieces/{id}/ratings", s.handle(s.updateRatings))
	private.Handle("POST /pieces/{id}/move", s.handle(s.movePiece))
	private.Handle("POST /pieces/{id}/delete", s.handle(s.deletePiece))
	private.Handle("GET /projects/{id}", s.handle(s.project))
	private.Handle("POST /projects/{id}", s.handle(s.updateProject))
	private.Handle("POST /projects/{id}/pieces", s.handle(s.addToProject))
	private.Handle("GET /clays", s.handle(s.clays))
	private.Handle("POST /clays", s.handle(s.addClay))
	private.Handle("GET /clays/{id}", s.handle(s.clay))
	private.Handle("POST /clays/{id}", s.handle(s.updateClay))
	private.Handle("POST /clays/{id}/delete", s.handle(s.deleteClay))
	private.Handle("GET /glazes", s.handle(s.glazes))
	private.Handle("POST /glazes", s.handle(s.addGlaze))
	private.Handle("GET /glazes/{id}", s.handle(s.glaze))
	private.Handle("POST /glazes/{id}", s.handle(s.renameGlaze))
	private.Handle("POST /glazes/{id}/delete", s.handle(s.deleteGlaze))
	private.Handle("POST /pieces/{id}/photos", s.handle(s.addPhotos))
	private.Handle("POST /pieces/{id}/photos/{pid}/delete", s.handle(s.deletePhoto))
	private.HandleFunc("GET /photos/{name}", s.photoFile)
	private.Handle("GET /backups", s.handle(s.backups))
	private.Handle("GET /new", s.handle(s.newForm))
	private.Handle("POST /new", s.handle(s.create))

	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /login", s.handle(s.loginForm))
	mux.Handle("POST /login", s.handle(s.login))
	mux.Handle("POST /logout", s.handle(s.logout))
	mux.Handle("POST /logout-everywhere", s.Auth.Require(s.handle(s.logoutEverywhere)))
	mux.Handle("/", s.Auth.Require(private))

	// Rejects cross-site non-GET requests (CSRF) using Sec-Fetch-Site/Origin.
	return http.NewCrossOriginProtection().Handler(securityHeaders(mux))
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A boosted form POST is answered with a redirect that the browser
		// follows; tell htmx the final URL so history and reloads are right.
		if r.Method == http.MethodGet && r.Header.Get("HX-Boosted") == "true" {
			w.Header().Set("HX-Push-Url", r.URL.RequestURI())
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}

// handlerFunc returns an error instead of writing one; handle() renders it.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) handle(f handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err == nil {
			return
		}
		var ue *db.UserError
		if r.Header.Get("X-Photos") != "" { // the photo uploader shows plain messages
			if errors.As(err, &ue) {
				http.Error(w, ue.Msg, http.StatusBadRequest)
			} else if errors.Is(err, db.ErrNotFound) {
				http.Error(w, "Not found.", http.StatusNotFound)
			} else {
				s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
			return
		}
		switch {
		case errors.As(err, &ue):
			s.renderError(w, r, http.StatusBadRequest, ue.Msg)
		case errors.Is(err, db.ErrNotFound):
			s.renderError(w, r, http.StatusNotFound, "Not found.")
		default:
			s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
			s.renderError(w, r, http.StatusInternalServerError, "Something went wrong.")
		}
	})
}

// --- templates ---

var funcs = template.FuncMap{
	"static":  staticURL,
	"lower":   strings.ToLower,
	"daysAgo": daysAgo,
	"measure": model.FormatMeasure,
	"dimsNew": func() []dimsRow { return []dimsRow{{Key: "new"}} },
	"hasID":   hasID,
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
	"list":       func(xs ...int) []int { return xs },
	"shortDate":  shortDate,
	"actions":    func() []model.Action { return model.Actions },
	"inProgress": func() []model.State { return model.InProgress },
	"ago":        ago,
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}

// staticVersion is a hash of the embedded static files. It is appended to
// static URLs so browsers fetch new CSS/JS after each deploy despite caching.
var staticVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(p)
			h.Write([]byte(p))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func staticURL(name string) string { return "/static/" + name + "?v=" + staticVersion }

// daysAgo describes a YYYY-MM-DD date relative to today: "today",
// "yesterday", "5 days ago".
func daysAgo(date, today string) string {
	d, err1 := time.Parse(model.DateLayout, date)
	t, err2 := time.Parse(model.DateLayout, today)
	if err1 != nil || err2 != nil {
		return date
	}
	switch n := int(t.Sub(d).Hours() / 24); {
	case n < 0:
		return date
	case n == 0:
		return "today"
	case n == 1:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", n)
	}
}

// shortDate formats a YYYY-MM-DD date as "Sep 20", adding the year when it
// isn't the current one: "Jun 1, 2025".
func shortDate(date, today string) string {
	d, err := time.Parse(model.DateLayout, date)
	if err != nil {
		return date
	}
	if len(today) >= 4 && date[:4] == today[:4] {
		return d.Format("Jan 2")
	}
	return d.Format("Jan 2, 2006")
}

// flashCookie carries what was just done to the next home page view, which
// shows it once (so the address bar stays "/" and a reload doesn't repeat it).
const flashCookie = "log_flash"

// redirectHome sends the browser back to the home page with a one-off notice.
func redirectHome(w http.ResponseWriter, r *http.Request, what url.Values) error {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: what.Encode(), Path: "/", MaxAge: 60,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	return redirect(w, r, "/")
}

// homeNotice describes what was just done: "Created #137 and #138." or
// "Marked #135 and #136 glazed."
func homeNotice(q url.Values) string {
	list := func(v string) (string, int) {
		var ids []string
		for _, s := range strings.Split(v, ",") {
			if _, err := strconv.ParseInt(s, 10, 64); err == nil {
				ids = append(ids, "#"+s)
			}
		}
		switch len(ids) {
		case 0:
			return "", 0
		case 1:
			return ids[0], 1
		}
		return strings.Join(ids[:len(ids)-1], ", ") + " and " + ids[len(ids)-1], len(ids)
	}
	if ids, n := list(q.Get("created")); n > 0 {
		return "Created " + ids + "."
	}
	if ids, n := list(q.Get("marked")); n > 0 {
		if a, err := model.ParseAction(q.Get("action")); err == nil {
			return "Marked " + ids + " " + strings.ToLower(a.Label()) + "."
		}
	}
	return ""
}

func joinIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func mustParsePages() map[string]*template.Template {
	base := template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/_*.html"))
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		panic(err)
	}
	pages := map[string]*template.Template{}
	for _, f := range files {
		name := path.Base(f)
		if strings.HasPrefix(name, "_") {
			continue
		}
		t := template.Must(template.Must(base.Clone()).ParseFS(templateFS, f))
		pages[strings.TrimSuffix(name, ".html")] = t
	}
	return pages
}

// Page is the data common to every page; handlers embed it.
type Page struct {
	Title    string
	Version  string
	LoggedIn bool
	Today    string
	Banners  []banner
	// LogOutEverywhere shows the menu item.
	LogOutEverywhere bool
}

// banner is a thin notice across the top of every page. Warnings are red.
type banner struct {
	Text    string
	Warning bool
}

func (s *Server) page(r *http.Request, title string) Page {
	p := Page{Title: title, Version: s.Version, LoggedIn: s.Auth.valid(r), Today: model.Today(s.Now()),
		LogOutEverywhere: s.canLogOutEverywhere()}
	if s.Demo {
		p.Banners = append(p.Banners, banner{Text: "This is a demo: everything you change is reset every day."})
		return p
	}
	if !p.LoggedIn {
		return p
	}
	if s.DefaultPassword {
		p.Banners = append(p.Banners, banner{Text: "Using the default password: set one with -password-hash", Warning: true})
	}
	st := s.Backup()
	switch {
	case st.Problem != "":
		p.Banners = append(p.Banners, banner{Text: "Backups misconfigured: " + st.Problem, Warning: true})
	case !st.Enabled:
		p.Banners = append(p.Banners, banner{Text: "Backups disabled", Warning: true})
	case st.LastOK.IsZero() && st.LastError != "":
		p.Banners = append(p.Banners, banner{Text: "Backups not working: " + shorten(st.LastError, 120) + " (see Backups)", Warning: true})
	case st.Stale(s.Now()):
		p.Banners = append(p.Banners, banner{Text: "Backups failing: last success " + ago(st.LastOK, s.Now()), Warning: true})
	}
	return p
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) error {
	t, ok := s.pages[name]
	if !ok {
		return fmt.Errorf("no template %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Always fresh: Back would otherwise show a stale copy (e.g. without a
	// photo just added).
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// renderPartial renders one named template from _partials.html on its own.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) error {
	var buf bytes.Buffer
	if err := s.pages["piece"].ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

func userErr(format string, args ...any) error {
	return &db.UserError{Msg: fmt.Sprintf(format, args...)}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	data := struct {
		Page
		Message string
	}{s.page(r, http.StatusText(status)), msg}
	if err := s.render(w, status, "error", data); err != nil {
		http.Error(w, msg, status)
	}
}

func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// --- form helpers ---

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		return 0, db.ErrNotFound
	}
	return id, nil
}

func formInt(r *http.Request, name string, def int64) (int64, error) {
	v := strings.TrimPrefix(strings.TrimSpace(r.FormValue(name)), "#")
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, &db.UserError{Msg: fmt.Sprintf("%q is not a number", v)}
	}
	return n, nil
}

// formEvent reads the action and date fields; the date defaults to today.
func (s *Server) formEvent(r *http.Request) (model.Action, string, error) {
	action, err := model.ParseAction(r.FormValue("action"))
	if err != nil {
		return "", "", &db.UserError{Msg: err.Error()}
	}
	date := r.FormValue("date")
	if date == "" {
		date = model.Today(s.Now())
	}
	if date, err = model.ParseDate(date); err != nil {
		return "", "", &db.UserError{Msg: err.Error()}
	}
	return action, date, nil
}

// isAutosave reports whether a form post came from an htmx autosave, which
// expects 204 rather than a redirect. Without JS the same form falls back to a
// normal post-redirect-get.
func isAutosave(r *http.Request) bool {
	return r.FormValue("autosave") == "1" && r.Header.Get("HX-Request") == "true"
}

func redirect(w http.ResponseWriter, r *http.Request, to string) error {
	http.Redirect(w, r, to, http.StatusSeeOther)
	return nil
}

func pieceURL(id int64) string { return "/pieces/" + strconv.FormatInt(id, 10) }

// --- auth handlers ---

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) error {
	if s.Auth.valid(r) {
		return redirect(w, r, safeNext(r.URL.Query().Get("next")))
	}
	return s.renderLogin(w, r, http.StatusOK, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) error {
	data := struct {
		Page
		Next, Error string
		Message     string
		Notice      string
	}{s.page(r, "Log in"), safeNext(r.FormValue("next")), msg, s.LoginMessage, ""}
	if r.URL.Query().Get("out") == "all" {
		data.Notice = "Logged out on every device."
	}
	return s.render(w, status, "login", data)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	ok, limited := s.Auth.CheckPassword(r, r.FormValue("password"))
	if limited {
		return s.renderLogin(w, r, http.StatusTooManyRequests, "Too many attempts. Wait a minute and try again.")
	}
	if !ok {
		return s.renderLogin(w, r, http.StatusUnauthorized, "Wrong password.")
	}
	s.Auth.SetSession(w, r)
	return redirect(w, r, safeNext(r.FormValue("next")))
}

// backups shows the backup policy, what's stored and how the last runs went.
func (s *Server) backups(w http.ResponseWriter, r *http.Request) error {
	data := struct {
		Page
		Status          backup.Status
		Now             time.Time
		IntervalMinutes int
		RetentionDays   int
		Count           int
		Oldest, Newest  time.Time
		ListError       string
	}{Page: s.page(r, "Backups"), Status: s.Backup(), Now: s.Now(),
		IntervalMinutes: int(backup.Interval.Minutes()), RetentionDays: int(backup.Retention.Hours() / 24)}
	if s.Snapshots != nil {
		snaps, err := s.Snapshots(r.Context())
		if err != nil {
			s.Log.Error("listing backups", "err", err)
			data.ListError = "Couldn’t list the stored backups right now."
		} else if len(snaps) > 0 {
			data.Count, data.Oldest, data.Newest = len(snaps), snaps[0].At, snaps[len(snaps)-1].At
		}
	}
	return s.render(w, http.StatusOK, "backups", data)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	s.Auth.ClearSession(w, r)
	return redirect(w, r, "/login")
}

// logoutEverywhere logs out every device, this one included, by rotating
// the session secret.
func (s *Server) logoutEverywhere(w http.ResponseWriter, r *http.Request) error {
	if !s.canLogOutEverywhere() {
		return db.ErrNotFound
	}
	if err := s.Auth.LogOutEverywhere(); err != nil {
		return err
	}
	s.Log.Info("logged out every device")
	s.Auth.ClearSession(w, r)
	return redirect(w, r, "/login?out=all")
}

// canLogOutEverywhere: not in the demo, where any visitor could do it.
func (s *Server) canLogOutEverywhere() bool { return s.Auth.Rotate != nil && !s.Demo }

// --- pages ---

type stateSection struct {
	State  model.State
	Pieces []db.Piece
}

// home lists all in-progress work, one section per state.
func (s *Server) home(w http.ResponseWriter, r *http.Request) error {
	var sections []stateSection
	for _, st := range model.InProgress {
		pieces, err := s.Store.PiecesInState(r.Context(), st)
		if err != nil {
			return err
		}
		sections = append(sections, stateSection{st, pieces})
	}
	data := struct {
		Page
		Sections []stateSection
		Notice   string // what just happened, after creating pieces or recording a step
	}{s.page(r, "Log"), sections, ""}
	if c, err := r.Cookie(flashCookie); err == nil {
		if q, err := url.ParseQuery(c.Value); err == nil {
			data.Notice = homeNotice(q)
		}
		http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1})
	}
	return s.render(w, http.StatusOK, "home", data)
}

// sameState returns the other pieces in p's project that are in p's state:
// the ones an action recorded on p can also be applied to.
func sameState(proj db.Project, p db.Piece) []db.Piece {
	var out []db.Piece
	for _, sib := range proj.Pieces {
		if sib.ID != p.ID && sib.State == p.State {
			out = append(out, sib)
		}
	}
	return out
}

func (s *Server) updatePiece(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p, err := s.Store.GetPiece(r.Context(), id)
	if err != nil {
		return err
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	// Notes are edited on the piece page and everything else on the edit
	// page, so only fields present in the form are updated.
	if _, ok := r.Form["notes"]; ok {
		if err := s.Store.UpdatePieceNotes(r.Context(), id, r.FormValue("notes")); err != nil {
			return err
		}
	}
	// The "title" field edits the project's name: all names are project names.
	// (Fields aren't called "name", which browsers autofill with your own.)
	if _, ok := r.Form["title"]; ok {
		if err := s.Store.RenameProject(r.Context(), p.ProjectID, strings.TrimSpace(r.FormValue("title"))); err != nil {
			return err
		}
	}
	if _, ok := r.Form["form"]; ok {
		if err := s.Store.SetForm(r.Context(), id, r.FormValue("form")); err != nil {
			return err
		}
	}
	if _, ok := r.Form["glaze_text"]; ok {
		if err := s.Store.SetGlazeText(r.Context(), id, r.FormValue("glaze_text")); err != nil {
			return err
		}
	}
	if r.FormValue("clays_shown") == "1" {
		ids, err := s.formClays(r)
		if err != nil {
			return err
		}
		if err := s.Store.SetPieceClays(r.Context(), id, ids); err != nil {
			return err
		}
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, pieceURL(id))
}

func (s *Server) addEvent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	action, date, err := s.formEvent(r)
	if err != nil {
		return err
	}
	ids := []int64{id}
	if err := r.ParseForm(); err != nil {
		return err
	}
	if also := r.PostForm["also"]; len(also) > 0 {
		p, err := s.Store.GetPiece(r.Context(), id)
		if err != nil {
			return err
		}
		proj, err := s.Store.GetProject(r.Context(), p.ProjectID)
		if err != nil {
			return err
		}
		allowed := map[string]int64{}
		for _, sib := range sameState(proj, p) {
			allowed[strconv.FormatInt(sib.ID, 10)] = sib.ID
		}
		for _, v := range also {
			sib, ok := allowed[v]
			if !ok {
				return &db.UserError{Msg: fmt.Sprintf("#%s isn't in this project at the same step, so the action can't apply to it.", v)}
			}
			ids = append(ids, sib)
		}
	}
	var details db.StepDetails
	if action.Measurable() {
		if details.Dims, err = formDims(r, "step"); err != nil {
			return err
		}
	}
	if action == model.Glazed {
		details.GlazeText = r.FormValue("glaze_text")
	}
	if err := s.Store.AddEvents(r.Context(), ids, action, date, details); err != nil {
		return err
	}
	// Work on the piece is done for now: back to the list of everything.
	return redirectHome(w, r, url.Values{"marked": {joinIDs(ids)}, "action": {string(action)}})
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	eid, err := pathID(r, "eid")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteEvent(r.Context(), id, eid); err != nil {
		return err
	}
	return redirect(w, r, pieceURL(id))
}

func (s *Server) movePiece(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	to, err := formInt(r, "to", 0)
	if err != nil {
		return err
	}
	if to == id {
		return &db.UserError{Msg: "A piece can't join its own project."}
	}
	if err := s.Store.MovePiece(r.Context(), id, to); err != nil {
		return err
	}
	return redirect(w, r, pieceURL(id))
}

func (s *Server) deletePiece(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if r.FormValue("confirm") != strconv.FormatInt(id, 10) {
		return &db.UserError{Msg: fmt.Sprintf("To delete, type %d to confirm.", id)}
	}
	if err := s.Store.DeletePiece(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/")
}

func (s *Server) project(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	p, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		return err
	}
	data := struct {
		Page
		Project db.Project
	}{s.page(r, p.DisplayName()), p}
	return s.render(w, http.StatusOK, "project", data)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.UpdateProject(r.Context(), id, strings.TrimSpace(r.FormValue("title")), r.FormValue("notes")); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, "/projects/"+strconv.FormatInt(id, 10))
}

func (s *Server) addToProject(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	return s.createFrom(w, r, id)
}

func (s *Server) newForm(w http.ResponseWriter, r *http.Request) error {
	next, err := s.Store.NextPieceID(r.Context())
	if err != nil {
		return err
	}
	clays, err := s.Store.Clays(r.Context())
	if err != nil {
		return err
	}
	lastClays, err := s.Store.LastUsedClays(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		NextID        int64
		Clays         []db.Clay
		SelectedClays []int64
	}{s.page(r, "New project"), next, clays, lastClays}
	return s.render(w, http.StatusOK, "new", data)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) error {
	return s.createFrom(w, r, 0)
}

func (s *Server) createFrom(w http.ResponseWriter, r *http.Request, projectID int64) error {
	// "count" is 1–4 from the pills, or "custom" with the number typed into
	// "count_custom".
	countField := "count"
	if r.FormValue("count") == "custom" {
		countField = "count_custom"
	}
	count, err := formInt(r, countField, 1)
	if err != nil {
		return err
	}
	startID, err := formInt(r, "start_id", 0)
	if err != nil {
		return err
	}
	action, date, err := s.formEvent(r)
	if err != nil {
		return err
	}
	clayIDs, err := s.formClays(r)
	if err != nil {
		return err
	}
	weight, err := formWeight(r, "clay_weight")
	if err != nil {
		return err
	}
	// Only thrown pieces have a size yet.
	var dims model.Dims
	if action == model.Thrown {
		if dims, err = formDims(r, "new"); err != nil {
			return err
		}
	}
	ids, err := s.Store.CreatePieces(r.Context(), db.NewPieces{
		Count:      int(count),
		StartID:    startID,
		ProjectID:  projectID,
		Name:       strings.TrimSpace(r.FormValue("title")),
		Form:       r.FormValue("form"),
		Action:     action,
		Date:       date,
		ClayIDs:    clayIDs,
		ClayWeight: weight,
		Dims:       dims,
	})
	if err != nil {
		return err
	}
	return redirectHome(w, r, url.Values{"created": {joinIDs(ids)}})
}

package web

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"unionpots.nyc/log/internal/backup"
	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
	"unionpots.nyc/log/internal/photos"
)

type testApp struct {
	t       *testing.T
	store   *db.Store
	h       http.Handler
	srv     *Server
	cookie  *http.Cookie
	cookie2 *http.Cookie // an extra cookie to send (e.g. the studio filter)
}

func newApp(t *testing.T) *testApp {
	t.Helper()
	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("UPDATE id_sequence SET next_piece_id = 120"); err != nil { // tests number from #120
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("clay-pots"), bcrypt.MinCost)
	now := func() time.Time { return time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC) }
	s := &Server{
		Store:  &db.Store{DB: sqlDB},
		Auth:   &Auth{PasswordHash: hash, Secret: []byte(strings.Repeat("s", 32)), Now: now},
		Backup: func() backup.Status { return backup.Status{} },
		Photos: &photos.Store{Dir: t.TempDir()},
		Now:    now,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &testApp{t: t, store: s.Store, h: s.Handler(), srv: s}
}

func (a *testApp) do(method, target string, form url.Values) *httptest.ResponseRecorder {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	if a.cookie2 != nil {
		req.AddCookie(a.cookie2)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// notice follows a redirect home the way a browser would, carrying its
// one-off notice cookie, and returns the notice shown (or "").
func (a *testApp) notice(rec *httptest.ResponseRecorder) string {
	a.t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(a.cookie)
	for _, c := range rec.Result().Cookies() {
		if c.Name == flashCookie {
			req.AddCookie(c)
		}
	}
	home := httptest.NewRecorder()
	a.h.ServeHTTP(home, req)
	body := home.Body.String()
	const open = `<p class="notice" role="status">`
	start := strings.Index(body, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	return html.UnescapeString(body[start : start+strings.Index(body[start:], "</p>")])
}

func (a *testApp) login() {
	a.t.Helper()
	rec := a.do("POST", "/login", url.Values{"password": {"clay-pots"}, "next": {"/"}})
	if rec.Code != http.StatusSeeOther {
		a.t.Fatalf("login: status %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			a.cookie = c
		}
	}
	if a.cookie == nil {
		a.t.Fatal("login: no session cookie")
	}
}

func TestPrivatePagesRequireLogin(t *testing.T) {
	a := newApp(t)
	rec := a.do("GET", "/pieces/120", nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("GET: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := a.do("POST", "/new", url.Values{"action": {"thrown"}}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST: status %d", rec.Code)
	}
	if rec := a.do("GET", "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz: status %d", rec.Code)
	}
}

func TestWrongPasswordAndRateLimit(t *testing.T) {
	a := newApp(t)
	for i := 0; i < maxFailures; i++ {
		if rec := a.do("POST", "/login", url.Values{"password": {"nope"}}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, rec.Code)
		}
	}
	if rec := a.do("POST", "/login", url.Values{"password": {"clay-pots"}}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: status %d, want 429", maxFailures, rec.Code)
	}
}

func TestTamperedCookieRejected(t *testing.T) {
	a := newApp(t)
	a.login()
	a.cookie.Value = strings.Replace(a.cookie.Value, ".", "9.", 1)
	if rec := a.do("GET", "/", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want redirect to login", rec.Code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	a := newApp(t)
	a.login()
	req := httptest.NewRequest("POST", "/new", strings.NewReader("action=thrown"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/pieces/1":       "/pieces/1",
		"//evil.com":      "/",
		"https://evil.co": "/",
		"/\\evil.com":     "/",
		"":                "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateAdvanceAndBrowse(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()

	rec := a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"3"}, "title": {"Bowls"}, "date": {"2026-10-01"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || a.notice(rec) != "Created #120, #121 and #122." {
		t.Fatalf("create: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = a.do("POST", "/pieces/121/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-03"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || a.notice(rec) != "Marked #121 trimmed." {
		t.Fatalf("add event: status %d, location %q, notice %q", rec.Code, rec.Header().Get("Location"), a.notice(rec))
	}
	if p, _ := a.store.GetPiece(ctx, 121); p.State != model.Drying {
		t.Fatalf("#121 = %s, want drying", p.State)
	}
	if p, _ := a.store.GetPiece(ctx, 120); p.State != model.PendingTrimming {
		t.Fatalf("#120 = %s; without the project option only #121 should move", p.State)
	}

	// Every page renders.
	for _, path := range []string{
		"/",
		"/pieces/120", "/projects/1", "/new",
	} {
		if rec := a.do("GET", path, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
	if rec := a.do("GET", "/pieces/999", nil); rec.Code != http.StatusNotFound {
		t.Errorf("missing piece: status %d", rec.Code)
	}
	home := a.do("GET", "/", nil).Body.String()
	if !strings.Contains(home, `href="/pieces/121"`) {
		t.Errorf("home should list drying #121")
	}
	if strings.Contains(home, `href="/pieces/7"`) {
		t.Errorf("home should not list broken #7")
	}
}

func TestApplyActionToChosenPieces(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"4"}, "title": {"Bowls"}, "date": {"2026-10-01"}})
	a.do("POST", "/pieces/123/events", url.Values{"action": {"broken"}, "date": {"2026-10-02"}})

	// The piece page offers the other pieces at the same step, not the broken one.
	page := a.do("GET", "/pieces/120", nil).Body.String()
	for _, want := range []string{`name="also" value="121"`, `name="also" value="122"`} {
		if !strings.Contains(page, want) {
			t.Errorf("piece page should offer %s", want)
		}
	}
	if strings.Contains(page, `name="also" value="123"`) {
		t.Errorf("broken #123 should not be offered")
	}

	// Choose just #122.
	rec := a.do("POST", "/pieces/120/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-03"}, "also": {"122"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	for id, want := range map[int64]model.State{120: model.Drying, 121: model.PendingTrimming, 122: model.Drying, 123: model.StateBroken} {
		if p, _ := a.store.GetPiece(ctx, id); p.State != want {
			t.Errorf("#%d = %s, want %s", id, p.State, want)
		}
	}

	// Pieces outside the offered set are refused and nothing is recorded.
	rec = a.do("POST", "/pieces/121/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-03"}, "also": {"123"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("broken sibling: status %d", rec.Code)
	}
	if p, _ := a.store.GetPiece(ctx, 121); p.State != model.PendingTrimming {
		t.Errorf("#121 should be unchanged after a refused request")
	}

	// A single-piece project offers nothing.
	a.do("POST", "/new", url.Values{"action": {"thrown"}})
	if strings.Contains(a.do("GET", "/pieces/124", nil).Body.String(), `name="also"`) {
		t.Errorf("single piece should not offer other pieces")
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}})
	if rec := a.do("POST", "/pieces/120/delete", url.Values{"confirm": {"12"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirm: status %d", rec.Code)
	}
	if rec := a.do("POST", "/pieces/120/delete", url.Values{"confirm": {"120"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: status %d", rec.Code)
	}
	if rec := a.do("GET", "/pieces/120", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: status %d", rec.Code)
	}
}

func TestLoginPageRenders(t *testing.T) {
	a := newApp(t)
	rec := a.do("GET", "/login?next=/pieces/1", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `value="/pieces/1"`) {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDaysAgo(t *testing.T) {
	for date, want := range map[string]string{
		"2026-10-03": "today",
		"2026-10-02": "yesterday",
		"2026-09-24": "9 days ago",
		"2025-10-03": "365 days ago",
		"2026-10-05": "2026-10-05", // future dates are shown as-is
	} {
		if got := daysAgo(date, "2026-10-03"); got != want {
			t.Errorf("daysAgo(%s) = %q, want %q", date, got, want)
		}
	}
}

func TestAutosavePiece(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}})

	// An htmx autosave gets 204 and no redirect.
	req := httptest.NewRequest("POST", "/pieces/120", strings.NewReader("autosave=1&title=Big+bowl&notes=speckled"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("autosave: status %d", rec.Code)
	}
	if p, _ := a.store.GetPiece(context.Background(), 120); p.ProjectName != "Big bowl" || p.Notes != "speckled" {
		t.Fatalf("not saved: %+v", p)
	}
	// Without htmx (Enter with JS off) it falls back to a redirect.
	if rec := a.do("POST", "/pieces/120", url.Values{"autosave": {"1"}, "title": {"x"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("plain post: status %d", rec.Code)
	}
}

func TestPiecePagePartOfProject(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"2"}, "title": {"Mugs"}})
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "title": {"Sculpture"}})
	if body := a.do("GET", "/pieces/120", nil).Body.String(); !strings.Contains(body, `Part of the <a href="/projects/1">Mugs</a> project`) {
		t.Errorf("multi-piece project should show part-of line")
	}
	if body := a.do("GET", "/pieces/122", nil).Body.String(); strings.Contains(body, "Part of the") {
		t.Errorf("single piece should not show part-of line")
	}
}

func TestShortDate(t *testing.T) {
	for date, want := range map[string]string{
		"2026-09-20": "Sep 20",
		"2025-06-01": "Jun 1, 2025",
	} {
		if got := shortDate(date, "2026-10-03"); got != want {
			t.Errorf("shortDate(%s) = %q, want %q", date, got, want)
		}
	}
}

func TestUndoLastStep(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "date": {"2026-10-01"}})
	if strings.Contains(a.do("GET", "/pieces/120/edit", nil).Body.String(), "Undo") {
		t.Errorf("a piece's only event should not be undoable")
	}
	a.do("POST", "/pieces/120/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-02"}})
	body := a.do("GET", "/pieces/120/edit", nil).Body.String()
	if !strings.Contains(body, "Undo “Trimmed” (Oct 2)") {
		t.Fatalf("undo button missing")
	}
	events, _ := a.store.PieceEvents(ctx, 120)
	a.do("POST", fmt.Sprintf("/pieces/120/events/%d/delete", events[1].ID), url.Values{})
	if p, _ := a.store.GetPiece(ctx, 120); p.State != model.PendingTrimming {
		t.Fatalf("after undo: %s", p.State)
	}
}

func TestCreateCount(t *testing.T) {
	a := newApp(t)
	a.login()
	rec := a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"3"}})
	if n := a.notice(rec); n != "Created #120, #121 and #122." {
		t.Fatalf("pill count: notice %q", n)
	}
	rec = a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"custom"}, "count_custom": {"6"}})
	if n := a.notice(rec); !strings.HasPrefix(n, "Created #123, #124, #125, #126, #127 and #128.") {
		t.Fatalf("custom count: notice %q", n)
	}
	if rec := a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"custom"}, "count_custom": {"lots"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad custom count: status %d", rec.Code)
	}
}

func TestMeasurementsFlow(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()

	// Created with clay weight and thrown size (applies to both pieces).
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"2"}, "title": {"Cups"},
		"clay_weight": {"1 1/4"}, "h_new": {"5"}, "w_new": {"4"}, "d_new": {"4"}})
	m, _ := a.store.GetMeasurements(ctx, 121)
	if m.ClayWeight != 1.25 || m.Dims[model.Thrown] != (model.Dims{H: 5, W: 4, D: 4}) {
		t.Fatalf("created measurements: %+v", m)
	}
	a.do("POST", "/pieces/120/events", url.Values{"action": {"trimmed"}, "also": {"121"}})

	// A drying piece's card asks for one set of dimensions, not one per piece.
	page := a.do("GET", "/pieces/120", nil).Body.String()
	if !strings.Contains(page, `name="h_step"`) || strings.Contains(page, `name="h_121"`) {
		t.Fatalf("next-step card should ask for one set of dimensions")
	}

	// Bad input is a 400 and records nothing.
	rec := a.do("POST", "/pieces/120/events", url.Values{"action": {"queued_bisque"}, "h_step": {"tall"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad dims: status %d", rec.Code)
	}
	if p, _ := a.store.GetPiece(ctx, 120); p.State != model.Drying {
		t.Fatalf("bad dims should record nothing; state %s", p.State)
	}

	// Queue both; the dimensions apply to each.
	a.do("POST", "/pieces/120/events", url.Values{"action": {"queued_bisque"}, "also": {"121"},
		"h_step": {"4 1/4"}, "w_step": {"3.5"}, "d_step": {"3.5"}})
	for _, id := range []int64{120, 121} {
		m, _ = a.store.GetMeasurements(ctx, id)
		if m.Dims[model.QueuedBisque] != (model.Dims{H: 4.25, W: 3.5, D: 3.5}) {
			t.Fatalf("#%d bisque dims: %+v", id, m.Dims)
		}
	}

	// Edit on the piece page (autosave) and see shrinkage.
	req := httptest.NewRequest("POST", "/pieces/120/measurements", strings.NewReader(url.Values{
		"autosave": {"1"}, "clay_weight": {"1.25"},
		"h_thrown": {"5"}, "w_thrown": {"4"}, "d_thrown": {"4"},
		"h_queued_bisque": {"4.5"}, "w_queued_bisque": {"3.75"}, "d_queued_bisque": {"3.75"},
		"h_finished": {"4"}, "w_finished": {"3.3"}, "d_finished": {"3.3"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(a.cookie)
	w := httptest.NewRecorder()
	a.h.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("measurements autosave: status %d: %s", w.Code, w.Body)
	}
	if body := a.do("GET", "/pieces/120", nil).Body.String(); !strings.Contains(body, "height 11%, width 12%, depth 12%") {
		t.Errorf("piece page should show shrinkage")
	}
}

func TestClayBodies(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()

	// A clay added on the clays page can be picked on the New form.
	a.do("POST", "/clays", url.Values{"title": {"Speckled buff"}})
	sb, _ := a.store.EnsureClay(ctx, "Speckled buff")
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "clay": {fmt.Sprint(sb)}})
	clays, _ := a.store.PieceClays(ctx, 120)
	if len(clays) != 1 || clays[0].Name != "Speckled buff" {
		t.Fatalf("#120 clays: %+v", clays)
	}
	speckle := clays[0].ID

	// The New form now shows it as a pill, pre-selected as the last used.
	page := a.do("GET", "/new", nil).Body.String()
	if !strings.Contains(page, fmt.Sprintf(`name="clay" value="%d" class="visually-hidden" checked`, speckle)) {
		t.Fatalf("last used clay should be pre-selected")
	}

	// Add another clay on the clays page and switch #120 to it (autosave form).
	rec := a.do("POST", "/clays", url.Values{"title": {"Porcelain"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add clay: status %d", rec.Code)
	}
	porcelain, _ := a.store.EnsureClay(ctx, "Porcelain")
	a.do("POST", "/pieces/120", url.Values{"clays_shown": {"1"}, "clay": {fmt.Sprint(porcelain)}, "notes": {""}})
	if clays, _ := a.store.PieceClays(ctx, 120); len(clays) != 1 || clays[0].ID != porcelain {
		t.Fatalf("#120 should now be porcelain: %+v", clays)
	}

	// Clay pages render; details save; unused clays can be deleted.
	for _, path := range []string{"/clays", fmt.Sprintf("/clays/%d", porcelain)} {
		if rec := a.do("GET", path, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", path, rec.Code)
		}
	}
	a.do("POST", fmt.Sprintf("/clays/%d", porcelain), url.Values{"title": {"Porcelain"}, "code": {"P-10"}, "price": {"$40"}})
	if c, _ := a.store.GetClay(ctx, porcelain); c.Code != "P-10" || c.Price != "$40" {
		t.Fatalf("clay details: %+v", c)
	}
	if rec := a.do("POST", fmt.Sprintf("/clays/%d/delete", porcelain), url.Values{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting a used clay: status %d", rec.Code)
	}
	if rec := a.do("POST", fmt.Sprintf("/clays/%d/delete", speckle), url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting an unused clay: status %d", rec.Code)
	}
}

func TestFormAndGlazesFlow(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"2"}, "form": {"bowl"}})
	if d, _ := a.store.GetDetails(ctx, 121); d.Form != "bowl" {
		t.Fatalf("form = %q", d.Form)
	}
	for _, act := range []string{"trimmed", "queued_bisque"} {
		a.do("POST", "/pieces/120/events", url.Values{"action": {act}, "also": {"121"}})
	}
	a.store.EnsureGlaze(ctx, "Pink")

	// Waiting to be bisque fired: the card asks for glaze text, with the known
	// glazes available to the page for suggestions.
	page := a.do("GET", "/pieces/120", nil).Body.String()
	if !strings.Contains(page, `name="glaze_text"`) || !strings.Contains(page, `data-glaze-names="[&#34;Pink&#34;]"`) {
		t.Fatalf("card should ask for glaze text with known glazes")
	}

	// Add "Red" inline (JSON), then glaze both pieces.
	req := httptest.NewRequest("POST", "/glazes", strings.NewReader("title=Red"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"Red"`) {
		t.Fatalf("inline add: %d %s", rec.Code, rec.Body)
	}
	a.do("POST", "/pieces/120/events", url.Values{"action": {"glazed"}, "also": {"121"}, "glaze_text": {"Pink with dabs of red"}})
	d, _ := a.store.GetDetails(ctx, 121)
	if d.GlazeText != "Pink with dabs of red" || strings.Join(d.Glazes, ",") != "Pink,Red" {
		t.Fatalf("#121 glazing: %+v", d)
	}

	// The piece page shows the text with known glazes linked.
	page = a.do("GET", "/pieces/120", nil).Body.String()
	if !strings.Contains(page, `>Pink</a> with dabs of <a href="/glazes/`) {
		t.Fatalf("glaze text should link known glazes")
	}
	// The edit page has the text; edits re-scan.
	if !strings.Contains(a.do("GET", "/pieces/120/edit", nil).Body.String(), ">Pink with dabs of red</textarea>") {
		t.Fatalf("edit page should show the glaze text")
	}
	a.do("POST", "/pieces/120", url.Values{"glaze_text": {"Just red"}, "form": {"cup"}})
	d, _ = a.store.GetDetails(ctx, 120)
	if strings.Join(d.Glazes, ",") != "Red" || d.Form != "cup" {
		t.Fatalf("#120 after edit: %+v", d)
	}
}

func TestGlazePages(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	a.do("POST", "/new", url.Values{"action": {"thrown"}})
	if rec := a.do("POST", "/glazes", url.Values{"title": {"Celdon"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("add glaze: %d", rec.Code)
	}
	typo, _ := a.store.EnsureGlaze(ctx, "Celdon")
	a.store.SetGlazeText(ctx, 120, "celdon all over")
	for _, path := range []string{"/glazes", fmt.Sprintf("/glazes/%d", typo.ID)} {
		if rec := a.do("GET", path, nil); rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
	}
	a.do("POST", fmt.Sprintf("/glazes/%d", typo.ID), url.Values{"title": {"Celadon"}})
	if d, _ := a.store.GetDetails(ctx, 120); d.GlazeText != "Celadon all over" {
		t.Fatalf("rename should fix the piece's text: %+v", d)
	}
	if rec := a.do("POST", fmt.Sprintf("/glazes/%d/delete", typo.ID), url.Values{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting a mentioned glaze: %d", rec.Code)
	}
}

func TestRatings(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	a.do("POST", "/new", url.Values{"action": {"thrown"}})
	a.do("POST", "/pieces/120/events", url.Values{"action": {"finished"}})
	// Unrated: only on the edit page.
	if strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), "rating_glaze") {
		t.Fatalf("unrated pieces shouldn't show ratings on the piece page")
	}
	if !strings.Contains(a.do("GET", "/pieces/120/edit", nil).Body.String(), `name="rating_glaze" value="5"`) {
		t.Fatalf("the edit page always has ratings")
	}
	a.do("POST", "/pieces/120/ratings", url.Values{"rating_glaze": {"5"}, "rating_overall": {"4"}})
	if r, _ := a.store.GetRatings(ctx, 120); r != (db.Ratings{Glaze: 5, Overall: 4}) {
		t.Fatalf("ratings = %+v", r)
	}
	if !strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), `name="rating_glaze" value="5" class="visually-hidden" data-toggle-off checked`) {
		t.Fatalf("saved rating should be selected")
	}
	if rec := a.do("POST", "/pieces/120/ratings", url.Values{"rating_shape": {"9"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("out of range: status %d", rec.Code)
	}
}

func TestPiecePageIsReadOnlyApartFromNotes(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	sb, _ := a.store.EnsureClay(ctx, "Speckled buff")
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "title": {"Big bowl"}, "form": {"bowl"},
		"clay": {fmt.Sprint(sb)}, "clay_weight": {"3"}, "h_new": {"5"}, "w_new": {"9"}, "d_new": {"9"}})

	page := a.do("GET", "/pieces/120", nil).Body.String()
	for _, want := range []string{"bowl", "Speckled buff", "3 lb", "5in × 9in × 9in", `name="notes"`, `href="/pieces/120/edit"`} {
		if !strings.Contains(page, want) {
			t.Errorf("piece page should show %q", want)
		}
	}
	for _, notWant := range []string{`name="form"`, `name="clay"`, `name="clay_weight"`, `name="h_thrown"`, `action="/pieces/120/delete"`} {
		if strings.Contains(page, notWant) {
			t.Errorf("piece page shouldn't have %q; it belongs on the edit page", notWant)
		}
	}
	if rec := a.do("GET", "/pieces/120/edit", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="h_thrown" value="5"`) {
		t.Fatalf("edit page: status %d", rec.Code)
	}

	// Saving notes doesn't touch the details, and saving details doesn't
	// touch the notes.
	a.do("POST", "/pieces/120", url.Values{"notes": {"lovely"}})
	a.do("POST", "/pieces/120", url.Values{"title": {"Big bowl"}, "form": {"serving bowl"}, "clays_shown": {"1"}})
	p, _ := a.store.GetPiece(ctx, 120)
	d, _ := a.store.GetDetails(ctx, 120)
	if p.Notes != "lovely" || d.Form != "serving bowl" {
		t.Fatalf("notes %q, form %q", p.Notes, d.Form)
	}
}

func TestFormatDims(t *testing.T) {
	for _, c := range []struct {
		d    model.Dims
		want string
	}{
		{model.Dims{H: 5, W: 6, D: 4}, "5in × 6in × 4in"},
		{model.Dims{H: 4.5, D: 3.75}, "4.5in × – × 3.75in"},
		{model.Dims{W: 3}, "– × 3in × –"},
	} {
		if got := formatDims(c.d); got != c.want {
			t.Errorf("formatDims(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestNextStepButtonWording(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"3"}})
	page := a.do("GET", "/pieces/120", nil).Body.String()
	if !strings.Contains(page, `data-count-label="Mark {n} pieces trimmed">Mark trimmed</button>`) {
		t.Fatalf("button should read \"Mark trimmed\", with a count label for several pieces")
	}
	if !strings.Contains(page, "<h2>Freeform notes") || strings.Index(page, "Freeform notes") > strings.Index(page, "<h2>History") {
		t.Fatalf("freeform notes should come before history")
	}
}

func TestHomeNotice(t *testing.T) {
	for q, want := range map[string]string{
		"created=137":                    "Created #137.",
		"created=137%2C138":              "Created #137 and #138.",
		"marked=135%2C136&action=glazed": "Marked #135 and #136 glazed.",
		"marked=135&action=bogus":        "",
		"created=x":                      "",
	} {
		v, _ := url.ParseQuery(q)
		if got := homeNotice(v); got != want {
			t.Errorf("homeNotice(%s) = %q, want %q", q, got, want)
		}
	}
}

func TestNoticeShownOnce(t *testing.T) {
	a := newApp(t)
	a.login()
	rec := a.do("POST", "/new", url.Values{"action": {"thrown"}})
	if a.notice(rec) == "" {
		t.Fatalf("first home view should show the notice")
	}
	if strings.Contains(a.do("GET", "/", nil).Body.String(), `class="notice"`) {
		t.Fatalf("a later home view without the cookie shouldn't")
	}
}

func TestManageClaysLink(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}})
	for _, path := range []string{"/new", "/pieces/120/edit"} {
		page := a.do("GET", path, nil).Body.String()
		if !strings.Contains(page, `<legend>Clay <span class="legend-link">(<a href="/clays">manage clays</a>)</span></legend>`) || strings.Contains(page, "pill-link") {
			t.Errorf("%s: should have a \"(manage clays)\" link in the label and no pill-shaped link", path)
		}
	}
}

func TestStartedFlow(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()

	// Status "started" on the New form; a size sent with it is ignored.
	a.do("POST", "/new", url.Values{"action": {"started"}, "count": {"2"}, "h_new": {"5"}})
	if p, _ := a.store.GetPiece(ctx, 120); p.State != model.StateStarted {
		t.Fatalf("state = %s", p.State)
	}
	if m, _ := a.store.GetMeasurements(ctx, 120); len(m.Dims) != 0 {
		t.Fatalf("a started piece has no thrown size yet: %+v", m)
	}
	home := a.do("GET", "/", nil).Body.String()
	if strings.Index(home, "<h2>Started") < 0 || strings.Index(home, "<h2>Started") > strings.Index(home, "<h2>Waiting to be trimmed") {
		t.Fatalf("Started should be the first home section")
	}

	// The card offers Thrown (default, with thrown size) or Built.
	page := a.do("GET", "/pieces/120", nil).Body.String()
	for _, want := range []string{`name="action" value="thrown" class="visually-hidden" checked`, `name="action" value="built"`,
		`data-show-for="thrown"`, "Thrown size", ">Mark thrown</button>"} {
		if !strings.Contains(page, want) {
			t.Errorf("card should contain %q", want)
		}
	}

	// #120 is thrown with its size; #121 is built.
	a.do("POST", "/pieces/120/events", url.Values{"action": {"thrown"}, "h_step": {"6"}, "w_step": {"5"}})
	a.do("POST", "/pieces/121/events", url.Values{"action": {"built"}})
	p120, _ := a.store.GetPiece(ctx, 120)
	p121, _ := a.store.GetPiece(ctx, 121)
	if p120.State != model.PendingTrimming || p121.State != model.Drying {
		t.Fatalf("states = %s, %s", p120.State, p121.State)
	}
	if m, _ := a.store.GetMeasurements(ctx, 120); m.Dims[model.Thrown] != (model.Dims{H: 6, W: 5}) {
		t.Fatalf("thrown size: %+v", m.Dims)
	}
	if !strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), "Thrown size") {
		t.Errorf("read-only details should say \"Thrown size\"")
	}
}

func TestSizeLabelsAndNoBackfill(t *testing.T) {
	for a, want := range map[model.Action]string{model.Thrown: "Thrown size", model.QueuedBisque: "Bone dry size", model.Finished: "Finished size", model.Built: "Built size"} {
		if got := sizeLabel(a); got != want {
			t.Errorf("sizeLabel(%s) = %q, want %q", a, got, want)
		}
	}
	a := newApp(t)
	a.login()
	if strings.Contains(a.do("GET", "/new", nil).Body.String(), "Backfill") {
		t.Errorf("the New page shouldn't offer backfill")
	}
}

func TestMenuAndBackupsPage(t *testing.T) {
	a := newApp(t)
	a.login()
	home := a.do("GET", "/", nil).Body.String()
	for _, want := range []string{`<a href="/">In progress</a>`, `href="/new">+ New project</a>`, `<a href="/new">New project</a>`, `href="/clays">Clays</a>`, `href="/glazes">Glazes</a>`,
		`href="/backups">Backups</a>`, `action="/logout"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home should contain %q", want)
		}
	}
	if !strings.Contains(a.do("GET", "/new", nil).Body.String(), "<h1>New project</h1>") {
		t.Errorf("new page should be titled \"New project\"")
	}
	// Backups are off in tests, which the page says.
	if rec := a.do("GET", "/backups", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Backups are off: no backup storage") {
		t.Fatalf("backups page: %d", rec.Code)
	}
	// The login page has no menu.
	a.cookie = nil
	if strings.Contains(a.do("GET", "/login", nil).Body.String(), "data-menu") {
		t.Errorf("login page shouldn't show the menu")
	}
}

func TestLoginMessage(t *testing.T) {
	a := newApp(t)
	if strings.Contains(a.do("GET", "/login", nil).Body.String(), "login-message") {
		t.Fatalf("no message by default")
	}
	srv := &Server{Store: a.store, Auth: &Auth{Now: time.Now}, Backup: func() backup.Status { return backup.Status{} },
		Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LoginMessage: "Password: clay-pots-dev"}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/login", nil))
	if !strings.Contains(rec.Body.String(), `<p class="login-message">Password: clay-pots-dev</p>`) {
		t.Fatalf("login page should show the message")
	}
}

func TestBackupsPageShowsPolicyAndStoredCopies(t *testing.T) {
	a := newApp(t)
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC)
	srv := &Server{Store: a.store, Auth: &Auth{PasswordHash: nil, Secret: []byte(strings.Repeat("s", 32)), Now: func() time.Time { return now }},
		Backup: func() backup.Status { return backup.Status{Enabled: true, Target: "s3://bucket/prod", LastOK: now} },
		Snapshots: func(context.Context) ([]backup.Snapshot, error) {
			return []backup.Snapshot{
				{Key: "db/snapshots/a", At: now.AddDate(0, 0, -29)},
				{Key: "db/snapshots/b", At: now.Add(-10 * time.Minute)},
			}, nil
		},
		Now: func() time.Time { return now }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := srv.Handler()
	cookie, _ := srv.Auth.newSession()
	req := httptest.NewRequest("GET", "/backups", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"every 10 minutes", "kept for 30 days", "never deleted", "<dd>2</dd>", "10 min ago", "29 days ago", "s3://bucket/prod"} {
		if !strings.Contains(body, want) {
			t.Errorf("backups page should contain %q", want)
		}
	}
}

func TestBanners(t *testing.T) {
	a := newApp(t)
	a.login()
	banners := func() string {
		body := a.do("GET", "/", nil).Body.String()
		var out []string
		for _, part := range strings.Split(body, `role="status">`)[1:] {
			out = append(out, part[:strings.Index(part, "<")])
		}
		return strings.Join(out, " | ")
	}
	if got := banners(); got != "Backups disabled" {
		t.Errorf("no backups: %q", got)
	}
	a.srv.DefaultPassword = true
	a.srv.Backup = func() backup.Status { return backup.Status{Problem: "missing -spaces-secret"} }
	if got := banners(); got != "Using the default password: set one with -password-hash | Backups misconfigured: missing -spaces-secret" {
		t.Errorf("default password + misconfigured: %q", got)
	}
	a.srv.Backup = func() backup.Status {
		return backup.Status{Enabled: true, LastError: "InvalidAccessKeyId: the key is wrong", LastErrorAt: a.srv.Now()}
	}
	if got := banners(); !strings.Contains(got, "Backups not working: InvalidAccessKeyId: the key is wrong (see Backups)") {
		t.Errorf("never worked: %q", got)
	}
	// Logged out: no warnings (they'd tell anyone the password is the default).
	a.cookie = nil
	if body := a.do("GET", "/login", nil).Body.String(); strings.Contains(body, `role="status"`) {
		t.Errorf("login page shouldn't show warnings")
	}
}

func TestDemoMode(t *testing.T) {
	a := newApp(t)
	a.srv.Demo, a.srv.LoginMessage = true, "This is a demo. The password is “potter”."
	login := a.do("GET", "/login", nil).Body.String()
	if !strings.Contains(login, "The password is “potter”") || !strings.Contains(login, "demo site: data is reset every hour") {
		t.Errorf("demo login page should show the password and the demo banner")
	}
	a.login()
	if _, err := a.store.CreatePieces(context.Background(), db.NewPieces{Count: 1, Action: "thrown", Date: "2026-10-01"}); err != nil {
		t.Fatal(err)
	}
	page := a.do("GET", "/pieces/120", nil).Body.String()
	if strings.Contains(page, "Add photos") || !strings.Contains(page, "turned off in the demo") {
		t.Errorf("demo piece page shouldn't offer uploads")
	}
	if strings.Contains(page, "Backups disabled") {
		t.Errorf("demo shows its own banner, not the backup one")
	}
	if rec := a.upload("/pieces/120/photos", true, map[string][]byte{"a.jpg": jpegBytes(t, 10, 10)}); rec.Code != 400 {
		t.Errorf("demo upload: %d", rec.Code)
	}
}

func TestLogOutEverywhere(t *testing.T) {
	a := newApp(t)
	rotated := 0
	a.srv.Auth.Rotate = func() ([]byte, error) { rotated++; return []byte(strings.Repeat("n", 32)), nil }
	a.login()
	phone := a.cookie
	a.login() // a second device
	if !strings.Contains(a.do("GET", "/", nil).Body.String(), "Log out everywhere") {
		t.Fatal("menu should offer Log out everywhere")
	}
	rec := a.do("POST", "/logout-everywhere", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?out=all" || rotated != 1 {
		t.Fatalf("logout everywhere: %d %q rotated=%d", rec.Code, rec.Header().Get("Location"), rotated)
	}
	a.cookie = phone
	if rec := a.do("GET", "/", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("the other device should be logged out too: %d", rec.Code)
	}
	a.cookie = nil
	if !strings.Contains(a.do("GET", "/login?out=all", nil).Body.String(), "Logged out on every device.") {
		t.Errorf("login page should confirm")
	}
	a.login() // logging in again works with the new secret
	if rec := a.do("GET", "/", nil); rec.Code != 200 {
		t.Errorf("log in again: %d", rec.Code)
	}
	// Not in the demo.
	a.srv.Demo = true
	if strings.Contains(a.do("GET", "/", nil).Body.String(), "Log out everywhere") {
		t.Errorf("demo shouldn't offer it")
	}
	if rec := a.do("POST", "/logout-everywhere", nil); rec.Code != 404 || rotated != 1 {
		t.Errorf("demo: %d rotated=%d", rec.Code, rotated)
	}
}

func TestApproximateDates(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	ids, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-09-01"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.AddEvents(ctx, ids, model.Trimmed, "2026-09-08", db.StepDetails{}); err != nil {
		t.Fatal(err)
	}
	// Only the notebook import sets the flag, directly in the database.
	if _, err := a.store.DB.Exec("UPDATE events SET approximate = 1 WHERE action = 'trimmed'"); err != nil {
		t.Fatal(err)
	}
	page := a.do("GET", "/pieces/120", nil).Body.String()
	if !strings.Contains(page, `<span class="date" title="Approximate date">early Sep</span>`) {
		t.Errorf("history should show the approximate date as early/mid/late")
	}
	if !strings.Contains(page, `<span class="date">Sep 1</span>
        <span class="date">2026</span>`) {
		t.Errorf("the first step always has its year")
	}
	if home := a.do("GET", "/", nil).Body.String(); !strings.Contains(home, `title="Approximate date">~`) {
		t.Errorf("home row should mark an approximate state date")
	}
}

func TestFinishedPieces(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	mk := func(name, thrown, finished string) {
		ids, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Name: name, Action: model.Thrown, Date: thrown})
		if err != nil {
			t.Fatal(err)
		}
		if finished != "" {
			if err := a.store.AddEvents(ctx, ids, model.Finished, finished, db.StepDetails{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("Old bowl", "2024-03-01", "2024-05-02")
	mk("New mug", "2026-01-01", "2026-02-03")
	mk("Still drying", "2026-09-01", "")
	mk("Newer plate", "2026-03-01", "2026-04-20")
	if !strings.Contains(a.do("GET", "/", nil).Body.String(), `href="/finished">Finished pieces</a>`) {
		t.Errorf("menu should link to Finished pieces")
	}
	body := a.do("GET", "/finished", nil).Body.String()
	order := []string{"<h2>Spring 2026</h2>", "Newer plate", "Apr 20", "<h2>Winter 2025/26</h2>", "New mug", "Feb 3",
		"<h2>Spring 2024</h2>", "Old bowl", "May 2"}
	at := 0
	for _, s := range order {
		i := strings.Index(body[at:], s)
		if i < 0 {
			t.Fatalf("finished page: %q missing or out of order", s)
		}
		at += i
	}
	if strings.Contains(body, "Still drying") {
		t.Errorf("unfinished pieces don't belong on the finished page")
	}
}

func TestSeason(t *testing.T) {
	for date, want := range map[string]string{
		"2024-06-01": "Summer 2024", "2024-08-31": "Summer 2024", "2024-09-01": "Fall 2024",
		"2024-12-05": "Winter 2024/25", "2025-02-28": "Winter 2024/25", "2025-03-01": "Spring 2025",
		"1999-12-31": "Winter 1999/00",
	} {
		if got := season(date); got != want {
			t.Errorf("season(%s) = %q, want %q", date, got, want)
		}
	}
}

func TestRateWhenFinishing(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	ids, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 2, Action: model.Glazed, Date: "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}
	a.store.SetRatings(ctx, ids[1], db.Ratings{Shape: 2})
	page := a.do("GET", "/pieces/120", nil).Body.String()
	card := page[strings.Index(page, `class="next-step"`):]
	card = card[:strings.Index(card, "</form>")]
	if !strings.Contains(card, `name="rating_overall" value="5"`) {
		t.Fatal("the Mark finished card should offer ratings")
	}
	// Finish both (the other via "also"), rating glaze and overall.
	rec := a.do("POST", "/pieces/120/events", url.Values{"action": {"finished"}, "also": {"121"},
		"rating_glaze": {"4"}, "rating_overall": {"5"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	if r, _ := a.store.GetRatings(ctx, 120); r != (db.Ratings{Glaze: 4, Overall: 5}) {
		t.Errorf("#120 ratings = %+v", r)
	}
	if r, _ := a.store.GetRatings(ctx, 121); r != (db.Ratings{Glaze: 4, Shape: 2, Overall: 5}) {
		t.Errorf("#121 keeps its shape rating: %+v", r)
	}
	// Other steps don't touch ratings.
	ids, _ = a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-10-01"})
	a.do("POST", fmt.Sprintf("/pieces/%d/events", ids[0]), url.Values{"action": {"trimmed"}, "rating_glaze": {"3"}})
	if r, _ := a.store.GetRatings(ctx, ids[0]); r != (db.Ratings{}) {
		t.Errorf("trimming shouldn't rate: %+v", r)
	}
}

func TestPublicPage(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	ids, err := a.store.CreatePieces(ctx, db.NewPieces{Count: 2, Name: "Macrocosmos Planters", Action: model.Thrown,
		Date: "2026-04-18", Form: "planter", ClayWeight: 6.5, Dims: model.Dims{H: 6, W: 10, D: 10}})
	if err != nil {
		t.Fatal(err)
	}
	clay, _ := a.store.EnsureClay(ctx, "Speckle")
	a.store.SetPieceClays(ctx, ids[0], []int64{clay})
	a.store.AddEvents(ctx, ids, model.Glazed, "2026-05-15", db.StepDetails{GlazeText: "Floating Blue"})
	a.store.AddEvents(ctx, ids[:1], model.Finished, "2026-05-22", db.StepDetails{Dims: model.Dims{H: 5, W: 9.25, D: 9.25}})
	a.store.AddEvents(ctx, ids[1:], model.Finished, "2026-05-23", db.StepDetails{})
	a.store.UpdatePieceNotes(ctx, ids[0], "SECRET NOTE")
	a.store.SetRatings(ctx, ids[0], db.Ratings{Glaze: 5, Shape: 5, Overall: 5})
	a.upload("/pieces/120/photos", false, map[string][]byte{"a.jpg": jpegBytes(t, 40, 30)})
	if !strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), `href="/p/120"`) {
		t.Errorf("the piece page should link to its public page")
	}
	a.srv.PublicBaseURL = "https://unionpots.nyc"
	if !strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), `href="https://unionpots.nyc/p/120"`) {
		t.Errorf("with a public base URL, the link should go there")
	}
	a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-09-01"}) // #122, unfinished

	a.cookie = nil // public: no login
	rec := a.do("GET", "/p/120", nil)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("public page: %d", rec.Code)
	}
	for _, want := range []string{"union<br>pots</a></h1>", `<p class="number">#120</p>`,
		"<dt>project</dt><dd>macrocosmos planters, 1/2</dd>", "planter", "6.5 lb of speckle clay",
		"<dd>floating blue</dd>", "<dt>size</dt><dd>5 × 9.25 × 9.25 in</dd>", "<h2>production<br>timeline</h2>",
		"<dt>thrown</dt><dd>Apr 18, 2026</dd>", "<dt>glazed</dt><dd>May 15</dd>", "<dt>finished</dt><dd>May 22</dd>",
		`<a href="https://unionpots.nyc">unionpots.nyc</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("public page should show %q", want)
		}
	}
	for _, private := range []string{"SECRET NOTE", "rating", "Rating", "/pieces/", "<img", ".jpg", "spring 2026"} {
		if strings.Contains(body, private) {
			t.Errorf("public page must not show %q", private)
		}
	}
	a.store.CreatePieces(ctx, db.NewPieces{Count: 1, Name: "Random bowl", Action: model.Finished, Date: "2026-05-01"})
	if body := a.do("GET", "/p/123", nil).Body.String(); !strings.Contains(body, "<dt>name</dt><dd>random bowl</dd>") {
		t.Errorf("a single piece has a name")
	}
	// Sizes before firing are never shown.
	if body := a.do("GET", "/p/121", nil).Body.String(); strings.Contains(body, "<dt>size") {
		t.Errorf("without a finished size, no size is shown")
	}
	for _, path := range []string{"/p/122", "/p/999", "/p/junk", "/p/120/anything", "/p/"} {
		rec := a.do("GET", path, nil)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), "<p>page not found</p>") {
			t.Errorf("%s: %d, want the shared 404 page", path, rec.Code)
		}
	}
	p120, _ := a.store.PiecePhotos(ctx, 120)
	if rec := a.do("GET", "/p/120/"+p120[0].SHA256+"_1600.jpg", nil); rec.Code == 200 {
		t.Errorf("photos aren't public")
	}
	a.srv.Demo = true
	if rec := a.do("GET", "/p/120", nil); rec.Code != 404 {
		t.Errorf("no public pages in the demo: %d", rec.Code)
	}
}

func TestIdeas(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	if !strings.Contains(a.do("GET", "/", nil).Body.String(), `<a href="/ideas">App ideas</a>`) {
		t.Errorf("menu should link to App ideas")
	}
	a.do("POST", "/ideas", url.Values{"text": {"a search page"}})
	a.do("POST", "/ideas", url.Values{"text": {"export to zip"}, "simple": {"1"}})
	if rec := a.do("POST", "/ideas", url.Values{"text": {"  "}}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty idea: %d", rec.Code)
	}
	open, _, _ := a.store.Ideas(ctx)
	if len(open) != 2 {
		t.Fatalf("ideas = %+v", open)
	}
	if !open[1].Simple || open[0].Simple {
		t.Errorf("the simple checkbox should be saved: %+v", open)
	}
	a.do("POST", fmt.Sprintf("/ideas/%d", open[1].ID), url.Values{"text": {"export everything to a zip"}, "simple": {"1"}})
	page := a.do("GET", "/ideas", nil).Body.String()
	if !strings.Contains(page, "a search page</textarea>") || !strings.Contains(page, "export everything to a zip</textarea>") {
		t.Errorf("open ideas should be listed")
	}
	if strings.Contains(page, open[0].Hash) {
		t.Errorf("hashes are never shown in the app")
	}

	// Implemented: marked done (as on startup), then hidden under Done.
	a.store.MarkIdeasDone(ctx, []string{open[0].Hash})
	page = a.do("GET", "/ideas", nil).Body.String()
	if strings.Contains(page, "a search page</textarea>") || !strings.Contains(page, "Done (1)") {
		t.Errorf("done ideas move under Done")
	}

	// The feed: public, open ideas with their hashes.
	if !strings.Contains(page, "/ideas/feed</code>") {
		t.Errorf("the page should show the feed link")
	}
	a.cookie = nil
	rec := a.do("GET", "/ideas/feed", nil)
	feed := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(feed, open[1].Hash) || !strings.Contains(feed, "export everything to a zip") {
		t.Fatalf("feed: %d %q", rec.Code, feed)
	}
	if i, j := strings.Index(feed, "# Simple ideas (1)"), strings.Index(feed, open[1].Hash); i < 0 || j < i {
		t.Errorf("the simple idea should be listed under Simple ideas:\n%s", feed)
	}
	if strings.Contains(feed, open[0].Hash) {
		t.Errorf("done ideas aren't in the feed")
	}
	if rec := a.do("GET", "/ideas", nil); rec.Code != http.StatusSeeOther {
		t.Errorf("the page itself needs a login: %d", rec.Code)
	}
	a.login()
	a.do("POST", fmt.Sprintf("/ideas/%d/delete", open[1].ID), nil)
	if open, _, _ := a.store.Ideas(ctx); len(open) != 0 {
		t.Errorf("deleted: %+v", open)
	}
}

func TestStudios(t *testing.T) {
	a := newApp(t)
	a.login()
	ctx := context.Background()
	// Add two studios on the Studios page.
	a.do("POST", "/studios", url.Values{"title": {"Clayworks"}})
	a.do("POST", "/studios", url.Values{"title": {"Greenpoint"}})
	st, _ := a.store.Studios(ctx)
	if len(st) != 2 {
		t.Fatalf("studios = %+v", st)
	}
	cw, gp := st[0].ID, st[1].ID

	// The New form offers them; the choice is recorded.
	if !strings.Contains(a.do("GET", "/new", nil).Body.String(), `name="studio" value="`+fmt.Sprint(cw)+`"`) {
		t.Errorf("New should offer the studios")
	}
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "title": {"Bowls"}, "count": {"2"}, "studio": {fmt.Sprint(cw)}})
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "title": {"Mugs"}, "studio": {fmt.Sprint(gp)}})
	if p, _ := a.store.GetPiece(ctx, 120); p.StudioName != "Clayworks" {
		t.Errorf("#120 studio = %q", p.StudioName)
	}
	// The New form defaults to the studio used last.
	if !strings.Contains(a.do("GET", "/new", nil).Body.String(), `name="studio" value="`+fmt.Sprint(gp)+`" class="visually-hidden" checked`) {
		t.Errorf("New should default to the last studio")
	}
	// Pieces added to a project are made where the project is.
	p120, _ := a.store.GetPiece(ctx, 120)
	a.do("POST", fmt.Sprintf("/projects/%d/pieces", p120.ProjectID), url.Values{"action": {"thrown"}, "count": {"1"}})
	if p, _ := a.store.GetPiece(ctx, 123); p.StudioName != "Clayworks" {
		t.Errorf("added piece #123 studio = %q, want the project's", p.StudioName)
	}
	// The piece page shows it; the edit page changes it.
	if !strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), "Clayworks") {
		t.Errorf("piece page should show the studio")
	}
	a.do("POST", "/pieces/121", url.Values{"studio": {fmt.Sprint(gp)}})
	if p, _ := a.store.GetPiece(ctx, 121); p.StudioName != "Greenpoint" {
		t.Errorf("edit: #121 studio = %q", p.StudioName)
	}

	// In progress: filter by studio, remembered.
	home := func(q string) string { return a.do("GET", "/"+q, nil).Body.String() }
	all := home("")
	if !strings.Contains(all, `class="studio-filter"`) || !strings.Contains(all, "#120") || !strings.Contains(all, "#122") {
		t.Fatalf("home should show the filter and every piece")
	}
	rec := a.do("GET", fmt.Sprintf("/?studio=%d", cw), nil)
	body := rec.Body.String()
	if !strings.Contains(body, "#120") || strings.Contains(body, "#121") || strings.Contains(body, "#122") {
		t.Errorf("filtered to Clayworks: wrong pieces")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == studioCookie {
			a.cookie2 = c
		}
	}
	if a.cookie2 == nil {
		t.Fatal("the choice should be remembered")
	}
	if body := home(""); strings.Contains(body, "#122") || !strings.Contains(body, "#120") {
		t.Errorf("the filter should stick")
	}
	if body := home("?studio=all"); !strings.Contains(body, "#122") {
		t.Errorf("All shows everything again")
	}

	// A studio in use can't be deleted; an unused one can.
	if rec := a.do("POST", fmt.Sprintf("/studios/%d/delete", cw), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting a used studio: %d", rec.Code)
	}
	a.do("POST", "/studios", url.Values{"title": {"Spare"}})
	st, _ = a.store.Studios(ctx)
	for _, s := range st {
		if s.Name == "Spare" {
			a.do("POST", fmt.Sprintf("/studios/%d/delete", s.ID), nil)
		}
	}
	if st, _ = a.store.Studios(ctx); len(st) != 2 {
		t.Errorf("unused studio should be deleted: %+v", st)
	}
	a.do("POST", fmt.Sprintf("/studios/%d", gp), url.Values{"title": {"Greenpoint Studio"}})
	if p, _ := a.store.GetPiece(ctx, 122); p.StudioName != "Greenpoint Studio" {
		t.Errorf("rename: %q", p.StudioName)
	}
}

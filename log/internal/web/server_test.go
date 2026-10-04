package web

import (
	"context"
	"fmt"
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
)

type testApp struct {
	t      *testing.T
	store  *db.Store
	h      http.Handler
	cookie *http.Cookie
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
	hash, _ := bcrypt.GenerateFromPassword([]byte("clay-pots"), bcrypt.MinCost)
	now := func() time.Time { return time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC) }
	s := &Server{
		Store:  &db.Store{DB: sqlDB},
		Auth:   &Auth{PasswordHash: hash, Secret: []byte(strings.Repeat("s", 32)), Now: now},
		Backup: func() backup.Status { return backup.Status{} },
		Now:    now,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return &testApp{t: t, store: s.Store, h: s.Handler()}
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
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
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

	rec := a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"3"}, "name": {"Bowls"}, "date": {"2026-10-01"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/created?ids=120%2C121%2C122" {
		t.Fatalf("create: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = a.do("POST", "/pieces/121/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-03"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/pieces/121" {
		t.Fatalf("add event: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if p, _ := a.store.GetPiece(ctx, 121); p.State != model.Drying {
		t.Fatalf("#121 = %s, want drying", p.State)
	}
	if p, _ := a.store.GetPiece(ctx, 120); p.State != model.PendingTrimming {
		t.Fatalf("#120 = %s; without the project option only #121 should move", p.State)
	}

	// Backfill an old finished piece.
	rec = a.do("POST", "/new", url.Values{"action": {"broken"}, "start_id": {"#7"}, "date": {"2025-03-01"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("backfill: status %d: %s", rec.Code, rec.Body)
	}
	if p, _ := a.store.GetPiece(ctx, 7); p.State != model.StateBroken {
		t.Fatalf("#7 state = %q", p.State)
	}

	// Duplicate explicit ID is a 400 with the message shown.
	rec = a.do("POST", "/new", url.Values{"action": {"thrown"}, "start_id": {"7"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "piece #7 already exists") {
		t.Fatalf("duplicate: status %d", rec.Code)
	}

	// Every page renders.
	for _, path := range []string{
		"/",
		"/pieces/120", "/pieces/7", "/projects/1", "/new", "/created?ids=120,121",
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
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"4"}, "name": {"Bowls"}, "date": {"2026-10-01"}})
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
	req := httptest.NewRequest("POST", "/pieces/120", strings.NewReader("autosave=1&name=Big+bowl&notes=speckled"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(a.cookie)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("autosave: status %d", rec.Code)
	}
	if p, _ := a.store.GetPiece(context.Background(), 120); p.Name != "Big bowl" || p.Notes != "speckled" {
		t.Fatalf("not saved: %+v", p)
	}
	// Without htmx (Enter with JS off) it falls back to a redirect.
	if rec := a.do("POST", "/pieces/120", url.Values{"autosave": {"1"}, "name": {"x"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("plain post: status %d", rec.Code)
	}
}

func TestPiecePagePartOfProject(t *testing.T) {
	a := newApp(t)
	a.login()
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"2"}, "name": {"Mugs"}})
	a.do("POST", "/new", url.Values{"action": {"thrown"}, "name": {"Sculpture"}})
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
	if strings.Contains(a.do("GET", "/pieces/120", nil).Body.String(), "Undo") {
		t.Errorf("a piece's only event should not be undoable")
	}
	a.do("POST", "/pieces/120/events", url.Values{"action": {"trimmed"}, "date": {"2026-10-02"}})
	body := a.do("GET", "/pieces/120", nil).Body.String()
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
	if loc := rec.Header().Get("Location"); loc != "/created?ids=120%2C121%2C122" {
		t.Fatalf("pill count: location %q", loc)
	}
	rec = a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"custom"}, "count_custom": {"6"}})
	if loc := rec.Header().Get("Location"); loc != "/created?ids=123%2C124%2C125%2C126%2C127%2C128" {
		t.Fatalf("custom count: location %q", loc)
	}
	if rec := a.do("POST", "/new", url.Values{"action": {"thrown"}, "count": {"custom"}, "count_custom": {"lots"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad custom count: status %d", rec.Code)
	}
}

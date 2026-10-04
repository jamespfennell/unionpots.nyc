# log.unionpots.nyc — implementation plan

A private work log for Union Pots pottery, with optional public pages for
individual pieces. Derived from `pmt.md` plus the review discussion; this
document is the source of truth for decisions made so far.

## 1. Decisions so far

| Topic | Decision |
|---|---|
| Name / domain | **log.unionpots.nyc** ("the log"). Public piece pages at **unionpots.nyc/p/{id}**. |
| Users | Only the owner. Single password. Public pages are read-only and unauthenticated. |
| Client | Responsive, mobile-first web app (installable PWA manifest, no offline mode). Studio connectivity is good, so it's online-only. |
| Stack | Go (stdlib `net/http`, `html/template`) + htmx, SQLite, photos on local disk. |
| Hosting | Existing DigitalOcean VM, Docker container, data on the VM disk, behind the existing Caddy. |
| Backups | DigitalOcean Spaces (S3-compatible): in-app hourly DB snapshot (≤1h data loss accepted), mirrored photo objects. No Litestream. |
| Actions & states | History is dated actions (thrown, built, trimmed, queued for bisque, glazed, finished, broken). The current state is what the piece is waiting for: waiting to be trimmed → drying → waiting to be bisque fired → waiting to be glaze fired → finished / broken. |
| Ratings | glaze / shape / overall, 1–5, only once a piece is finished. |
| Metadata | Typed fields for the searchable things (form, clays, glazes, weight, 3-D dimensions, ratings) + free-form key/value + notes. Project-level values are inherited by pieces. |
| Units | Imperial: inches and pounds. |
| Glazes | Typed list of glazes used + optional free-text "application notes" for complex cases. |
| Firing details | Omitted for now (studio uses standard firings). Easy to add later as a typed field. |
| Ownership | Record who has a finished piece (kept / gifted / sold, recipient, date). Always private. |
| IDs | Piece ID = the number marked on the piece. Never reused. Counter starts at 120. No QR codes. |
| Stale-piece reminders | Not wanted. Instead: list all pieces in a given state. |
| OCR of notebooks | Deferred to v3. |

## 2. Domain model

### 2.1 Projects and pieces

- A **project** groups 1+ pieces (a set of 4 bowls; a planter + plate; a one-off
  bowl). Projects have an internal ID that is *never shown* as a number, to
  avoid confusion with piece IDs. They're identified by name (optional; defaults
  to e.g. "Project of #121–#124").
- A **piece** has a globally unique integer ID — the number written on its base.
  - New pieces get the next number from a monotonic counter (`id_sequence`), so
    deleting #130 never causes #130 to be handed out again.
  - When backfilling history, an explicit ID can be entered if unused.
  - The counter starts at **120**: the first piece logged in the app is #120.
    Historical pieces (#1–#119) are backfilled with explicit IDs.
- Single-piece projects are the common case, so the UI hides the project:
  "New piece" silently creates a project; the project page only appears once a
  project has 2+ pieces (or has a name).
- Pieces can be moved between projects; projects can be merged/split by
  moving pieces. An empty project is deleted automatically.

### 2.2 Actions and states

A piece's history is a list of dated **events**, each recording an **action**:
something that happened to it. Its current **state** is what it is waiting
for, derived from its latest event (by date, then insertion order) and
denormalised onto `pieces.state` in the same transaction.

| Action (event) | Resulting state | Typical next action |
|---|---|---|
| `thrown` | Waiting to be trimmed | trimmed |
| `trimmed` | Drying | queued_bisque |
| `built` (hand-built; replaces thrown + trimmed) | Drying | queued_bisque |
| `queued_bisque` (dry: measured and put in the kiln queue) | Waiting to be bisque fired | glazed |
| `glazed` | Waiting to be glaze fired | finished |
| `finished` (out of the glaze kiln) | Finished | — |
| `broken` (cracked, kiln accident, reclaimed…) | Broken | — |

- States are named for the wait because that's how the lists are used: most
  of a piece's life is waiting for drying or for the studio tech to fire it.
- There is no "bisqued" state. Glazing happens straight after the bisque
  firing, so the `glazed` date stands in for the bisque date.
- Transitions are **not enforced**: any action can follow any other. This
  supports re-glazing (`finished → glazed → finished`), skipped steps and
  corrections. The UI just offers the typical next action as the primary
  button.
- `broken` can happen in any state. The state it broke in is the one before
  it in the history, so no extra states are needed. What happened goes in the
  piece's notes ("cracked at base").
- Event dates are calendar dates (`YYYY-MM-DD`, America/New_York), default
  today, editable — so backdating historical pieces is natural.

### 2.3 Metadata

Metadata attaches to either a project or a piece. Each value is a row
`(owner, key, value_json)`. Fields come in two kinds:

**Typed fields** — defined in a Go registry (`internal/model/fields.go`), each
with a type, validation, label, display order, whether it's allowed at project
level, and whether it's public-eligible:

| Key | Type | Project-level? | Notes |
|---|---|---|---|
| `form` | form ID | yes | Bowl, mug, plate, planter… (vocab). |
| `clays` | list of clay IDs | yes | Usually one; supports multi-clay pieces. |
| `glazes` | list of glaze IDs | yes | Which glazes were used, in rough order. |
| `glaze_notes` | text | yes | Optional: how they were applied (dipped, layered, wax resist…). |
| `clay_weight` | number (lb) | yes | Weight of clay at throwing, decimal pounds (e.g. 1.25). |
| `dims_thrown` | dimensions | yes | `{h, w, d}` in inches, each optional. For round forms w = d = diameter; the form offers "round" to fill both from one input. |
| `dims_bisqued` | dimensions | yes | |
| `dims_finished` | dimensions | yes | Enables shrinkage % per clay body. |
| `rating_glaze` | int 1–5 | no | Editable only when the piece is finished. |
| `rating_shape` | int 1–5 | no | ditto |
| `rating_overall` | int 1–5 | no | ditto |

**Free-form fields** — any other key, text value. The key input autocompletes
from keys already in use so they stay consistent. A CLI `rename-key` command
handles cleanup when keys change over time.

**Units** — imperial throughout: inches (decimal, e.g. 4.5) and pounds
(decimal). Values are stored as plain numbers in those units, with the unit
fixed by the field definition; shrinkage % is unit-independent. If metric is
ever wanted, it's a display-layer conversion.

**Vocabularies** — clays, glazes and forms are rows in `clays` / `glazes` /
`forms` tables (name, notes). Selecting one autocompletes; typing a new name creates it after
a confirm ("Create new clay 'Speckled buff'?"). This keeps search from
fragmenting on spelling. Vocab entries can be renamed and merged.

**Notes** — projects and pieces each have a plain `notes` text column (not
metadata) for anything unstructured.

**Inheritance** — the effective value of a key on a piece is:
1. the piece's own row, if present (a row with JSON `null` means "explicitly
   cleared, don't inherit");
2. otherwise the project's row;
3. otherwise unset.

Lists are replaced, never merged. The piece page shows inherited values in a
muted style with an "inherited from project" hint and an "override" action.
A SQL view `effective_metadata` computes this so search can use it directly.

### 2.4 Ownership

`piece_transfers(piece, date, kind, recipient, note)`, kind ∈ `kept`, `gifted`,
`sold`, `other`. The latest row is "who has it". Never exposed publicly.

### 2.5 Photos

- Attached to a piece or a project. Optional caption and optional state tag
  (defaulting to the piece's current state at upload — "this is what it looked
  like when trimmed").
- Stored content-addressed by SHA-256. The original is kept untouched
  (private). Derivatives are generated on upload: 400px thumb and 1600px
  display, JPEG, **EXIF orientation applied then all EXIF stripped** (removes
  GPS). Only derivatives are ever served publicly.
- Upload via `<input type="file" accept="image/jpeg,image/png,image/webp"
  capture="environment" multiple>`. iOS Safari converts HEIC to JPEG when the
  accept list excludes HEIC. Anything undecodable is rejected with a clear
  message. Each photo has a `public` flag (default false).

## 3. Database schema (SQLite)

```sql
CREATE TABLE projects (
  id          INTEGER PRIMARY KEY,
  name        TEXT,
  notes       TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE id_sequence (next_piece_id INTEGER NOT NULL);  -- single row

CREATE TABLE pieces (
  id             INTEGER PRIMARY KEY,         -- the number on the pot
  project_id     INTEGER NOT NULL REFERENCES projects(id),
  name           TEXT,                        -- optional ("big bowl")
  notes          TEXT NOT NULL DEFAULT '',
  state          TEXT NOT NULL,               -- derived from the latest event
  public         INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);

CREATE TABLE events (
  id           INTEGER PRIMARY KEY,
  piece_id     INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  action       TEXT NOT NULL CHECK (action IN ('thrown','built','trimmed','queued_bisque','glazed','finished','broken')),
  occurred_on  TEXT NOT NULL,                 -- YYYY-MM-DD
  created_at   TEXT NOT NULL
);

CREATE TABLE metadata (
  project_id  INTEGER REFERENCES projects(id) ON DELETE CASCADE,
  piece_id    INTEGER REFERENCES pieces(id)   ON DELETE CASCADE,
  key         TEXT NOT NULL,
  value       TEXT,                           -- JSON; NULL = explicitly cleared
  updated_at  TEXT NOT NULL,
  CHECK ((project_id IS NULL) != (piece_id IS NULL))
);
CREATE UNIQUE INDEX metadata_project_key ON metadata(project_id, key) WHERE project_id IS NOT NULL;
CREATE UNIQUE INDEX metadata_piece_key   ON metadata(piece_id, key)   WHERE piece_id   IS NOT NULL;

CREATE TABLE clays  (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');
CREATE TABLE glazes (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');
CREATE TABLE forms  (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE COLLATE NOCASE, notes TEXT NOT NULL DEFAULT '');

CREATE TABLE piece_transfers (
  id           INTEGER PRIMARY KEY,
  piece_id     INTEGER NOT NULL REFERENCES pieces(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL CHECK (kind IN ('kept','gifted','sold','other')),
  recipient    TEXT NOT NULL DEFAULT '',
  occurred_on  TEXT NOT NULL,
  note         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE photos (
  id            INTEGER PRIMARY KEY,
  sha256        TEXT NOT NULL UNIQUE,
  project_id    INTEGER REFERENCES projects(id) ON DELETE SET NULL,
  piece_id      INTEGER REFERENCES pieces(id)   ON DELETE SET NULL,
  state         TEXT,
  caption       TEXT NOT NULL DEFAULT '',
  width         INTEGER NOT NULL,
  height        INTEGER NOT NULL,
  original_ext  TEXT NOT NULL,
  public        INTEGER NOT NULL DEFAULT 0,
  position      INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  backed_up_at  TEXT                          -- NULL until mirrored to Spaces
);

CREATE VIEW effective_metadata AS
  SELECT p.id AS piece_id, m.key, m.value, 0 AS inherited
    FROM pieces p JOIN metadata m ON m.piece_id = p.id
  UNION ALL
  SELECT p.id, m.key, m.value, 1
    FROM pieces p JOIN metadata m ON m.project_id = p.project_id
   WHERE NOT EXISTS (SELECT 1 FROM metadata o WHERE o.piece_id = p.id AND o.key = m.key);
```

- Migrations: numbered `.sql` files embedded with `embed`, applied in order
  at startup and tracked in `PRAGMA user_version`.
- Pragmas: `journal_mode=WAL` (readers don't block the writer, incl. backups), `foreign_keys=ON`,
  `busy_timeout=5000`.
- Scale: hundreds to low thousands of pieces, so no indexing concerns beyond
  the obvious (`pieces.state`, `events.piece_id`,
  `metadata.key`).

## 4. Screens

Mobile-first. One column on phones, two on desktop. htmx for partial updates
(record an action, edit a field, upload photos) without a JS framework.

1. **Home** (`/`) — in-progress work only: one section per in-progress state
   (waiting to be trimmed, drying, waiting to be bisque fired, waiting to be
   glaze fired),
   each a heading with a count and the pieces in that state, ordered by
   piece ID. Each piece shows its project name, with its position for
   multi-piece projects ("Set of 4 bowls (2/4)"; a lone piece shows its own
   name), and how long ago it entered the state ("9 days ago").
   Plain lists, no dividers or state colours. Tapping a piece opens it. No bulk actions here. Finished and broken
   pieces will be reachable another way (TBD). "+ New" lives in the header.
2. *(The separate per-state list page was folded into Home.)*
3. **Piece** (`/pieces/121`):
   - Title: **#121 Set of 4 bowls (2/4)** (as on Home), then the state in
     plain text, then "Part of the *Set of 4 bowls* project" (linked) for
     multi-piece or named projects.
   - **Next-step widget**: a box whose bottom edge is the button for the
     usual next step ("→ Queued for bisque today"). Inside the box: "Also
     apply to other pieces in the project: #120 #122 #123". Tapping a number
     highlights it and includes that piece; the button then reads "… · 3
     pieces". Only pieces in the same project *and* the same state are
     offered (a broken bowl or a piece at a different step never is), and the
     server enforces this. This box is where step-specific input will go
     later (e.g. dimensions when queuing for bisque).
   - History reads left to right: Thrown › Trimmed › Queued for bisque, with
     the date under each action. Read-only; events carry no notes (notes
     belong to the piece).
   - Details (name, notes) autosave as you type; a quiet "Saving…" / "Saved"
     appears next to the heading ("Saved" fades after 2 s), or an error if a
     save fails.
   - Ratings section (only when finished), ownership section (only when
     finished), photos, public toggle with a "view public page" link.
   - **Advanced** (collapsed, at the bottom) for unusual steps: record any
     action with any date, undo the last step, move to another piece's
     project or split off, delete (type the number to confirm).
4. **Project** (`/projects/{id}`) — name, project-level metadata, member
   pieces as cards, "add piece to project", "move pieces", photos.
5. **New piece / project** (`/new`) — minimum input: number of pieces
   (default 1), started by throwing or building, date (today). Everything else is
   optional and collapsed under "More details". The result shows the
   allocated ID(s) in big type so they can be written on the pots. Backfill
   mode: "Use specific ID", any latest action, any date.
6. **Search** (`/search`) — filters: state, form, clay, glaze, minimum
   rating per dimension, date range (any event, or a specific action), free
   text (names, notes, glaze notes, free-form values), and free-form
   `key = value`. Filters live in query params, so searches are bookmarkable.
   Results show as a grid of thumbnails, or as a table on desktop.
7. **Library** (`/clays`, `/glazes`, `/forms`) — every entry with a piece
   count; detail pages show their pieces sorted by rating, plus average
   shrinkage (height/width/depth, thrown → finished) for clays. Rename/merge
   actions.
8. **Admin** — deferred. Backup problems show in a site-wide banner. Export
   and other admin tools come back when needed.
9. **Login** (`/login`).
10. **Public piece** (`unionpots.nyc/p/121`, v2) — see §7.

## 5. Architecture

### 5.1 Repository layout

The app lives in this repo under `log/` so it shares the site's design
tokens and CI. It is built as a separate Docker image.

```
log/
  go.mod                      module unionpots.nyc/log
  cmd/log/main.go             subcommands: serve, backup, restore, export, rename-key, hash-password
  internal/db/                connection, migrations/, queries
  internal/model/             actions & states, field registry, validation, inheritance
  internal/photos/            ingest, derivatives, storage, Spaces mirror
  internal/backup/            hourly DB snapshot, upload, retention, restore
  internal/web/               router, handlers, auth, templates, static (embedded)
    templates/*.html
    static/htmx.min.js        vendored, pinned
    static/app.css
  Dockerfile
```

### 5.2 Dependencies (kept minimal)

- `modernc.org/sqlite` — pure-Go SQLite, so no cgo and a simple static build.
- `github.com/disintegration/imaging` — decode, EXIF auto-orient, resize.
- `golang.org/x/image/webp` — WebP decoding.
- `github.com/aws/aws-sdk-go-v2/service/s3` — photo mirror to Spaces.
- `golang.org/x/crypto/bcrypt` — password hash.
- htmx (vendored JS file). No other front-end dependencies, no build step.

### 5.3 Auth

- Config: `LOG_PASSWORD_HASH` (bcrypt, generated with `log hash-password`) and
  `LOG_SESSION_SECRET`.
- Login sets an HMAC-signed session cookie (`HttpOnly; Secure;
  SameSite=Lax`, 1 year).
- All non-public routes require the cookie.
- CSRF: SameSite=Lax plus an `Origin`/`Sec-Fetch-Site` check on every
  non-GET request.
- Login is rate-limited (e.g. 5 attempts/min per IP, in memory).

### 5.4 Configuration (env)

| Var | Purpose |
|---|---|
| `LOG_ADDR` | listen address (default `:8080`) |
| `LOG_DATA_DIR` | `/data` — holds `log.db` and `photos/` |
| `LOG_PASSWORD_HASH`, `LOG_SESSION_SECRET` | auth |
| `LOG_PUBLIC_BASE_URL` | `https://unionpots.nyc` for public links |
| `SPACES_ENDPOINT`, `SPACES_REGION`, `SPACES_BUCKET`, `SPACES_KEY`, `SPACES_SECRET` | backups. If unset, the app refuses to start unless `LOG_BACKUPS=off` |
| `LOG_BACKUPS` | `off` to explicitly run without backups (local dev / testing) |

### 5.5 On-disk layout

```
/data/log.db (+ -wal, -shm)
/data/photos/originals/ab/abcdef….jpg
/data/photos/derived/ab/abcdef…_400.jpg
/data/photos/derived/ab/abcdef…_1600.jpg
```

### 5.6 Deployment

- Multi-stage Dockerfile: `golang` build stage → small runtime image
  (`alpine` or distroless) containing only the `log` binary.
- Entrypoint: `/app/log serve`. Backups run inside the process (§6), so
  there's no wrapper script or second process.
- If `/data/log.db` is missing at startup, the app does **not** auto-restore.
  It creates an empty DB only when the bucket has no backups, and otherwise
  refuses to start with "backups exist in Spaces; run `log restore` or set
  `LOG_ALLOW_EMPTY_DB=1`". That stops a mis-mounted volume from silently
  starting fresh (and then backing up an empty DB).
- CI: add a second job to `.github/workflows/build.yml` that runs
  `go test ./...` and builds and pushes `jamespfennell/log.unionpots.nyc:latest`
  (path-filtered on `log/**`).
- VM: run the container with `-v /srv/log.unionpots.nyc:/data`.
- DNS: `log.unionpots.nyc` A record pointing at the VM.
- TLS and routing: the existing front Caddy on the VM gets two additions.
  The log container's port is published only on localhost (or the two share a
  Docker network). Caddy provisions the `log.` certificate automatically.

  ```caddyfile
  log.unionpots.nyc {
      request_body {
          max_size 50MB            # multi-photo uploads
      }
      reverse_proxy localhost:8081
  }

  unionpots.nyc {
      handle /p/* {                # v2: public piece pages
          reverse_proxy localhost:8081
      }
      handle {
          reverse_proxy localhost:8080   # existing site container
      }
  }
  ```

  The log app trusts `X-Forwarded-For` from the front Caddy only (used for
  login rate limiting). It serves `/p/*` without auth regardless of host, and
  every other route requires a session.

## 6. Backup and restore

Target: **lose at most ~1 hour of edits** in a disaster (VM/disk loss).
Deploys and restarts lose nothing, because a snapshot is taken on shutdown.

**Database:** a goroutine in `internal/backup` runs hourly, plus once on
graceful shutdown (SIGTERM):
1. `VACUUM INTO '/data/backup-tmp.db'` produces a consistent, compacted copy.
   It runs as a read transaction, so the app keeps serving and writing
   meanwhile. At this DB size (a few MB) it takes milliseconds.
2. `PRAGMA integrity_check` on the copy. If it fails, don't upload, and log
   loudly.
3. **Skip if nothing changed:** compare the copy's SHA-256 with the last
   uploaded one. `VACUUM INTO` output is deterministic for unchanged data, so
   most hours upload nothing.
4. gzip and upload to `s3://<bucket>/db/hourly/2026-10-03T14-00Z.db.gz`. The
   first snapshot each day is also copied to `db/daily/2026-10-03.db.gz`.
5. Retention: after each run the app deletes `db/hourly/` snapshots older
   than 7 days, but never the newest one, so even if backups stopped for a
   long time there is always at least one hourly snapshot (a bucket
   lifecycle rule would delete by age alone). Deletion removes every stored
   version, so bucket versioning doesn't keep expired snapshots around.
   `db/daily/` is kept forever, since a few MB/day is negligible.
6. Record the result (time, size, success/error) in memory for the
   banner. The last uploaded hash lives in `/data/backup-state.json`, not the DB,
   so recording a backup never itself counts as a change.

If an upload fails, the next hourly run retries, since the stored hash hasn't
changed. A thin site-wide banner warns if no backup run has
succeeded (uploaded, or confirmed nothing changed) in the last 3 hours.

`log backup` runs the same thing on demand (e.g. before a risky migration).
The app also takes a snapshot automatically before applying any new schema
migration.

**No bucket configured:** the app refuses to start unless `LOG_BACKUPS=off`
is set explicitly. With `off` it runs normally, but the photo mirror and DB
snapshots are disabled and every page shows a "Backups disabled" banner.
Local development uses `LOG_BACKUPS=off`, so production can never be
unbacked-up by accident.

**Photos:**
- Photo files are immutable and content-addressed. After ingest, a background
  goroutine uploads the original and its derivatives to
  `s3://<bucket>/photos/...` and sets `backed_up_at`.
- On startup and hourly, it retries any photo with `backed_up_at IS NULL`.
- Spaces bucket versioning is on, so an accidental delete is recoverable.

**Export:** `log export out.zip` writes
`data.json` (every project, piece, event, metadata row, vocab entry and
transfer, with clay/glaze names resolved) plus `photos/originals/`. It's a
human-readable, tool-independent archive of the life's work. Running it
occasionally and keeping the zip on the desktop is the third copy.

**Restore** is documented step by step in `restore_playbook.md` (Compose
on the VM): stop the service, move the data directory aside, run
`log restore` (newest snapshot by default, or `-at`/`-key` for an older one,
integrity-checked, photos downloaded too), start it, spot-check. Run once as
a drill.

## 7. Public pages (v2)

- `unionpots.nyc/p/{id}` shows a page only if `pieces.public = 1`; otherwise
  it returns 404 (same response as nonexistent, so private pieces aren't
  enumerable).
- Shown: ID, name, form, project name and sibling public pieces, event dates
  (made on / finished on), clays, glazes, dimensions (finished), public photos
  (derivatives only).
- Never shown: ratings, notes, glaze_notes, free-form fields, ownership,
  originals.
- The fixed public field set lives in the field registry
  (`PublicEligible`); a per-piece override can come later if needed.
- Styled exactly like unionpots.nyc (same layout and type), with a link back
  to the homepage.
- Optional later: a "pieces" index on the main site listing all public pieces.

## 8. Visual design

Taken from `index.html` and kept deliberately small: few colours, flat,
simple. The rules (also at the top of `app.css`):

- **Colour has a job.** Ink (`#1f1b18`) for content, muted grey (`#7a716a`)
  for secondary information (dates, counts, labels, hints), terracotta
  (`#7a3f2e`) *only* for things you can press (links, disclosure toggles,
  the primary button, selected pills), red only for errors and deletion.
  Background `#faf8f5`, one hairline colour, white only inside form fields.
- **Two typefaces, fixed roles.** Spectral for the brand, page titles and
  section headings; system sans for everything else, piece numbers included
  (semibold, tabular figures).
- **At most one box per page.** Form fields, plus the next-step card on the
  piece page. Disclosures ("Advanced", "Backfill", "Add pieces") are plain
  text with a chevron; secondary buttons are a quiet outline; states are
  plain grey text, not badges.
- **Lists** have no dividers; numbers sit in a fixed column so names line
  up; rows highlight on hover. Home groups are separated by a hairline rule.
- **Pills** (tap to toggle) for small choices: "Started by" and "Also apply
  to #130 #131".
- Touch targets ≥ 44px; 640px max content width; no dark mode in v1.
- One hand-written `app.css` with custom properties; static URLs are
  content-hashed so updates are picked up immediately.

## 9. Testing

- `internal/model`: unit tests for inheritance resolution, action → state
  validation, field validation, ID allocation (no reuse after delete, explicit
  ID collisions).
- `internal/db`: tests against a temp SQLite file covering migrations from
  empty, the `effective_metadata` view, and search queries.
- `internal/photos`: golden tests covering orientation (a rotated EXIF
  fixture), GPS stripping (assert no EXIF in derivatives) and resize bounds.
- `internal/backup`: snapshot → upload (to a fake S3 / local dir) →
  restore round-trip; skip-when-unchanged; refuses to start with backups
  unconfigured.
- `internal/web`: `httptest` tests covering auth required on private routes,
  public 404 for private pieces, CSRF rejection, applying an action to a
  project.
- Export → import round-trip test (an import command exists for testing and
  disaster recovery, not as a user feature).

## 10. Milestones

**M0 — Skeleton and safety net**
- Go module, `serve` with login/logout, migrations, layout template + CSS
  tokens.
- Dockerfile, CI job, deployed at log.unionpots.nyc.
- Hourly DB backup + `log restore` working against Spaces, **restore drill
  performed**.

**M1 — Core workflow**
- Projects, pieces, ID allocation, events, current-state
  denormalisation.
- Home page grouped by state, piece page (history, notes, apply to project),
  new-piece/project flow.

**M2 — Metadata**
- Field registry, clays/glazes vocab with autocomplete and create-on-confirm.
- Project inheritance with override/clear.
- Free-form fields, ratings (finished only), ownership transfers.

**M3 — Photos**
- Upload (multi), derivatives, EXIF handling, gallery on piece/project
  pages.
- Spaces mirror with retry.

**M4 — Search, library, export**
- Search page, clay/glaze library with shrinkage stats.
- Export zip, `rename-key` and vocab merge.

**M5 — Backfill**
- Enter the ~120 historical pieces from the notebook using backfill mode
  (explicit IDs, backdated events).
- If entry is too slow, add a desktop "spreadsheet-style" bulk form or CSV
  import.

**v2 — Public pages**
- `unionpots.nyc/p/{id}`, per-piece and per-photo public toggles, reverse
  proxy route.

**v3 — Notebook OCR**
- Photograph a notebook page and send it to a vision LLM, which returns
  structured JSON. That becomes a review form pre-filled with proposed
  pieces/fields, and nothing is saved until confirmed. Probably first as a
  desktop CLI for the historical notebooks.

**Later / explicitly out of scope for now:**
- Firing details (cone, atmosphere, kiln).
- Offline mode.
- Multi-user.
- Reminders.
- QR codes.

## 11. Open questions

None outstanding. Resolved in review:
- Front proxy: the existing Caddy on the VM (§5.6).
- Units: imperial, inches and pounds (§2.3).
- Form: a typed vocab field (§2.3).
- Dimensions: height, width and depth (§2.3).
- End states: just finished / broken (§2.2).
- States named for what the piece waits for; drying is its own state (§2.2).

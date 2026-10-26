package db

import (
	"context"
	"net/url"
	"strings"
)

// Link is one of the useful links (menu → Links).
type Link struct {
	ID    int64
	Title string
	URL   string
}

// Host is the link's site, shown next to its title.
func (l Link) Host() string {
	u, err := url.Parse(l.URL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// Links returns all links, by title.
func (s *Store) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, title, url FROM links ORDER BY title COLLATE NOCASE, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ls []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Title, &l.URL); err != nil {
			return nil, err
		}
		ls = append(ls, l)
	}
	return ls, rows.Err()
}

// AddLink adds a link. A URL without a scheme gets https://.
func (s *Store) AddLink(ctx context.Context, title, rawURL string) (int64, error) {
	title, u, err := checkLink(title, rawURL)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.ExecContext(ctx, "INSERT INTO links (title, url, created_at) VALUES (?, ?, ?)", title, u, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateLink(ctx context.Context, id int64, title, rawURL string) error {
	title, u, err := checkLink(title, rawURL)
	if err != nil {
		return err
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE links SET title = ?, url = ? WHERE id = ?", title, u, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteLink(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM links WHERE id = ?", id)
	return err
}

// checkLink trims the title and URL and checks the URL is a web address.
func checkLink(title, rawURL string) (string, string, error) {
	title, rawURL = strings.TrimSpace(title), strings.TrimSpace(rawURL)
	if title == "" {
		return "", "", userErr("a link needs a title")
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", userErr("a link needs a web address, like example.com/form")
	}
	return title, u.String(), nil
}

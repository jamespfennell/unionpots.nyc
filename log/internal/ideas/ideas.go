// Package ideas lists the App ideas that have been implemented. Each line of
// done.txt is an idea's hash, optionally followed by a note; lines starting
// with # are comments. On startup the app marks these ideas done. See
// log/CLAUDE.md for how ideas are addressed.
package ideas

import (
	_ "embed"
	"regexp"
	"strings"
)

//go:embed done.txt
var doneFile string

var hashRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Done returns the hashes of implemented ideas.
func Done() []string { return parse(doneFile) }

func parse(s string) []string {
	var hashes []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if f := strings.Fields(line)[0]; hashRE.MatchString(f) {
			hashes = append(hashes, f)
		}
	}
	return hashes
}

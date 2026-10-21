package main

import (
	"database/sql"
	"database/sql/driver"
	"html"
	"net/url"
	"regexp"
	"strings"

	"modernc.org/sqlite"
)

// Search must see exactly the same source-redacted text as member responses.
// Otherwise a search for a hidden hostname can disclose which entries came from it.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("infogap_public_text", 4, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		text, _ := args[0].(string)
		visibility, _ := args[1].(string)
		if visibility != "internal" {
			return text, nil
		}
		name, _ := args[2].(string)
		link, _ := args[3].(string)
		return privateSourceFilter(name, link).scrub(text), nil
	})
}

func publicTextSQL(expression string, u *User) string {
	if u != nil && (u.Role == "admin" || u.Role == "reviewer") {
		return expression
	}
	return "infogap_public_text(" + expression + ",g.source_visibility,g.source_name,g.source_url)"
}

func defaultSourceVisibility(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "public"
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "linux.do" || strings.HasSuffix(host, ".linux.do") {
		return "internal"
	}
	return "public"
}

type sourceFilter struct{ sensitive *regexp.Regexp }

func normalizeSourceText(text string) string {
	// Decode the common URL/HTML forms before checking, including percent-encoded
	// hostnames and invisible characters used to break an otherwise exact name.
	for range 2 {
		text = html.UnescapeString(text)
		if decoded, err := url.PathUnescape(text); err == nil {
			text = decoded
		}
	}
	return strings.Map(func(r rune) rune {
		if r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\u2060' || r == '\ufeff' {
			return -1
		}
		return r
	}, text)
}

func privateSourceFilter(name, link string) sourceFilter {
	patterns := []string{}
	for _, term := range []string{strings.TrimSpace(name), strings.TrimSpace(link)} {
		if term != "" {
			patterns = append(patterns, regexp.QuoteMeta(normalizeSourceText(term)))
		}
	}
	if parsed, err := url.Parse(link); err == nil {
		host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
		host = strings.TrimPrefix(host, "www.")
		if host != "" {
			patterns = append(patterns, regexp.QuoteMeta(host))
		}
		if defaultSourceVisibility(link) == "internal" {
			patterns = append(patterns, `linux\s*\.?\s*do`)
		}
	}
	if len(patterns) == 0 {
		return sourceFilter{}
	}
	return sourceFilter{regexp.MustCompile("(?i)(?:" + strings.Join(patterns, "|") + ")")}
}

var privateMarkdownLink = regexp.MustCompile(`!?\[([^\]\n]*)\]\(\s*<?([^\n)]+)>?\s*\)`)
var privateReferenceLink = regexp.MustCompile(`(?m)^\s*\[[^\]\n]+\]:[^\n]*$`)
var sourceURLToken = regexp.MustCompile(`(?i)(?:(?:https?://|//|www\.)[^\s<>"'\[\]()]+|(?:[\p{L}\p{N}-]+\.)+[\p{L}]{2,}/[^\s<>"'\[\]()]*)`)

func (f sourceFilter) contains(text string) bool {
	return f.sensitive != nil && f.sensitive.MatchString(normalizeSourceText(text))
}

func (f sourceFilter) scrub(text string) string {
	if f.sensitive == nil || text == "" {
		return text
	}
	text = normalizeSourceText(text)
	text = privateMarkdownLink.ReplaceAllStringFunc(text, func(link string) string {
		parts := privateMarkdownLink.FindStringSubmatch(link)
		if f.contains(parts[2]) {
			return f.sensitive.ReplaceAllString(parts[1], "社区线索")
		}
		return link
	})
	text = privateReferenceLink.ReplaceAllStringFunc(text, func(line string) string {
		if f.contains(line) {
			return ""
		}
		return line
	})
	text = sourceURLToken.ReplaceAllStringFunc(text, func(link string) string {
		if f.contains(link) {
			return "社区线索"
		}
		return link
	})
	return f.sensitive.ReplaceAllString(text, "社区线索")
}

func internalSourceRemains(g *Gap) bool {
	f := privateSourceFilter(g.SourceName, g.SourceURL)
	for _, text := range append([]string{g.Title, g.Summary, g.Body}, g.Tags...) {
		if f.contains(text) {
			return true
		}
	}
	return false
}

func redactInternalSource(g *Gap) {
	f := privateSourceFilter(g.SourceName, g.SourceURL)
	g.Title, g.Summary, g.Body = f.scrub(g.Title), f.scrub(g.Summary), f.scrub(g.Body)
	g.Author = f.scrub(g.Author)
	for i, tag := range g.Tags {
		g.Tags[i] = f.scrub(tag)
	}
	g.SourceName, g.SourceURL, g.ReviewNote = "", "", ""
}

func migrateSourceVisibility(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(gaps)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "source_visibility" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	if _, err = db.Exec("ALTER TABLE gaps ADD COLUMN source_visibility TEXT NOT NULL DEFAULT 'public' CHECK(source_visibility IN ('public','internal'))"); err != nil {
		return err
	}
	rows, err = db.Query("SELECT id,source_url FROM gaps")
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		var link string
		if err = rows.Scan(&id, &link); err != nil {
			rows.Close()
			return err
		}
		if defaultSourceVisibility(link) == "internal" {
			ids = append(ids, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = db.Exec("UPDATE gaps SET source_visibility='internal' WHERE id=?", id); err != nil {
			return err
		}
	}
	return nil
}

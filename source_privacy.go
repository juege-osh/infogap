package main

import (
	"database/sql"
	"database/sql/driver"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

// Search must see exactly the same source-redacted text as member responses.
// Otherwise a search for a hidden hostname can disclose which entries came from it.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("infogap_public_text", 4, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		text, _ := args[0].(string)
		visibility, _ := args[1].(string)
		if visibility != "internal" && visibility != "locked" && visibility != "internal_locked" {
			return text, nil
		}
		name, _ := args[2].(string)
		link, _ := args[3].(string)
		if visibility == "locked" {
			return scrubLockedLinks(privateSourceFilter(name, link).scrubWith(text, "来源已锁定")), nil
		}
		if visibility == "internal_locked" {
			return scrubLockedLinks(privateSourceFilter(name, link).scrub(text)), nil
		}
		return privateSourceFilter(name, link).scrub(text), nil
	})
}

func publicTextSQL(expression string, u *User) string {
	if u != nil && (u.Role == "admin" || u.Role == "reviewer") {
		return expression
	}
	// Only a server-authenticated numeric ID is interpolated. Keeping the CASE
	// parameter-free preserves every caller's existing argument ordering.
	visibility := "CASE WHEN g.source_visibility='internal' THEN 'internal_locked' ELSE 'locked' END"
	if u != nil && u.ID > 0 {
		id := strconv.FormatInt(u.ID, 10)
		authorized := "g.author_id=" + id + " OR EXISTS(SELECT 1 FROM unlocks source_access WHERE source_access.gap_id=g.id AND source_access.user_id=" + id + ")"
		visibility = "CASE WHEN " + authorized + " THEN CASE WHEN g.source_visibility='internal' THEN 'internal' ELSE 'public' END ELSE CASE WHEN g.source_visibility='internal' THEN 'internal_locked' ELSE 'locked' END END"
	}
	return "infogap_public_text(" + expression + "," + visibility + ",g.source_name,g.source_url)"
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
var lockedURLToken = regexp.MustCompile(`(?i)(?:[a-z][a-z0-9+.-]*://[^\s<>"'\[\]()]+|//[^\s<>"'\[\]()]+|(?:[\p{L}\p{N}-]+\.)+[\p{L}]{2,}(?::[0-9]+)?(?:/[^\s<>"'\[\]()]*)?)`)

// A locked preview may contain several evidence links unrelated to source_url.
// None of their targets may become a shortcut around the unlock boundary.
func scrubLockedLinks(text string) string {
	text = normalizeSourceText(text)
	text = privateMarkdownLink.ReplaceAllString(text, "$1")
	text = privateReferenceLink.ReplaceAllString(text, "")
	return lockedURLToken.ReplaceAllString(text, "来源已锁定")
}

func (f sourceFilter) contains(text string) bool {
	return f.sensitive != nil && f.sensitive.MatchString(normalizeSourceText(text))
}

func (f sourceFilter) scrub(text string) string {
	return f.scrubWith(text, "社区线索")
}

func (f sourceFilter) scrubWith(text, replacement string) string {
	if f.sensitive == nil || text == "" {
		return text
	}
	text = normalizeSourceText(text)
	text = privateMarkdownLink.ReplaceAllStringFunc(text, func(link string) string {
		parts := privateMarkdownLink.FindStringSubmatch(link)
		if f.contains(parts[2]) {
			return f.sensitive.ReplaceAllString(parts[1], replacement)
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
			return replacement
		}
		return link
	})
	return f.sensitive.ReplaceAllString(text, replacement)
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

// Call after gapScan has computed owner/staff/unlock access and applied the
// stronger internal-source rule. Response redaction never changes stored data.
func redactLockedSource(g *Gap) {
	if g == nil || g.Unlocked {
		return
	}
	f := privateSourceFilter(g.SourceName, g.SourceURL)
	g.Title = scrubLockedLinks(f.scrubWith(g.Title, "来源已锁定"))
	g.Summary = scrubLockedLinks(f.scrubWith(g.Summary, "来源已锁定"))
	g.Author = scrubLockedLinks(f.scrubWith(g.Author, "来源已锁定"))
	for i, tag := range g.Tags {
		g.Tags[i] = scrubLockedLinks(f.scrubWith(tag, "来源已锁定"))
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

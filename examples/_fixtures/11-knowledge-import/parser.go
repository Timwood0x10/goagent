// This file implements a lightweight, structure-aware markdown parser.
//
// Unlike a plain text reader, it classifies content into typed blocks
// (heading, paragraph, fenced code, table, list, blockquote) and extracts
// document-level metadata (title, YAML frontmatter, tags). Downstream chunking
// relies on these block boundaries so that code blocks and tables are never
// split across chunks.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BlockType enumerates the structural units recognised by the parser.
type BlockType string

// Block type constants.
const (
	BlockHeading   BlockType = "heading"
	BlockParagraph BlockType = "paragraph"
	BlockCode      BlockType = "code"
	BlockTable     BlockType = "table"
	BlockList      BlockType = "list"
	BlockQuote     BlockType = "quote"
)

// Block is a single structural unit of a markdown document.
type Block struct {
	Type  BlockType // Structural classification.
	Level int       // Heading level 1-6; 0 for non-heading blocks.
	Lang  string    // Fenced code language; empty otherwise.
	Title string    // Heading text; empty for non-heading blocks.
	Text  string    // Full raw text of the block, including markers.
}

// Document is the parsed representation of a single markdown file.
type Document struct {
	Path        string            // Source file path.
	Title       string            // Resolved document title.
	Frontmatter map[string]string // Parsed YAML frontmatter key/value pairs.
	Tags        []string          // Merged frontmatter and inline tags.
	Blocks      []Block           // Ordered structural blocks.
}

var (
	headingRe    = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*\s*$`)
	fenceRe      = regexp.MustCompile("^(`{3,}|~{3,})\\s*([\\w+-]*)\\s*$")
	tableSepRe   = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	listItemRe   = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s+\S`)
	quoteRe      = regexp.MustCompile(`^\s*>`)
	hashtagRe    = regexp.MustCompile(`(^|\s)#([A-Za-z][A-Za-z0-9_\-/]*)`)
	frontKVRe    = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)
	listBulletRe = regexp.MustCompile(`^\s*-\s+(.+)$`)
)

// ParseFile reads a markdown file from disk and parses it.
//
// Args:
//
//	path - filesystem path to a markdown file, must be non-empty.
//
// Returns:
//
//	doc - parsed document, never nil on success.
//	err - a read error with context.
func ParseFile(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, wrapf(err, "read markdown %q", path)
	}
	return ParseContent(path, string(data)), nil
}

// ParseContent parses in-memory markdown content into a Document.
// It never returns an error: malformed markdown degrades to paragraph blocks.
//
// Args:
//
//	path    - logical source path, used for the fallback title only.
//	content - raw markdown text.
//
// Returns:
//
//	doc - parsed document, never nil.
func ParseContent(path, content string) *Document {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	front, body := extractFrontmatter(lines)
	sc := &blockScanner{lines: body}
	blocks := sc.scan()

	doc := &Document{
		Path:        path,
		Frontmatter: front,
		Blocks:      blocks,
	}
	doc.Title = resolveTitle(path, front, blocks)
	doc.Tags = collectTags(front, blocks)
	return doc
}

// extractFrontmatter splits leading YAML frontmatter from the body lines.
// It returns an empty (non-nil) map when no frontmatter is present.
func extractFrontmatter(lines []string) (map[string]string, []string) {
	front := make(map[string]string)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return front, lines
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return front, lines
	}

	parseFrontmatterBody(lines[1:end], front)
	if end+1 >= len(lines) {
		return front, nil
	}
	return front, lines[end+1:]
}

// parseFrontmatterBody parses simple "key: value" and "- item" frontmatter.
// Only scalar values and a single-level "tags" list are supported, which
// covers the common note-taking cases without a full YAML dependency.
func parseFrontmatterBody(body []string, front map[string]string) {
	lastKey := ""
	for _, raw := range body {
		line := strings.TrimRight(raw, " \t")
		if line == "" {
			continue
		}
		if item := listBulletRe.FindStringSubmatch(line); item != nil && lastKey != "" {
			existing := front[lastKey]
			if existing == "" {
				front[lastKey] = strings.TrimSpace(item[1])
			} else {
				front[lastKey] = existing + ", " + strings.TrimSpace(item[1])
			}
			continue
		}
		if kv := frontKVRe.FindStringSubmatch(strings.TrimLeft(line, " \t")); kv != nil {
			key := strings.ToLower(kv[1])
			val := strings.TrimSpace(kv[2])
			front[key] = strings.Trim(val, "[]")
			lastKey = key
		}
	}
}

// resolveTitle picks a document title: frontmatter title, then the first
// heading, then the file name without extension.
func resolveTitle(path string, front map[string]string, blocks []Block) string {
	if t := strings.TrimSpace(front["title"]); t != "" {
		return t
	}
	for _, b := range blocks {
		if b.Type == BlockHeading {
			return b.Title
		}
	}
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// collectTags merges frontmatter tags with inline "#tag" hashtags found in
// paragraph, list and quote text. The result is de-duplicated and stable.
func collectTags(front map[string]string, blocks []Block) []string {
	seen := make(map[string]struct{})
	var tags []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		tags = append(tags, t)
	}

	for _, raw := range strings.Split(front["tags"], ",") {
		add(strings.Trim(strings.TrimSpace(raw), "\"'"))
	}
	for _, b := range blocks {
		if b.Type == BlockHeading || b.Type == BlockCode {
			continue
		}
		for _, m := range hashtagRe.FindAllStringSubmatch(b.Text, -1) {
			add(m[2])
		}
	}
	return tags
}

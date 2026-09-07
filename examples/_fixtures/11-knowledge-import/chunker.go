// This file implements section-first chunking over parsed markdown blocks.
//
// Chunks follow document structure rather than a fixed character window: each
// heading starts a new section, and a section becomes one chunk unless it
// exceeds the configured size, in which case it is split at block boundaries
// only. Code blocks and tables are therefore never cut in half.
package main

import (
	"strings"
	"unicode/utf8"
)

// Chunk is a retrieval unit derived from one or more structural blocks.
type Chunk struct {
	Index       int      // Zero-based position within the document.
	Content     string   // Chunk text, ready for embedding.
	HeadingPath []string // Breadcrumb from document title to this section.
	Level       int      // Section heading level; 0 for the preamble.
	BlockTypes  []string // Distinct block types present, in first-seen order.
	Tags        []string // Document tags associated with this chunk.
}

// section is an internal grouping of blocks under a single heading.
type section struct {
	path   []string
	level  int
	blocks []Block
}

// ChunkDocument converts a parsed document into ordered chunks.
//
// Args:
//
//	doc - a parsed document, must be non-nil.
//	cfg - knowledge configuration controlling size and overlap.
//
// Returns:
//
//	chunks - ordered chunks; empty when the document has no content.
func ChunkDocument(doc *Document, cfg KnowledgeConfig) []Chunk {
	if doc == nil {
		return nil
	}
	sections := splitSections(doc)
	var chunks []Chunk
	for _, sec := range sections {
		chunks = append(chunks, chunkSection(sec, len(chunks), cfg, doc.Tags)...)
	}
	return chunks
}

// chunkMetadata builds the JSONB metadata attached to a stored chunk.
//
// Args:
//
//	c   - the chunk being stored.
//	doc - the source document, used for title and file path.
//
// Returns:
//
//	a metadata map with title, heading breadcrumb, level, block types and tags.
func chunkMetadata(c Chunk, doc *Document) map[string]any {
	heading := ""
	if n := len(c.HeadingPath); n > 0 {
		heading = c.HeadingPath[n-1]
	}
	return map[string]any{
		"title":        doc.Title,
		"heading":      heading,
		"heading_path": strings.Join(c.HeadingPath, " > "),
		"level":        c.Level,
		"block_types":  c.BlockTypes,
		"tags":         c.Tags,
		"file":         doc.Path,
	}
}

// splitSections groups blocks into heading-delimited sections, tracking the
// heading breadcrumb via a level-ordered stack.
func splitSections(doc *Document) []section {
	var sections []section
	var stack []Block
	cur := section{path: []string{doc.Title}, level: 0}

	flush := func() {
		if len(cur.blocks) > 0 {
			sections = append(sections, cur)
		}
	}

	for _, b := range doc.Blocks {
		if b.Type != BlockHeading {
			cur.blocks = append(cur.blocks, b)
			continue
		}
		flush()
		for len(stack) > 0 && stack[len(stack)-1].Level >= b.Level {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, b)
		cur = section{path: headingPath(doc.Title, stack), level: b.Level, blocks: []Block{b}}
	}
	flush()
	return sections
}

// headingPath builds a breadcrumb from the document title and heading stack.
// The title is prepended only when it is not already the top heading, so a
// document whose title comes from its H1 is not duplicated in the breadcrumb.
func headingPath(title string, stack []Block) []string {
	path := make([]string, 0, len(stack)+1)
	if len(stack) == 0 || stack[0].Title != title {
		path = append(path, title)
	}
	for _, h := range stack {
		path = append(path, h.Title)
	}
	return path
}

// chunkSection renders a section into one or more chunks, splitting only when
// the section exceeds the configured size.
func chunkSection(sec section, idxStart int, cfg KnowledgeConfig, tags []string) []Chunk {
	groups := groupBlocks(sec.blocks, cfg.ChunkSize)
	heading := sectionHeading(sec)
	chunks := make([]Chunk, 0, len(groups))

	for gi, group := range groups {
		content := renderBlocks(group)
		if gi > 0 && heading != "" {
			content = truncateRunes(heading, cfg.ChunkOverlap) + "\n\n" + content
		}
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Index:       idxStart + len(chunks),
			Content:     content,
			HeadingPath: sec.path,
			Level:       sec.level,
			BlockTypes:  blockTypes(group),
			Tags:        tags,
		})
	}
	return chunks
}

// groupBlocks packs blocks greedily into groups bounded by maxChars, returning
// contiguous sub-slices of the input. A single block larger than maxChars
// becomes its own group and is never split.
func groupBlocks(blocks []Block, maxChars int) [][]Block {
	if len(blocks) == 0 {
		return nil
	}

	var groups [][]Block
	start := 0
	curLen := 0

	for i, b := range blocks {
		blockLen := len(b.Text) + 2
		if i > start && curLen+blockLen > maxChars {
			groups = append(groups, blocks[start:i])
			start = i
			curLen = 0
		}
		curLen += blockLen
	}
	groups = append(groups, blocks[start:])
	return groups
}

// sectionHeading returns the heading line for a section, or the last breadcrumb
// element when the section has no leading heading block.
func sectionHeading(sec section) string {
	if len(sec.blocks) > 0 && sec.blocks[0].Type == BlockHeading {
		return sec.blocks[0].Text
	}
	if len(sec.path) > 0 {
		return sec.path[len(sec.path)-1]
	}
	return ""
}

// renderBlocks joins block texts with blank-line separators.
func renderBlocks(blocks []Block) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n")
}

// blockTypes returns the distinct block types in a group, preserving order.
func blockTypes(blocks []Block) []string {
	seen := make(map[BlockType]struct{}, len(blocks))
	var types []string
	for _, b := range blocks {
		if _, ok := seen[b.Type]; ok {
			continue
		}
		seen[b.Type] = struct{}{}
		types = append(types, string(b.Type))
	}
	return types
}

// truncateRunes returns s truncated to at most n runes, avoiding cutting a
// multibyte character in half.
func truncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

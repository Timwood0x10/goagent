// This file implements the line-oriented block scanner used by the parser.
// It walks a document's body lines once and groups them into typed blocks,
// keeping fenced code blocks and tables intact.
package main

import "strings"

// blockScanner walks body lines and produces typed blocks.
type blockScanner struct {
	lines []string
	pos   int
}

// scan consumes all lines and returns the ordered list of blocks.
func (s *blockScanner) scan() []Block {
	var blocks []Block
	for s.pos < len(s.lines) {
		line := s.lines[s.pos]
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			s.pos++
		case isFence(trimmed):
			blocks = append(blocks, s.consumeCode())
		case isHeading(line):
			blocks = append(blocks, s.consumeHeading())
		case s.isTableStart():
			blocks = append(blocks, s.consumeTable())
		case listItemRe.MatchString(line):
			blocks = append(blocks, s.consumeList())
		case quoteRe.MatchString(line):
			blocks = append(blocks, s.consumeQuote())
		default:
			blocks = append(blocks, s.consumeParagraph())
		}
	}
	return blocks
}

// consumeHeading reads a single ATX heading line.
func (s *blockScanner) consumeHeading() Block {
	line := s.lines[s.pos]
	s.pos++
	m := headingRe.FindStringSubmatch(line)
	// m is guaranteed non-nil: the caller checked isHeading first.
	return Block{
		Type:  BlockHeading,
		Level: len(m[1]),
		Title: strings.TrimSpace(m[2]),
		Text:  line,
	}
}

// consumeCode reads a fenced code block, preserving fences and inner content.
func (s *blockScanner) consumeCode() Block {
	open := s.lines[s.pos]
	m := fenceRe.FindStringSubmatch(strings.TrimSpace(open))
	fenceChar := "`"
	lang := ""
	if m != nil {
		fenceChar = m[1][:1]
		lang = m[2]
	}

	var b strings.Builder
	b.WriteString(open)
	b.WriteString("\n")
	s.pos++

	for s.pos < len(s.lines) {
		cur := s.lines[s.pos]
		b.WriteString(cur)
		b.WriteString("\n")
		s.pos++
		if isClosingFence(strings.TrimSpace(cur), fenceChar) {
			break
		}
	}
	return Block{Type: BlockCode, Lang: lang, Text: strings.TrimRight(b.String(), "\n")}
}

// isTableStart reports whether the current line begins a GFM table, i.e. a row
// containing a pipe immediately followed by a separator row.
func (s *blockScanner) isTableStart() bool {
	line := s.lines[s.pos]
	if !strings.Contains(line, "|") {
		return false
	}
	if s.pos+1 >= len(s.lines) {
		return false
	}
	return tableSepRe.MatchString(s.lines[s.pos+1])
}

// consumeTable reads consecutive pipe-delimited rows as one table block.
func (s *blockScanner) consumeTable() Block {
	var b strings.Builder
	for s.pos < len(s.lines) {
		line := s.lines[s.pos]
		if strings.TrimSpace(line) == "" || !strings.Contains(line, "|") {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		s.pos++
	}
	return Block{Type: BlockTable, Text: strings.TrimRight(b.String(), "\n")}
}

// consumeList reads a list and its indented continuation lines.
func (s *blockScanner) consumeList() Block {
	var b strings.Builder
	for s.pos < len(s.lines) {
		line := s.lines[s.pos]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isHeading(line) || isFence(trimmed) {
			break
		}
		if !listItemRe.MatchString(line) && !isIndented(line) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		s.pos++
	}
	return Block{Type: BlockList, Text: strings.TrimRight(b.String(), "\n")}
}

// consumeQuote reads consecutive blockquote lines.
func (s *blockScanner) consumeQuote() Block {
	var b strings.Builder
	for s.pos < len(s.lines) {
		line := s.lines[s.pos]
		if !quoteRe.MatchString(line) {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		s.pos++
	}
	return Block{Type: BlockQuote, Text: strings.TrimRight(b.String(), "\n")}
}

// consumeParagraph reads a run of plain text lines until a structural boundary.
func (s *blockScanner) consumeParagraph() Block {
	var b strings.Builder
	for s.pos < len(s.lines) {
		line := s.lines[s.pos]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isHeading(line) || isFence(trimmed) ||
			listItemRe.MatchString(line) || quoteRe.MatchString(line) || s.isTableStart() {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		s.pos++
	}
	return Block{Type: BlockParagraph, Text: strings.TrimRight(b.String(), "\n")}
}

// isHeading reports whether a line is an ATX heading.
func isHeading(line string) bool {
	return headingRe.MatchString(line)
}

// isFence reports whether a trimmed line opens or closes a code fence.
func isFence(trimmed string) bool {
	return fenceRe.MatchString(trimmed)
}

// isClosingFence reports whether a trimmed line is a pure fence of fenceChar.
func isClosingFence(trimmed, fenceChar string) bool {
	if len(trimmed) < 3 {
		return false
	}
	for _, r := range trimmed {
		if string(r) != fenceChar {
			return false
		}
	}
	return true
}

// isIndented reports whether a line is an indented list continuation.
func isIndented(line string) bool {
	return strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")
}

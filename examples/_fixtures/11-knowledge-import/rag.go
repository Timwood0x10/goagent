// This file implements retrieval-augmented question answering over the stored
// knowledge base: retrieve the most relevant chunks, assemble a grounded
// prompt, and generate an answer. Without an LLM it degrades to returning the
// retrieved passages directly.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Timwood0x10/ares/internal/storage/postgres/services"
)

// snippetRunes bounds how much of a chunk is shown as a citation snippet.
const snippetRunes = 240

// Source is a single retrieved passage used to ground an answer.
type Source struct {
	Rank    int     // One-based citation index.
	Score   float64 // Similarity score in [0, 1].
	Path    string  // Source file path.
	Snippet string  // Truncated content preview.
}

// Answer is the result of a RAG query.
type Answer struct {
	Question  string   // The original question.
	Text      string   // The generated or fallback answer text.
	Sources   []Source // Retrieved passages, in rank order.
	Generated bool     // True when produced by the LLM, false for fallbacks.
}

// Ask answers a question using retrieval-augmented generation.
//
// Args:
//
//	ctx      - cancellation context.
//	tenantID - tenant namespace to search, must be non-empty.
//	question - the user question, must be non-empty.
//
// Returns:
//
//	answer - the answer with cited sources, never nil on success.
//	err    - a retrieval or generation error with context.
func (kb *KnowledgeBase) Ask(ctx context.Context, tenantID, question string) (*Answer, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, wrapf(os.ErrInvalid, "ask: empty tenant id")
	}
	if strings.TrimSpace(question) == "" {
		return nil, wrapf(os.ErrInvalid, "ask: empty question")
	}

	results, err := kb.retrieval.Search(ctx, tenantID, question)
	if err != nil {
		return nil, wrapf(err, "retrieve for question")
	}

	sources := toSources(results)
	if len(sources) == 0 {
		return &Answer{
			Question:  question,
			Text:      "No relevant information was found in the knowledge base.",
			Generated: false,
		}, nil
	}

	if kb.llmClient == nil {
		return &Answer{
			Question:  question,
			Text:      retrievalOnlyAnswer(sources),
			Sources:   sources,
			Generated: false,
		}, nil
	}

	prompt := buildPrompt(question, results)
	text, err := kb.llmClient.Generate(ctx, prompt)
	if err != nil {
		return nil, wrapf(err, "generate answer")
	}
	return &Answer{
		Question:  question,
		Text:      strings.TrimSpace(text),
		Sources:   sources,
		Generated: true,
	}, nil
}

// toSources converts retrieval results into display-ready sources.
func toSources(results []*services.SimpleSearchResult) []Source {
	sources := make([]Source, 0, len(results))
	for i, r := range results {
		if r == nil {
			continue
		}
		sources = append(sources, Source{
			Rank:    i + 1,
			Score:   r.Score,
			Path:    r.Source,
			Snippet: truncateRunes(strings.TrimSpace(r.Content), snippetRunes),
		})
	}
	return sources
}

// retrievalOnlyAnswer joins the retrieved passages when no LLM is configured.
func retrievalOnlyAnswer(sources []Source) string {
	var b strings.Builder
	b.WriteString("Retrieval-only mode (no LLM configured). Top passages:\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "\n[%d] (score %.3f) %s\n%s\n", s.Rank, s.Score, s.Path, s.Snippet)
	}
	return b.String()
}

// buildPrompt assembles a grounded prompt instructing the model to answer only
// from the provided context and to cite passages by their bracketed index.
func buildPrompt(question string, results []*services.SimpleSearchResult) string {
	var b strings.Builder
	b.WriteString("You are a knowledge base assistant. Answer the question using ")
	b.WriteString("only the numbered context below. Cite sources as [n]. ")
	b.WriteString("If the context is insufficient, say so explicitly.\n\n")
	b.WriteString("Context:\n")
	for i, r := range results {
		if r == nil {
			continue
		}
		fmt.Fprintf(&b, "[%d] source: %s\n%s\n\n", i+1, r.Source, strings.TrimSpace(r.Content))
	}
	b.WriteString("Question: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nAnswer:")
	return b.String()
}

package context

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// retrieveHelperSharedLimit caps parallel Retrieve calls so a large set of
// retrievers cannot exhaust the embedder / AKG backend. The limit is
// conservative on purpose: retrieval runs on the hot chat-loop path.
const retrieveHelperSharedLimit = 4

// RetrieveAll calls every retriever in parallel (ctx-cancelable via errgroup,
// code_rules §4.5) and merges their results.
//
// Retrievers are invoked concurrently with a shared concurrency limit. A
// failure in any single retriever is logged via the returned error chain and
// does NOT cancel the others — retrieval is best-effort by design. The
// returned slice is the concatenation of all non-error results, deduplicated
// by Source+Content (keeping the highest Score) and sorted by Score
// descending.
//
// Args:
//
//	ctx        - operation context, honoured for cancellation and timeout.
//	retrievers - list of retrievers to query. Nil/empty yields an empty slice.
//	input      - query text. Empty input yields an empty slice (no retriever
//	             is called).
//	topK       - per-retriever limit. <= 0 is normalized to 5.
//	minScore   - global minimum Score filter applied after merge. < 0 is
//	             treated as 0 (no filter).
//
// Returns:
//
//	[]ContextSnippet - merged, deduplicated, sorted snippets.
//	error            - joined error of all retriever failures, or nil when
//	                   every retriever succeeded. A non-nil error does NOT
//	                   imply an empty result: partial results are returned.
func RetrieveAll(
	ctx context.Context,
	retrievers []ContextRetriever,
	input string,
	topK int,
	minScore float64,
) ([]ContextSnippet, error) {
	if len(retrievers) == 0 || input == "" {
		return []ContextSnippet{}, nil
	}
	if topK <= 0 {
		topK = DefaultTopK
	}
	if minScore < 0 {
		minScore = 0
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(retrieveHelperSharedLimit)

	type retrieverResult struct {
		snippets []ContextSnippet
		err      error
	}
	results := make([]retrieverResult, len(retrievers))

	for i, r := range retrievers {
		g.Go(func() error {
			snippets, err := r.Retrieve(gCtx, input, topK)
			results[i] = retrieverResult{snippets: snippets, err: err}
			return nil // never return err here: best-effort, do not cancel siblings
		})
	}
	_ = g.Wait()

	// Merge: collect all snippets, track errors.
	var all []ContextSnippet
	var errs []string
	for _, res := range results {
		if res.err != nil {
			errs = append(errs, res.err.Error())
			continue
		}
		all = append(all, res.snippets...)
	}

	// Dedup, filter, sort.
	all = DedupSnippets(all)
	all = filterByMinScore(all, minScore)
	SortSnippetsByScore(all)

	if len(all) > topK {
		all = all[:topK]
	}

	if len(errs) > 0 {
		return all, fmt.Errorf("retrieve helper: %d retriever(s) failed: %s",
			len(errs), strings.Join(errs, "; "))
	}
	return all, nil
}

// FormatSnippetsAsContext renders a slice of ContextSnippet as a flat string
// suitable for injection into BuildContext's text output.
//
// The format is:
//
//	Relevant context:
//	[experience] Problem: ... Solution: ...
//	[knowledge] <summary or normalized text>
//
// Snippets are rendered in the given order (callers should pre-sort by Score).
// An empty input slice returns an empty string.
func FormatSnippetsAsContext(snippets []ContextSnippet) string {
	if len(snippets) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(len(snippets) * 256)
	b.WriteString("Relevant context:\n")
	for _, s := range snippets {
		fmt.Fprintf(&b, "[%s] %s\n", s.Source, s.Content)
	}
	return b.String()
}

// SnippetsToSystemMessages converts a slice of ContextSnippet into a single
// system Message suitable for prepending to a prompt. Returns nil when the
// input is empty so callers can skip the injection entirely.
func SnippetsToSystemMessages(snippets []ContextSnippet) []Message {
	if len(snippets) == 0 {
		return nil
	}
	content := FormatSnippetsAsContext(snippets)
	return []Message{{Role: RoleSystem, Content: content}}
}

// RunRetrieval is the shared entry point used by memory managers to execute
// RAG retrieval with config-driven defaults. It centralizes the default
// normalization (topK<=0 ⇒ DefaultTopK, minScore<=0 ⇒ DefaultMinScore) so the
// two ProductionMemoryManager / memoryManager implementations do not each
// re-declare the same magic numbers.
//
// Unlike RetrieveAll (which treats minScore<=0 as "no filter" / 0), this
// helper applies the DefaultMinScore threshold when the caller does not
// configure one explicitly — matching the prior per-manager behaviour while
// removing the duplicated literals.
//
// Retrieval is best-effort: a non-nil error means one or more retrievers
// failed, but partial snippets are still returned. Callers log the error and
// proceed with whatever snippets are available.
//
// Args:
//
//	ctx        - operation context, honoured for cancellation and timeout.
//	retrievers - retrievers to query. Nil/empty yields nil with no error.
//	input      - query text. Empty yields nil with no error.
//	topK       - per-retriever + global cap. <= 0 ⇒ DefaultTopK.
//	minScore   - global minimum Score filter. <= 0 ⇒ DefaultMinScore.
//
// Returns:
//
//	[]ContextSnippet - merged, deduplicated, sorted snippets (possibly empty).
//	error            - joined error of retriever failures, or nil.
func RunRetrieval(
	ctx context.Context,
	retrievers []ContextRetriever,
	input string,
	topK int,
	minScore float64,
) ([]ContextSnippet, error) {
	if len(retrievers) == 0 || input == "" {
		return nil, nil
	}
	if topK <= 0 {
		topK = DefaultTopK
	}
	if minScore <= 0 {
		minScore = DefaultMinScore
	}
	return RetrieveAll(ctx, retrievers, input, topK, minScore)
}

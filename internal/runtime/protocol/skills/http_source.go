package ares_skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPSource is a declared remote skill source whose manifest is fetched over
// HTTP(S) (config.toml `[[skill_sources]] type = "http"`). The manifest is the
// only thing fetched — a declared list, never a directory scan. OCI sources
// use the same manifest interface (an OCI artifact that wraps the same JSON).
type HTTPSource struct {
	// URL is the manifest endpoint returning an HTTPManifest JSON document.
	URL string `toml:"url"`
}

// HTTPManifestSkill is one skill entry inside a remote manifest. Fields map
// 1:1 onto SkillIndexEntry metadata (Level-0 only; bodies stay remote).
type HTTPManifestSkill struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Keywords     []string `json:"keywords"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	ToolTypes    []string `json:"tool_types"`
}

// HTTPManifest is the remote skill manifest document.
type HTTPManifest struct {
	// Skills is the declared skill list.
	Skills []HTTPManifestSkill `json:"skills"`
}

// httpClient is overridable for tests (local httptest servers).
var httpClient = &http.Client{Timeout: 10 * time.Second}

// FetchHTTPManifest fetches and decodes a remote skill manifest, mapping it
// onto metadata-only index entries (Source=registered). A fetch failure is
// returned so the caller can skip the source without failing the whole index.
//
// Args:
//   - ctx: context for cancellation.
//   - src: the declared HTTP source.
//
// Returns:
//   - []SkillIndexEntry: metadata entries declared by the manifest.
//   - error: wrapped fetch/decode error, or nil.
func FetchHTTPManifest(ctx context.Context, src HTTPSource) ([]SkillIndexEntry, error) {
	if src.URL == "" {
		return nil, errors.New("ares_skills: http source needs url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("ares_skills: http request %s: %w", src.URL, err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ares_skills: http fetch %s: %w", src.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("ares_skills: http status %d from %s", resp.StatusCode, src.URL)
	}
	var manifest HTTPManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("ares_skills: decode manifest %s: %w", src.URL, err)
	}
	entries := make([]SkillIndexEntry, 0, len(manifest.Skills))
	for _, s := range manifest.Skills {
		if s.ID == "" {
			continue
		}
		name := s.Name
		if name == "" {
			name = s.ID
		}
		desc := s.Description
		if desc == "" {
			desc = s.ID
		}
		entries = append(entries, SkillIndexEntry{
			ID:          s.ID,
			Name:        name,
			Description: desc,
			Keywords:    s.Keywords,
			Source:      SourceRegistered,
			// P1-8: Set Path to the manifest URL so Load() can detect remote
			// skills and return a clear error instead of trying to read
			// SKILL.md from CWD (which produced a silent wrong-file load).
			Path:         src.URL,
			Version:      s.Version,
			Capabilities: s.Capabilities,
			ToolTypes:    s.ToolTypes,
			Hash:         s.ID + ":" + s.Version,
		})
	}
	return entries, nil
}

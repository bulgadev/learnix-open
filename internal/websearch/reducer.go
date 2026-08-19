package websearch

import (
	"sort"
	"strings"
	"unicode"
)

const (
	// RawExtractLimitRunes protects the application before the reducer runs.
	RawExtractLimitRunes = 20000
	// ReducedExtractLimitRunes is the maximum page content sent to the model.
	ReducedExtractLimitRunes = 6000
	contentChunkLimitRunes   = 1200
)

// ContentReducer keeps the relevant parts of readable web content while
// preserving their original order. It is intentionally deterministic: it
// saves prompt tokens without adding another model call or another failure
// point to the research pipeline.
type ContentReducer struct {
	MaxRunes   int
	ChunkRunes int
}

// DefaultContentReducer is the stable reducer used by Tavily extraction.
var DefaultContentReducer = ContentReducer{
	MaxRunes:   ReducedExtractLimitRunes,
	ChunkRunes: contentChunkLimitRunes,
}

// Reduce normalizes content and selects the most relevant blocks for focus.
// With an empty focus it keeps the beginning of the cleaned document, which
// is the safest fallback for callers that do not have the user's query.
func (r ContentReducer) Reduce(content, focus string) string {
	if r.MaxRunes <= 0 || r.ChunkRunes <= 0 {
		return ""
	}
	clean := normalizeContent(content)
	if clean == "" {
		return ""
	}
	if len([]rune(clean)) <= r.MaxRunes {
		return clean
	}
	if strings.TrimSpace(focus) == "" {
		return truncateRunes(clean, r.MaxRunes)
	}

	chunks := splitContent(clean, r.ChunkRunes)
	if len(chunks) == 0 {
		return truncateRunes(clean, r.MaxRunes)
	}
	terms := contentTerms(focus)
	type scoredChunk struct {
		index int
		text  string
		score int
	}
	scored := make([]scoredChunk, 0, len(chunks))
	for i, chunk := range chunks {
		scored = append(scored, scoredChunk{index: i, text: chunk, score: scoreChunk(chunk, terms)})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].index < scored[j].index
	})

	selected := make(map[int]bool)
	used := 0
	hasRelevant := scored[0].score > 0
	for _, chunk := range scored {
		if used >= r.MaxRunes {
			break
		}
		if hasRelevant && chunk.score == 0 {
			break
		}
		chunkRunes := len([]rune(chunk.text))
		remaining := r.MaxRunes - used
		if chunkRunes > remaining {
			if remaining > 0 {
				selected[chunk.index] = true
			}
			break
		}
		selected[chunk.index] = true
		used += chunkRunes
	}

	ordered := make([]scoredChunk, 0, len(selected))
	for _, chunk := range scored {
		if selected[chunk.index] {
			ordered = append(ordered, chunk)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].index < ordered[j].index })
	parts := make([]string, 0, len(ordered))
	for _, chunk := range ordered {
		parts = append(parts, chunk.text)
	}
	return truncateRunes(strings.Join(parts, "\n\n"), r.MaxRunes)
}

func normalizeContent(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(clean) == 0 || clean[len(clean)-1] == "" {
				continue
			}
			clean = append(clean, "")
			continue
		}
		clean = append(clean, strings.Join(strings.Fields(line), " "))
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}

func splitContent(content string, limit int) []string {
	paragraphs := strings.Split(content, "\n\n")
	chunks := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		runes := []rune(paragraph)
		for len(runes) > limit {
			cut := limit
			for i := limit; i > limit/2; i-- {
				if unicode.IsSpace(runes[i-1]) {
					cut = i
					break
				}
			}
			chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
			runes = []rune(strings.TrimSpace(string(runes[cut:])))
		}
		if len(runes) > 0 {
			chunks = append(chunks, string(runes))
		}
	}
	return chunks
}

func contentTerms(focus string) []string {
	seen := make(map[string]bool)
	var terms []string
	var word []rune
	flush := func() {
		if len(word) < 3 {
			word = nil
			return
		}
		term := strings.ToLower(string(word))
		if !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
		word = nil
	}
	for _, r := range strings.ToLower(focus) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			word = append(word, r)
		} else {
			flush()
		}
	}
	flush()
	return terms
}

func scoreChunk(chunk string, terms []string) int {
	if len(terms) == 0 {
		return 0
	}
	lower := strings.ToLower(chunk)
	score := 0
	for _, term := range terms {
		occurrences := strings.Count(lower, term)
		if occurrences > 0 {
			score += occurrences
		}
	}
	return score
}

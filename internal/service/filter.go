package service

import "strings"

type FilterResult struct {
	Pass    bool
	Reason  string
	Replace string // Replacement content (for partial filter)
}

// ContentFilter checks message content before delivery.
type ContentFilter interface {
	Check(uid int64, content string) FilterResult
}

// FilterChain applies multiple filters in order. Stops on first rejection.
type FilterChain struct {
	filters []ContentFilter
}

func NewFilterChain(filters ...ContentFilter) *FilterChain {
	return &FilterChain{filters: filters}
}

func (c *FilterChain) Check(uid int64, content string) FilterResult {
	for _, f := range c.filters {
		result := f.Check(uid, content)
		if !result.Pass {
			return result
		}
		if result.Replace != "" {
			content = result.Replace
		}
	}
	return FilterResult{Pass: true, Replace: content}
}

// DirtyWordFilter replaces forbidden words with asterisks.
type DirtyWordFilter struct {
	words []string
}

func NewDirtyWordFilter(words []string) *DirtyWordFilter {
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	return &DirtyWordFilter{words: lower}
}

func (f *DirtyWordFilter) Check(_ int64, content string) FilterResult {
	lowerContent := strings.ToLower(content)
	replaced := content
	for _, word := range f.words {
		if strings.Contains(lowerContent, word) {
			mask := strings.Repeat("*", len(word))
			replaced = replaceIgnoreCase(replaced, word, mask)
			lowerContent = strings.ToLower(replaced)
		}
	}
	return FilterResult{Pass: true, Replace: replaced}
}

func replaceIgnoreCase(s, old, replacement string) string {
	lower := strings.ToLower(s)
	lowerOld := strings.ToLower(old)
	var result strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(lower[i:], lowerOld)
		if idx == -1 {
			result.WriteString(s[i:])
			break
		}
		result.WriteString(s[i : i+idx])
		result.WriteString(replacement)
		i += idx + len(old)
	}
	return result.String()
}

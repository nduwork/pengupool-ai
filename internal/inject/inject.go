// Package inject chooses Compass lines for a context. Plain code, no
// model call: score is word overlap between the context and the rule,
// times confidence. Mirrors pengu/inject.py.
package inject

import (
	"regexp"
	"sort"
	"strings"

	"github.com/nduwork/pengupool-ai/internal/compass"
)

// MinConfidence floors injection: contradicted lines never inject.
const MinConfidence = 0.3

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be but by did do does for from had has have
in is it its of on or that the this to was with you your`) {
		stop[w] = true
	}
}

var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)

// Terms of a text: lowercased content words with crude plurals stripped,
// on both sides alike. Function words and stubs carry no topic.
func Terms(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if stop[w] || len(w) < 4 {
			continue
		}
		if strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 4 {
			w = w[:len(w)-1]
		}
		out[w] = true
	}
	return out
}

// Confidence of a line, defaulting to 1.0.
func Confidence(l *compass.Line) float64 {
	if l == nil {
		return 1.0
	}
	c := l.Confidence
	if c != c || c < 0 || c > 1 {
		return 1.0
	}
	return c
}

// Cap keeps the first k items within the character budget; the first one
// is always kept.
func Cap(items []*compass.Line, k, budget int) []*compass.Line {
	if k < 0 {
		k = 0
	}
	if k < len(items) {
		items = items[:k]
	}
	out := []*compass.Line{}
	size := 0
	for _, l := range items {
		size += len(l.Rule)
		if size > budget && len(out) > 0 {
			break
		}
		out = append(out, l)
	}
	return out
}

// HelperHint tells helpers to pull Compass and experience at job start.
const HelperHint = "When you get a job, first call compass_context for the topic " +
	"and experience_for for your helper name: the Compass lines and " +
	"the lessons of earlier tasks."

// Select returns relevant lines first, capped. A non-empty context with no
// usable terms matches nothing; an empty context ranks everything above
// the floor by confidence.
func Select(lines []*compass.Line, context string, k, budget int) []*compass.Line {
	ctx := Terms(context)
	if strings.TrimSpace(context) != "" && len(ctx) == 0 {
		return nil
	}
	type scored struct {
		score float64
		conf  float64
		line  *compass.Line
	}
	var ss []scored
	for _, l := range lines {
		conf := Confidence(l)
		if conf < MinConfidence {
			continue
		}
		if len(ctx) == 0 {
			ss = append(ss, scored{0, conf, l})
			continue
		}
		overlap := 0
		lt := Terms(l.Rule)
		for w := range ctx {
			if lt[w] {
				overlap++
			}
		}
		if overlap == 0 {
			continue
		}
		ss = append(ss, scored{float64(overlap) * conf, conf, l})
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].score != ss[j].score {
			return ss[i].score > ss[j].score
		}
		return ss[i].conf > ss[j].conf
	})
	out := make([]*compass.Line, len(ss))
	for i, s := range ss {
		out[i] = s.line
	}
	return Cap(out, k, budget)
}

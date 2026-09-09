package main

// suggest returns the candidate closest to name by edit distance, or "" if
// none is close enough to be worth a hint. This is a typo hint, not a spell
// checker, so it deliberately stays simple: plain Levenshtein distance with
// a threshold scaled to the word length.
func suggest(name string, candidates []string) string {
	best := ""
	bestDist := -1
	for _, c := range candidates {
		d := levenshtein(name, c)
		if bestDist == -1 || d < bestDist {
			bestDist = d
			best = c
		}
	}
	if best == "" || !closeEnough(name, best, bestDist) {
		return ""
	}
	return best
}

// closeEnough caps the distance suggest accepts: a third of the longer
// word's length, at least 1 and at most 3, so "aip" suggests "api" but
// "validate" does not suggest "call".
func closeEnough(a, b string, dist int) bool {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	limit := n / 3
	if limit < 1 {
		limit = 1
	}
	if limit > 3 {
		limit = 3
	}
	return dist <= limit
}

// levenshtein is the edit distance between two strings (insert, delete,
// substitute, each cost 1).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	m, n := len(ra), len(rb)
	prev := make([]int, n+1)
	cur := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		cur[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			min := prev[j] + 1 // delete
			if ins := cur[j-1] + 1; ins < min {
				min = ins
			}
			if sub := prev[j-1] + cost; sub < min {
				min = sub
			}
			cur[j] = min
		}
		prev, cur = cur, prev
	}
	return prev[n]
}

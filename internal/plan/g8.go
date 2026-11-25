package plan

import (
	"fmt"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// g8ImportGraph validates that cross-slice import edges in deps are ordered by depends_on.
func g8ImportGraph(p contract.Plan, reach [][]bool, deps []contract.DepsEdge) ([]string, string) {
	if deps == nil {
		return nil, "not checked: it needs the engines' deps_graph, which `a2dlc plan check` does not run yet"
	}
	if len(deps) == 0 {
		return nil, ""
	}
	var bad []string
	seen := map[string]bool{}
	for _, edge := range deps {
		var ownersFrom, ownersTo []int
		for i, s := range p.Slices {
			if matchAny(s.Owns, edge.From) {
				ownersFrom = append(ownersFrom, i)
			}
			if matchAny(s.Owns, edge.To) {
				ownersTo = append(ownersTo, i)
			}
		}
		for _, i := range ownersFrom {
			for _, j := range ownersTo {
				if i == j {
					continue
				}
				// edge.From imports edge.To => slice i depends on slice j (reach[i][j] must be true).
				if !reach[i][j] {
					msg := fmt.Sprintf("slice %s owns %s which imports %s (owned by %s), but %s does not depend on %s",
						p.Slices[i].SliceID, edge.From, edge.To, p.Slices[j].SliceID, p.Slices[i].SliceID, p.Slices[j].SliceID)
					if !seen[msg] {
						seen[msg] = true
						bad = append(bad, msg)
					}
				}
			}
		}
	}
	return bad, ""
}

// globsIntersect reports whether two path globs can match at least one common path.
func globsIntersect(a, b string) bool {
	sa := strings.Split(a, "/")
	sb := strings.Split(b, "/")
	memo := map[[2]int]bool{}
	vis := map[[2]int]bool{}
	var dp func(i, j int) bool
	dp = func(i, j int) bool {
		k := [2]int{i, j}
		if vis[k] {
			return memo[k]
		}
		vis[k] = true
		var res bool
		switch {
		case i == len(sa) && j == len(sb):
			res = true
		case i < len(sa) && sa[i] == "**":
			res = dp(i+1, j) || (j < len(sb) && dp(i, j+1))
		case j < len(sb) && sb[j] == "**":
			res = dp(i, j+1) || (i < len(sa) && dp(i+1, j))
		case i < len(sa) && j < len(sb):
			res = segIntersect(sa[i], sb[j]) && dp(i+1, j+1)
		}
		memo[k] = res
		return res
	}
	return dp(0, 0)
}

// segIntersect reports whether two single-segment globs (supporting * and ?) can match a common string.
func segIntersect(a, b string) bool {
	memo := map[[2]int]bool{}
	vis := map[[2]int]bool{}
	var dp func(i, j int) bool
	dp = func(i, j int) bool {
		k := [2]int{i, j}
		if vis[k] {
			return memo[k]
		}
		vis[k] = true
		var res bool
		switch {
		case i == len(a) && j == len(b):
			res = true
		case i < len(a) && a[i] == '*':
			res = dp(i+1, j) || (j < len(b) && dp(i, j+1))
		case j < len(b) && b[j] == '*':
			res = dp(i, j+1) || (i < len(a) && dp(i+1, j))
		case i < len(a) && j < len(b):
			if a[i] == '?' || b[j] == '?' || a[i] == b[j] {
				res = dp(i+1, j+1)
			}
		}
		memo[k] = res
		return res
	}
	return dp(0, 0)
}

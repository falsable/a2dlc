package app

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// sourceLanguage names the language of a source file by its extension, the way a person would count them. It is a list of
// what install reports, not of what a2dlc can judge: only python and typescript have an engine in this repo.
var sourceLanguage = map[string]string{
	".py": "Python", ".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".go": "Go", ".rs": "Rust", ".java": "Java", ".kt": "Kotlin", ".scala": "Scala", ".rb": "Ruby", ".php": "PHP",
	".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".cxx": "C++", ".hpp": "C++", ".cs": "C#", ".swift": "Swift",
}

// vendoredDirs hold code that is not the repo's own. .gitignore already hides most of them; this covers the repos that
// commit them or have no .gitignore yet.
var vendoredDirs = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "vendor": true, "dist": true, "build": true,
	"target": true, "__pycache__": true, "site-packages": true, ".tox": true, ".git": true, ".a2dlc": true, ".next": true}

// isTestFile is what config.tests gates, not what an engine examines: a src layout leaves tests/ out of the engine's
// paths on purpose, so those files are not a gap.
func isTestFile(rel string) bool {
	base := path.Base(rel)
	for _, dir := range strings.Split(path.Dir(rel), "/") {
		switch dir {
		case "test", "tests", "__tests__":
			return true
		}
	}
	stem := strings.TrimSuffix(base, path.Ext(base))
	return base == "conftest.py" || strings.HasPrefix(base, "test_") || strings.HasSuffix(stem, "_test") ||
		strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
}

// sourceFiles lists the repo's own source files that are not tests, with / separators: by extension, outside the vendored
// and ignored directories. An unreadable listing is no files, so install never fails for want of this report.
func (a App) sourceFiles() []string {
	all, err := a.Repo.Visible()
	if err != nil {
		return nil
	}
	out := []string{}
	for _, f := range all {
		if _, ok := sourceLanguage[path.Ext(f)]; !ok || strings.HasSuffix(f, ".d.ts") || isTestFile(f) {
			continue
		}
		if _, err := os.Stat(a.path(f)); err != nil { // tracked but deleted in the working tree
			continue
		}
		vendored := false
		for _, dir := range strings.Split(path.Dir(f), "/") {
			vendored = vendored || vendoredDirs[dir]
		}
		if !vendored {
			out = append(out, f)
		}
	}
	return out
}

// hasSource says whether some file has one of the extensions; under, whether it lives below dir/.
func hasSource(files []string, under string, exts ...string) bool {
	for _, f := range files {
		for _, e := range exts {
			if path.Ext(f) == e && strings.HasPrefix(f, under) {
				return true
			}
		}
	}
	return false
}

// CoverageWarning says which source files no engine of the config covers, by language, extension and count (a2dlc-fjl).
// A change to them is out of scope for every engine, so a check can only be INCONCLUSIVE about them. It reads the config
// on disk, written by install or by hand, and is false when everything is covered or there is no source at all.
func (a App) CoverageWarning() (string, bool) {
	cfg, err := a.config()
	if err != nil {
		return "", false
	}
	_, without := verdict.Assign(cfg.Engines, a.sourceFiles())
	if len(without) == 0 {
		return "", false
	}
	parts := []string{}
	for _, g := range uncoveredByLanguage(without) {
		parts = append(parts, g.Part)
	}
	return fmt.Sprintf("%s: no engine in %s covers them, so no engine examines a change there and check reports it as out of scope "+
		"(a PASS with 0 targets), not as judged. Add an engine for those paths, or list them under config.critical_code so that "+
		"pre-push blocks a push that leaves them unjudged.",
		strings.Join(parts, "; "), pathConfig), true
}

// languageGap is the source files of one language that no engine covers; Part reads "2 Go files (.go)".
type languageGap struct {
	Language string
	Count    int
	Part     string
}

// uncoveredByLanguage groups files by language, sorted by language, with the count per extension when there are several.
func uncoveredByLanguage(without []string) []languageGap {
	byLang := map[string]map[string]int{}
	for _, f := range without {
		lang := sourceLanguage[path.Ext(f)]
		if byLang[lang] == nil {
			byLang[lang] = map[string]int{}
		}
		byLang[lang][path.Ext(f)]++
	}
	langs := make([]string, 0, len(byLang))
	for l := range byLang {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	gaps := []languageGap{}
	for _, l := range langs {
		exts := make([]string, 0, len(byLang[l]))
		n := 0
		for e, c := range byLang[l] {
			exts = append(exts, e)
			n += c
		}
		sort.Strings(exts)
		label := exts[0]
		if len(exts) > 1 {
			for i, e := range exts {
				exts[i] = fmt.Sprintf("%s: %d", e, byLang[l][e])
			}
			label = strings.Join(exts, ", ")
		}
		noun := "files"
		if n == 1 {
			noun = "file"
		}
		gaps = append(gaps, languageGap{Language: l, Count: n, Part: fmt.Sprintf("%d %s %s (%s)", n, l, noun, label)})
	}
	return gaps
}

// engineSummary names the engines of a config and their paths, for what install reports.
func engineSummary(c contract.Config) string {
	if len(c.Engines) == 0 {
		return "no engine"
	}
	parts := []string{}
	for _, e := range c.Engines {
		parts = append(parts, fmt.Sprintf("engine %s (%s)", e.Language, strings.Join(e.Paths, ", ")))
	}
	return strings.Join(parts, ", ")
}

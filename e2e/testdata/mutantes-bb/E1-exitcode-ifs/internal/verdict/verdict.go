// Package verdict tiene la lógica pura del veredicto: agregar los targets, cruzar waivers y aprobaciones, y elegir
// qué archivos mira cada motor.
package verdict

import (
	"regexp"
	"sort"
	"strings"

	"a2dlc/internal/contract"
)

// Aggregate arma el veredicto: FAIL si algún target FAIL no tiene waiver; si no, INCONCLUSIVE si alguno lo es o hubo
// motores caídos; si no, PASS. Un FAIL con waiver no cuenta, apruebe o no; sin aprobación va a pendientes.
func Aggregate(targets []contract.TargetResult, waived, approved map[string]bool, engineErrors int) (string, []contract.VerdictTarget, []string) {
	out := make([]contract.VerdictTarget, 0, len(targets))
	pendientes := []string{}
	fail, inc := false, engineErrors > 0
	for _, t := range targets {
		w := t.Verdict == contract.FAIL && waived[t.Target]
		switch {
		case t.Verdict == contract.FAIL && !w:
			fail = true
		case t.Verdict == contract.INCONCLUSIVE:
			inc = true
		}
		if w && !approved[t.Target] {
			pendientes = append(pendientes, t.Target)
		}
		out = append(out, contract.VerdictTarget{TargetResult: t, Waived: w})
	}
	sort.Strings(pendientes)
	switch {
	case fail:
		return contract.FAIL, out, pendientes
	case inc:
		return contract.INCONCLUSIVE, out, pendientes
	}
	return contract.PASS, out, pendientes
}

// ExitCode: 0 PASS, 1 FAIL, 2 INCONCLUSIVE.
func ExitCode(v string) int {
	if v == contract.PASS {
		return 0
	}
	if v == contract.FAIL {
		return 1
	}
	return 2
}

// globRe traduce un glob con ** a una regex anclada: ** cruza carpetas, * y ? no.
func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Match dice si la ruta (con /) cae en el glob.
func Match(glob, path string) bool { return globRe(glob).MatchString(path) }

// Assign reparte los archivos entre motores: el primero de la config cuyo glob calza se lo queda. Devuelve los
// archivos por índice de motor y los que no cubre ninguno.
func Assign(engines []contract.EngineConfig, files []string) (map[int][]string, []string) {
	por := map[int][]string{}
	sin := []string{}
	for _, f := range files {
		asignado := false
		for i, e := range engines {
			for _, g := range e.Paths {
				if Match(g, f) {
					por[i] = append(por[i], f)
					asignado = true
					break
				}
			}
			if asignado {
				break
			}
		}
		if !asignado {
			sin = append(sin, f)
		}
	}
	return por, sin
}

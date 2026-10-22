package run

import (
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// engineLanguageOf names the language of the repo's engine for the specifier and the inquisitor (role-payloads: absent
// means python). It is the engine that covers the changed sources, the same rule Stage 1 uses for the capture.
func (m *Machine) engineLanguageOf() (string, error) {
	cfg, err := m.app.Config()
	if err != nil {
		return "", err
	}
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", err
	}
	sources, _, err := m.app.Repo.Changed(m.baseSha, tree)
	if err != nil {
		return "", err
	}
	return engineLanguage(cfg.Engines, sources), nil
}

// engineLanguage is the language of the first engine whose paths cover one of the files. With no file to match, a
// repo with a single engine has only that language to name; with several engines and no match it names none, and the
// schema's default (python) stands.
func engineLanguage(engines []contract.EngineConfig, files []string) string {
	for _, f := range files {
		for _, e := range engines {
			for _, g := range e.Paths {
				if verdict.Match(g, f) {
					return e.Language
				}
			}
		}
	}
	if len(engines) == 1 {
		return engines[0].Language
	}
	return ""
}

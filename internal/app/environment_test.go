package app

import "testing"

func TestMissingFromEnvironment(t *testing.T) {
	repo := map[string]bool{"click": true, "src": true, "types": true}
	inRepo := func(n string) bool { return repo[n] }
	for text, want := range map[string]string{
		"ModuleNotFoundError: No module named 'typing_extensions'":               `module "typing_extensions" is not installed`,
		"E   ModuleNotFoundError: No module named 'yaml.nodes'":                  `module "yaml.nodes" is not installed`,
		"ModuleNotFoundError: No module named 'click.newmodule'":                 "",
		"ImportError: cannot import name 'Thing' from 'click.types'":             "",
		"Error: Cannot find module 'jsdom'":                                      `package "jsdom" is not installed`,
		"Error: Cannot find module '@testing-library/dom'":                       `package "@testing-library/dom" is not installed`,
		"Error: Cannot find module './newfile'":                                  "",
		"Cannot find package 'types' imported from /work/a.js":                   "",
		"FileNotFoundError: [Errno 2] No such file or directory: 'node'":         `executable "node" is not on the PATH`,
		"FileNotFoundError: [Errno 2] No such file or directory: 'fixture.json'": "",
		"FileNotFoundError: [Errno 2] No such file or directory: 'tests/data/a'": "",
		`exec: "git": executable file not found in $PATH`:                        `executable "git" is not on the PATH`,
		"Error: spawn node ENOENT":                                               `executable "node" is not on the PATH`,
		"sh: 1: node: not found":                                                 `executable "node" is not on the PATH`,
		"bash: node: command not found":                                          `executable "node" is not on the PATH`,
		"KeyError: 'id': not found":                                              "",
		"AssertionError: assert 'user' == 'admin'\n  expected: not found":        "",
	} {
		if got := missingFromEnvironment(text, inRepo); got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

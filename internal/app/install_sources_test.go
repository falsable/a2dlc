package app

import "testing"

func TestIsTestFileRecognizesPHPUnitNames(t *testing.T) {
	for rel, want := range map[string]bool{
		"src/FooTest.php":     true,
		"FooTest.php":         true,
		"tests/Foo.php":       true,
		"src/Foo.php":         false,
		"src/Test.php":        true,
		"src/Contest.php":     false,
		"src/FooTest.php.bak": false,
	} {
		if got := isTestFile(rel); got != want {
			t.Errorf("isTestFile(%q) = %v, want %v", rel, got, want)
		}
	}
}

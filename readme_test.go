package scionic_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	scionic "github.com/HORNET-Storage/Scionic-Merkle-Tree/v2"
)

// The README is held to the module, so that it cannot fall behind it: the
// version it names, the Go code it shows, the programs it tells a reader to
// run, and the files it points to. Its tables of branch sizes are held to
// the program that measures them, by that program's own tests.

func readme(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	// Line endings are whatever the checkout gave.
	return strings.ReplaceAll(string(content), "\r\n", "\n")
}

func TestTheReadmeNamesThisVersion(t *testing.T) {
	text := readme(t)
	for _, want := range []string{
		"version-" + scionic.Version + "-",
		"version " + scionic.Version,
		"What's New in " + scionic.Version,
		"/v2@v" + scionic.Version,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the README does not say %q", want)
		}
	}
	// The oldest Go the module supports is the one that go.mod names.
	module, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	named := regexp.MustCompile(`(?m)^go (\d+\.\d+)`).FindSubmatch(module)
	if named == nil {
		t.Fatal("go.mod names no version of Go")
	}
	oldest := string(named[1])
	for _, want := range []string{"go-" + oldest + "%2B", "| Go | " + oldest + " or newer |", "on Go " + oldest + ","} {
		if !strings.Contains(text, want) {
			t.Fatalf("the README does not say %q", want)
		}
	}
}

func TestEveryProgramTheReadmeRunsIsThere(t *testing.T) {
	text := readme(t)
	// Each `go run ./examples/<name>` names a program.
	named := 0
	for _, after := range strings.Split(text, "go run ./examples/")[1:] {
		name := strings.Fields(after)[0]
		if _, err := os.Stat(filepath.Join("examples", name, "main.go")); err != nil {
			t.Fatalf("the README runs a program that is not there: %s", name)
		}
		named++
	}
	if named < 3 {
		t.Fatal("the README does not run the module's programs")
	}
	// And every program the module has is one the README tells of.
	programs, err := os.ReadDir("examples")
	if err != nil {
		t.Fatal(err)
	}
	for _, program := range programs {
		if !strings.Contains(text, "go run ./examples/"+program.Name()) {
			t.Fatalf("the README does not tell of examples/%s", program.Name())
		}
	}
}

func TestEveryFileTheReadmePointsToIsThere(t *testing.T) {
	text := readme(t)
	links := 0
	for _, after := range strings.Split(text, "](")[1:] {
		target, _, _ := strings.Cut(after, ")")
		if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "#") {
			continue
		}
		if _, err := os.Stat(filepath.FromSlash(target)); err != nil {
			t.Fatalf("the README points to a file that is not there: %s", target)
		}
		links++
	}
	if links < 10 {
		t.Fatal("the README does not point into the repository")
	}
	// A place in the README that its table of contents names is there.
	for _, after := range strings.Split(text, "](#")[1:] {
		anchor, _, _ := strings.Cut(after, ")")
		found := false
		for _, line := range strings.Split(text, "\n") {
			if title, is := strings.CutPrefix(strings.TrimLeft(line, "#"), " "); is && strings.HasPrefix(line, "#") && anchorOf(title) == anchor {
				found = true
			}
		}
		if !found {
			t.Fatalf("the README's contents name a place that is not there: #%s", anchor)
		}
	}
}

// anchorOf gives the anchor that a heading gets: lower case, with a hyphen
// for each space, and nothing but letters, digits and hyphens.
func anchorOf(title string) string {
	var anchor strings.Builder
	for _, letter := range strings.ToLower(title) {
		switch {
		case letter == ' ':
			anchor.WriteByte('-')
		case letter == '-' || letter >= 'a' && letter <= 'z' || letter >= '0' && letter <= '9':
			anchor.WriteRune(letter)
		}
	}
	return anchor.String()
}

// inOrder reports whether every one of lines stands in source, in the same
// order.
func inOrder(lines, source []string) bool {
	at := 0
	for _, line := range source {
		if at < len(lines) && line == lines[at] {
			at++
		}
	}
	return at == len(lines)
}

// trimmed gives the lines of a text without the space around them, and
// without the empty ones.
func trimmed(text string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestTheReadmesGoCodeIsCodeThatIsRun(t *testing.T) {
	text := readme(t)
	// The files that the README's code is taken from: the examples, which
	// the tests run, and the file that says what a store is.
	names, err := filepath.Glob("example*_test.go")
	if err != nil || len(names) < 5 {
		t.Fatal(names, err)
	}
	names = append(names, "store.go")
	sources := make(map[string][]string)
	for _, name := range names {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = trimmed(string(content))
	}
	blocks := 0
	for _, after := range strings.Split(text, "```go\n")[1:] {
		block, _, closed := strings.Cut(after, "\n```")
		if !closed {
			t.Fatal("a block of Go code in the README is not closed")
		}
		blocks++
		// A reader's own program has its package and its main where an
		// example has its own. Every other line has to be a line that is
		// compiled, and all of them in one file, in the same order.
		var lines []string
		for _, line := range trimmed(block) {
			if line != "package main" && line != "func main() {" {
				lines = append(lines, line)
			}
		}
		found := false
		for _, source := range sources {
			found = found || inOrder(lines, source)
		}
		if !found {
			t.Fatalf("a block of Go code in the README is in none of the files that are compiled:\n%s", block)
		}
	}
	if blocks < 7 {
		t.Fatalf("the README shows %d blocks of Go code", blocks)
	}
	// The check has teeth: a line that no file has is found out.
	if inOrder([]string{"store := scionic.NewMemoryStore()", "no file has this line"}, sources["example_test.go"]) {
		t.Fatal("a line that no file has passed for one that is compiled")
	}
	if inOrder([]string{"fmt.Println(root) // bafkrei...", "store := scionic.NewMemoryStore()"}, sources["example_test.go"]) {
		t.Fatal("lines out of their order passed for lines in order")
	}
}

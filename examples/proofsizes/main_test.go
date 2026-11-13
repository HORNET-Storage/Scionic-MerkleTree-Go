package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The example is run, and the README's two tables are held to it: a branch
// size that the README gives is the size that is measured here.

// measured is one row of a table: how many entries or chunks, and how many
// bytes the whole list and a branch take.
type measured struct {
	count, list, branch int
}

var (
	folderRows = []measured{{10, 610, 300}, {1_000, 61_000, 648}, {10_000, 610_000, 880}, {1_000_000, 61_000_000, 1228}}
	fileRows   = []measured{{10, 360, 219}, {1_000, 36_000, 459}, {10_000, 360_000, 619}, {500_000, 18_000_000, 819}}
)

// withCommas writes a number as the README does: 1,228.
func withCommas(number int) string {
	digits := fmt.Sprint(number)
	for at := len(digits) - 3; at > 0; at -= 3 {
		digits = digits[:at] + "," + digits[at:]
	}
	return digits
}

func TestABranchIsAsSmallAsTheTablesSay(t *testing.T) {
	for _, row := range folderRows {
		list, branch, err := folderRow(row.count)
		if err != nil || list != row.list || branch != row.branch {
			t.Fatalf("a folder of %d entries: a list of %d bytes and a branch of %d (%v)", row.count, list, branch, err)
		}
	}
	for _, row := range fileRows {
		list, branch, err := fileRow(row.count)
		if err != nil || list != row.list || branch != row.branch {
			t.Fatalf("a file of %d chunks: a list of %d bytes and a branch of %d (%v)", row.count, list, branch, err)
		}
	}
}

func TestTheExamplePrintsBothTables(t *testing.T) {
	var out bytes.Buffer
	if err := sizes(&out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	for _, row := range append(append([]measured{}, folderRows...), fileRows...) {
		want := fmt.Sprintf("%10d  %12d  %8d  ", row.count, row.list, row.branch)
		if !strings.Contains(printed, want) {
			t.Fatalf("the example did not print %q:\n%s", want, printed)
		}
	}
	if !strings.Contains(printed, "49674x") || !strings.Contains(printed, "21978x") {
		t.Fatalf("the example did not print how much smaller the longest branches are:\n%s", printed)
	}
}

func TestTheReadmeGivesTheSizesThatAreMeasured(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
	if len(folderCounts) != len(folderRows) || len(fileCounts) != len(fileRows) {
		t.Fatal("the example measures other sizes than the tables have")
	}
	// A row of the README starts with the count, and has the branch's size
	// in its own column.
	for _, row := range append(append([]measured{}, folderRows...), fileRows...) {
		start := "| " + withCommas(row.count) + " |"
		branch := "| " + withCommas(row.branch) + " bytes |"
		found := false
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, start) && strings.Contains(line, branch) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the README has no row for %s with a branch of %s bytes", withCommas(row.count), withCommas(row.branch))
		}
	}
}

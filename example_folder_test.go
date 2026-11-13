package scionic_test

import (
	"log"
	"os"
	"path/filepath"
)

// workIn makes a new folder with these files in it, and makes it the folder
// that the program works in, so that an example can name its files as a
// reader of it would. It gives back what undoes both.
//
// Examples are run one after another, once every test has ended, so nothing
// else is at work while the folder is another one.
func workIn(files map[string][]byte) (undo func()) {
	was, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	work, err := os.MkdirTemp("", "scionic-example-")
	if err != nil {
		log.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.Chdir(work); err != nil {
		log.Fatal(err)
	}
	return func() {
		if err := os.Chdir(was); err != nil {
			log.Fatal(err)
		}
		os.RemoveAll(work)
	}
}

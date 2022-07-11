package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGofmt tests that all files are formatted.
func TestGofmt(t *testing.T) {
	root, fileMap := copyTree(t)

	gofmt := exec.Command("gofmt", "-w", ".")
	gofmt.Dir = root
	gofmt.Stdout, gofmt.Stderr = os.Stdout, os.Stderr
	if err := gofmt.Run(); err != nil {
		t.Fatalf("gofmt failed: %v", err)
	}

	// Diff the trees.
	if diffFiles(t, fileMap) {
		t.Errorf("Files are not gofmt clean. Please run gofmt.")
	}
}

// TestGenerated tests that all generated files are up-to-date.
func TestGenerated(t *testing.T) {
	root, fileMap := copyTree(t)

	// Build bitstringer
	build := exec.Command("go", "build")
	build.Dir = filepath.Join(root, "cmd/bitstringer")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("go build failed: %v", err)
	}

	gen := exec.Command("go", "generate", "./...")
	gen.Dir = root
	gen.Stdout, gen.Stderr = os.Stdout, os.Stderr
	if err := gen.Run(); err != nil {
		t.Fatalf("go generate failed: %v", err)
	}

	// Diff the trees.
	if diffFiles(t, fileMap) {
		t.Errorf("Please run go generate.")
	}
}

func copyTree(t *testing.T) (string, map[string]string) {
	src, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	dst := t.TempDir()

	// Ensure src ends with "/"
	src = fmt.Sprintf("%s%c", filepath.Clean(src), filepath.Separator)

	fileMap := make(map[string]string)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if !strings.HasPrefix(path, src) {
			panic(fmt.Sprintf("WalkDir path %q does not start with source path %q", path, src))
		}

		rel := path[len(src):]
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if rel == "" {
				// This is the root of the tree, so
				// the destination already exists.
				return nil
			}
			return os.Mkdir(filepath.Join(dst, rel), 0777)
		}
		// Only copy .go and related files.
		if n := d.Name(); !(filepath.Ext(n) == ".go" || n == "go.mod" || n == "go.sum") {
			return nil
		}

		// Copy file.
		fileMap[path] = filepath.Join(dst, rel)
		return copyFile(path, filepath.Join(dst, rel))
	})
	if err != nil {
		t.Fatalf("error copying source tree: %v", err)
	}

	t.Logf("copied source tree to %s", dst)

	return dst, fileMap
}

func copyFile(src, dst string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}

	return out.Close()
}

func diffFiles(t *testing.T, fileMap map[string]string) bool {
	diffs := 0
	for orig, new := range fileMap {
		diff := exec.Command("diff", "-u", orig, new)
		diff.Stdout = os.Stdout
		diff.Stderr = os.Stderr
		if err := diff.Run(); err != nil {
			switch err := err.(type) {
			case *exec.ExitError:
				if err.ExitCode() == 1 {
					diffs++
					continue
				}
			}
			t.Errorf("diff failed: %v", err)
		}
	}
	return diffs != 0
}

package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeGo stands in for the go command: GOROOT and a module list that differs by platform.
type fakeGo struct {
	goroot, moduleDir string
	lists             [][]string // environments of the go list calls
}

func (f *fakeGo) run(env []string, args ...string) ([]byte, error) {
	switch args[0] {
	case "env":
		return []byte(f.goroot + "\n"), nil
	case "list":
		f.lists = append(f.lists, env)
		out := "example.com/module@v1.0.0 " + f.moduleDir + "\n"
		if slices.Contains(env, "GOOS=linux") {
			out += "example.com/linux-only@v2.0.0 " + f.moduleDir + "\n"
		}
		return []byte(out), nil
	}
	return nil, nil
}

func noticesProject(t *testing.T) *fakeGo {
	t.Helper()
	project(t, "0.4.2")
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGo{goroot: filepath.Join(root, "goroot"), moduleDir: filepath.Join(root, "module dir")}
	writeFile(t, filepath.Join(f.goroot, "LICENSE"), "Go license")
	writeFile(t, filepath.Join(f.moduleDir, "LICENSE"), "Module license")
	writeFile(t, filepath.Join(f.moduleDir, "NOTICE.md"), "Module notice")
	writeFile(t, filepath.Join(f.moduleDir, "main.go"), "package module")
	return f
}

const testOut = "third_party"

func TestNoticesGatherGoAndModuleNotices(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"release", []string{"-version", "0.4.2", "-file", versionFile}},
		{"snapshot", []string{"-version", "0.4.2-SNAPSHOT", "-file", versionFile, "-snapshot=true"}},
		{"unchecked", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := noticesProject(t)
			// The output of an earlier run, which holds go/LICENSE, is replaced.
			writeFile(t, filepath.Join(testOut, "go", "LICENSE"), "old Go license")
			stale := filepath.Join(testOut, "stale", "LICENSE")
			writeFile(t, stale, "removed module")
			args := append([]string{"notices", "-out", testOut, "-main", "./cmd/app", "-platforms", "darwin/arm64, linux/amd64"}, tc.args...)
			if res, err := run(args, f.run, io.Discard); err != nil || res != "" {
				t.Fatalf("run = %q, %v", res, err)
			}
			for name, want := range map[string]string{
				"go/LICENSE":                            "Go license",
				"example.com/module@v1.0.0/LICENSE":     "Module license",
				"example.com/module@v1.0.0/NOTICE.md":   "Module notice",
				"example.com/linux-only@v2.0.0/LICENSE": "Module license",
			} {
				if got := readFile(t, filepath.Join(testOut, filepath.FromSlash(name))); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if _, err := os.Stat(filepath.Join(testOut, "example.com", "module@v1.0.0", "main.go")); !os.IsNotExist(err) {
				t.Error("copied a file that is not a notice")
			}
			if _, err := os.Stat(stale); !os.IsNotExist(err) {
				t.Error("kept the notices of an earlier run")
			}
			want := [][]string{{"CGO_ENABLED=0", "GOOS=darwin", "GOARCH=arm64"}, {"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}}
			if !slices.EqualFunc(f.lists, want, slices.Equal) {
				t.Errorf("go list environments = %q, want %q", f.lists, want)
			}
		})
	}
}

// Without -platforms, go list keeps GOOS and GOARCH of the environment, so it lists the
// platform go builds for by default.
func TestNoticesDefaultToGoDefaultPlatformAndTakeEmptyOut(t *testing.T) {
	f := noticesProject(t)
	if err := os.Mkdir(testOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run([]string{"notices", "-out", testOut}, f.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"CGO_ENABLED=0"}}; !slices.EqualFunc(f.lists, want, slices.Equal) {
		t.Fatalf("go list environments = %q, want %q", f.lists, want)
	}
	if _, err := os.Stat(filepath.Join(testOut, "example.com", "linux-only@v2.0.0")); !os.IsNotExist(err) {
		t.Fatal("gathered the notices of another platform")
	}
}

func TestNoticesHelpListsFlagsOnStderr(t *testing.T) {
	noticesProject(t)
	var stderr strings.Builder
	if _, err := run([]string{"notices", "-h"}, noGo(t), &stderr); err == nil || !strings.Contains(stderr.String(), "-platforms") {
		t.Fatalf("err = %v; stderr %q", err, stderr.String())
	}
}

func TestNoticesRefuseBeforeTouchingAnything(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"version mismatch", []string{"-out", testOut, "-version", "0.5.0", "-file", versionFile}, "does not match 0.4.2"},
		{"version without file", []string{"-out", testOut, "-version", "0.4.2"}, "-file is required"},
		{"bool snapshot apart", []string{"-out", testOut, "-snapshot", "true"}, "unexpected arguments"},
		{"no out", nil, "-out is required"},
		{"current directory", []string{"-out", "."}, "relative path inside"},
		{"parent directory", []string{"-out", ".."}, "relative path inside"},
		{"outside", []string{"-out", "../elsewhere"}, "relative path inside"},
		{"absolute", []string{"-out", os.TempDir()}, "relative path inside"},
		{"sources", []string{"-out", "cmd"}, "refusing to replace"},
		{"foreign directory", []string{"-out", "kept"}, "refusing to replace"},
		{"through link", []string{"-out", "link/notices"}, "escapes"},
		{"bad platform", []string{"-out", testOut, "-platforms", "linux"}, "not GOOS/GOARCH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noticesProject(t)
			writeFile(t, filepath.Join("kept", "file"), "kept")
			elsewhere := filepath.Join("..", "elsewhere")
			writeFile(t, filepath.Join(elsewhere, "notices", "file"), "elsewhere")
			if err := os.Symlink(elsewhere, "link"); err != nil {
				t.Fatal(err)
			}
			if _, err := run(append([]string{"notices"}, tc.args...), noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
			if readFile(t, filepath.Join("kept", "file")) != "kept" || readFile(t, versionFile) != testSource("0.4.2") ||
				readFile(t, filepath.Join(elsewhere, "notices", "file")) != "elsewhere" {
				t.Fatal("a refused call changed files")
			}
		})
	}
}

func TestNoticesRefuseIncompleteModules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *fakeGo)
		want   string
	}{
		{"no notices", func(f *fakeGo) {
			_ = os.Remove(filepath.Join(f.moduleDir, "LICENSE"))
			_ = os.Remove(filepath.Join(f.moduleDir, "NOTICE.md"))
		}, "example.com/linux-only@v2.0.0: no license notices found"},
		{"no directory", func(f *fakeGo) { f.moduleDir = "" }, "no module directory found for example.com/module@v1.0.0"},
		{"no Go license", func(f *fakeGo) { _ = os.Remove(filepath.Join(f.goroot, "LICENSE")) }, filepath.Join("goroot", "LICENSE")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := noticesProject(t)
			tc.change(f)
			if _, err := run([]string{"notices", "-out", testOut, "-platforms", "linux/amd64"}, f.run, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one with %q", err, tc.want)
			}
		})
	}
}

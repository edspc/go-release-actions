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

func TestNoticesCombineProjectGoAndModules(t *testing.T) {
	f := noticesProject(t)
	writeFile(t, "LICENSE", "Project license")
	args := []string{"notices", "-out", testOut, "-platforms", "linux/amd64", "-combined", "release/NOTICES.txt"}
	if _, err := run(args, f.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	combined := readFile(t, "release/NOTICES.txt")
	for _, want := range []string{"Project license", "Go license", "Module license", "Module notice"} {
		if !strings.Contains(combined, want) {
			t.Errorf("combined notices omit %q", want)
		}
	}
	if _, err := run(args, f.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if readFile(t, "release/NOTICES.txt") != combined {
		t.Fatal("combined notices changed order on a repeated run")
	}
}

func TestNoticesRefuseUnsafeCombinedOutput(t *testing.T) {
	for _, out := range []string{"LICENSE", testOut, testOut + "/all.txt", "../outside.txt"} {
		t.Run(out, func(t *testing.T) {
			noticesProject(t)
			writeFile(t, "LICENSE", "Project license")
			if _, err := run([]string{"notices", "-out", testOut, "-combined", out}, noGo(t), io.Discard); err == nil {
				t.Fatal("accepted unsafe combined output")
			}
			if readFile(t, "LICENSE") != "Project license" {
				t.Fatal("changed the project license")
			}
		})
	}
}

func TestNoticesCombinedOutputCannotAliasLicense(t *testing.T) {
	for _, link := range []struct {
		name   string
		create func(string, string) error
	}{
		{"symlink", os.Symlink},
		{"hardlink", os.Link},
	} {
		t.Run(link.name, func(t *testing.T) {
			f := noticesProject(t)
			writeFile(t, "LICENSE", "Project license")
			if err := link.create("LICENSE", "combined.txt"); err != nil {
				t.Fatal(err)
			}
			if _, err := run([]string{"notices", "-out", testOut, "-combined", "combined.txt"}, f.run, io.Discard); err == nil || !strings.Contains(err.Error(), "aliases license notice") {
				t.Fatalf("err = %v", err)
			}
			if readFile(t, "LICENSE") != "Project license" {
				t.Fatal("overwrote the project license")
			}
		})
	}
}

func TestNoticesKeepCombinedOutputOnIncompleteNotices(t *testing.T) {
	f := noticesProject(t)
	writeFile(t, "LICENSE", "Project license")
	writeFile(t, "combined.txt", "Previous release notices")
	writeFile(t, filepath.Join(f.moduleDir, "NOTICE.md"), " ")
	if _, err := run([]string{"notices", "-out", testOut, "-combined", "combined.txt"}, f.run, io.Discard); err == nil || !strings.Contains(err.Error(), "empty license notice") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, "combined.txt") != "Previous release notices" {
		t.Fatal("replaced combined output with incomplete notices")
	}
}

func TestNoticesCombinesRelativeDirectorySpellings(t *testing.T) {
	for _, dir := range []string{"./" + testOut, testOut + "/"} {
		t.Run(dir, func(t *testing.T) {
			f := noticesProject(t)
			writeFile(t, "LICENSE", "Project license")
			if _, err := run([]string{"notices", "-out", dir, "-combined", "combined.txt"}, f.run, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(readFile(t, "combined.txt"), "Go license") {
				t.Fatal("combined output omits the Go license")
			}
		})
	}
}

func TestNoticesRefuseLicenseInsideOutputBeforeChangingFiles(t *testing.T) {
	for _, tc := range []struct{ out, license string }{
		{testOut, testOut + "/project-LICENSE"},
		{"./" + testOut + "/", "./" + testOut + "/project-LICENSE"},
		{testOut, testOut + "/nested/project-LICENSE"},
		{testOut, testOut + "/nested/../project-LICENSE"},
		{testOut, testOut},
	} {
		t.Run(tc.out+":"+tc.license, func(t *testing.T) {
			noticesProject(t)
			writeFile(t, testOut+"/go/LICENSE", "Previous Go license")
			writeFile(t, testOut+"/module/NOTICE", "Previous module notice")
			if tc.license != testOut {
				writeFile(t, filepath.Clean(tc.license), "Project license")
			}
			writeFile(t, "combined.txt", "Previous release notices")
			args := []string{"notices", "-out", tc.out, "-license", tc.license, "-combined", "combined.txt"}
			if _, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), "-license must be outside -out") {
				t.Fatalf("err = %v", err)
			}
			if tc.license != testOut && readFile(t, filepath.Clean(tc.license)) != "Project license" {
				t.Fatal("changed the project license")
			}
			for path, want := range map[string]string{
				testOut + "/go/LICENSE":    "Previous Go license",
				testOut + "/module/NOTICE": "Previous module notice",
				"combined.txt":             "Previous release notices",
			} {
				if readFile(t, path) != want {
					t.Fatalf("changed %s", path)
				}
			}
		})
	}
}

func TestNoticesAllowLicenseInSiblingDirectory(t *testing.T) {
	f := noticesProject(t)
	license := testOut + "-backup/LICENSE"
	writeFile(t, license, "Project license")
	if _, err := run([]string{"notices", "-out", testOut, "-license", license, "-combined", "combined.txt"}, f.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, "combined.txt"), "Project license") {
		t.Fatal("combined output omits the project license")
	}
}

func TestNoticesRefuseLicenseAliasingOutputBeforeChangingFiles(t *testing.T) {
	for _, tc := range []struct{ name, out, license string }{
		{"license symlink", testOut, "project-LICENSE"},
		{"parent symlink", testOut, "notice-link/project-LICENSE"},
		{"output symlink", "notice-link", testOut + "/project-LICENSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noticesProject(t)
			writeFile(t, testOut+"/go/LICENSE", "Previous Go license")
			writeFile(t, testOut+"/project-LICENSE", "Project license")
			writeFile(t, "combined.txt", "Previous release notices")
			if err := os.Symlink(testOut+"/project-LICENSE", "project-LICENSE"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(testOut, "notice-link"); err != nil {
				t.Fatal(err)
			}
			args := []string{"notices", "-out", tc.out, "-license", tc.license, "-combined", "combined.txt"}
			if _, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), "-license must be outside -out") {
				t.Fatalf("err = %v", err)
			}
			for path, want := range map[string]string{
				testOut + "/go/LICENSE":      "Previous Go license",
				testOut + "/project-LICENSE": "Project license",
				"combined.txt":               "Previous release notices",
			} {
				if readFile(t, path) != want {
					t.Fatalf("changed %s", path)
				}
			}
		})
	}
}

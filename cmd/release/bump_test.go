package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testReadme = "Version 0.4.2, tag v0.4.2, archive 0.4.2-source.tar.gz\n\n"

// versionFile is where project keeps the version; Go reads slash-separated paths on every OS.
const versionFile = "cmd/app/version.go"

// outsideFile stands in, in the arguments of a test, for the absolute path of the file outside
// the project.
const outsideFile = "OUTSIDE"

func testSource(version string) string {
	return "package main\n\nconst version = \"" + version + "\"\n\nvar other = \"0.4.2\"\n"
}

// project makes a project in a new directory, next to a file outside it, and changes into it,
// as the command runs from the root of the project. It returns the outside file.
func project(t *testing.T, version string) string {
	t.Helper()
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside.txt")
	writeFile(t, outside, "outside 0.4.2\n")
	t.Chdir(parent)
	if err := os.Mkdir("project", 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir("project")
	writeFile(t, versionFile, testSource(version))
	writeFile(t, "README.md", testReadme)
	return outside
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// noGo fails a test that reaches the go command.
func noGo(t *testing.T) goTool {
	return func([]string, ...string) ([]byte, error) {
		t.Error("the go command was run")
		return nil, nil
	}
}

func TestNextVersion(t *testing.T) {
	for _, tc := range []struct{ version, part, want string }{
		{"0.4.2", "patch", "0.4.3"},
		{"0.4.2", "minor", "0.5.0"},
		{"0.4.2", "major", "1.0.0"},
		{"0.9.9", "patch", "0.9.10"},
		{"1.10.7", "minor", "1.11.0"},
	} {
		if got, err := nextVersion(tc.version, tc.part); err != nil || got != tc.want {
			t.Errorf("nextVersion(%s, %s) = %s, %v; want %s", tc.version, tc.part, got, err, tc.want)
		}
	}
}

func TestReplaceVersionOnlyWhereItStandsAlone(t *testing.T) {
	for text, want := range map[string]string{
		"1.2.3":                         "1.3.0",
		"v1.2.3, 1.2.3-rc.1, (1.2.3).":  "v1.3.0, 1.3.0-rc.1, (1.3.0).",
		"1.2.3 1.2.3":                   "1.3.0 1.3.0",
		"11.2.3 1.2.30 1.2.3.4 0.1.2.3": "11.2.3 1.2.30 1.2.3.4 0.1.2.3",
		"foo@1.2.3/LICENSE":             "foo@1.3.0/LICENSE",
	} {
		if got := replaceVersion(text, "1.2.3", "1.3.0"); got != want {
			t.Errorf("replaceVersion(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestBumpRaisesFileAndReplacesElsewhere(t *testing.T) {
	project(t, "0.4.2")
	const others = "Needs foo 10.4.2 and bar 0.4.20.\n"
	writeFile(t, "README.md", testReadme+others)
	version, err := run([]string{"bump", "-part", "minor", "-file", versionFile, "-replace", " README.md ,\n"}, noGo(t), io.Discard)
	if err != nil || version != "0.5.0" {
		t.Fatalf("run = %q, %v", version, err)
	}
	if got, want := readFile(t, versionFile), testSource("0.5.0"); got != want {
		t.Fatalf("version file = %q, want %q: only the matched version changes", got, want)
	}
	if got, want := readFile(t, "README.md"), strings.ReplaceAll(testReadme, "0.4.2", "0.5.0")+others; got != want {
		t.Fatalf("README = %q, want %q", got, want)
	}
}

func TestBumpWithPatternAndVersionFileReplaced(t *testing.T) {
	project(t, "0.4.2")
	writeFile(t, "VERSION.txt", "version: 1.2.3\nsee 1.2.3\n")
	version, err := run([]string{"bump", "-part", "patch", "-file", "VERSION.txt", "-pattern", `^version: (\S+)$`, "-replace", "VERSION.txt"}, noGo(t), io.Discard)
	if err != nil || version != "1.2.4" {
		t.Fatalf("run = %q, %v", version, err)
	}
	if got := readFile(t, "VERSION.txt"); got != "version: 1.2.4\nsee 1.2.4\n" {
		t.Fatalf("VERSION.txt = %q", got)
	}
}

func TestBumpHelpListsFlagsOnStderr(t *testing.T) {
	project(t, "0.4.2")
	var stderr strings.Builder
	out, err := run([]string{"bump", "-h"}, noGo(t), &stderr)
	if err == nil || out != "" || !strings.Contains(stderr.String(), "-replace") {
		t.Fatalf("run = %q, %v; stderr %q", out, err, stderr.String())
	}
}

func TestBumpRefusesBadInputUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		args          []string
		want          string
	}{
		{"no command", "0.4.2", nil, "usage"},
		{"unknown command", "0.4.2", []string{"tag"}, "usage"},
		{"no file", "0.4.2", []string{"bump", "-part", "patch"}, "-file is required"},
		{"no part", "0.4.2", []string{"bump", "-file", versionFile}, "part must be"},
		{"upper case", "0.4.2", []string{"bump", "-part", "MINOR", "-file", versionFile}, "part must be"},
		{"extra argument", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "minor"}, "unexpected arguments"},
		{"unknown flag", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-force"}, "flag provided but not defined"},
		{"missing replace file", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-replace", "README.md,CHANGES.md"}, "CHANGES.md"},
		{"no group", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-pattern", `^const version = ".*"$`}, "exactly one group"},
		{"bad pattern", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-pattern", `(`}, "pattern"},
		{"two numbers", "0.4", []string{"bump", "-part", "patch", "-file", versionFile}, "not MAJOR.MINOR.PATCH"},
		{"leading zero", "01.4.2", []string{"bump", "-part", "patch", "-file", versionFile}, "not MAJOR.MINOR.PATCH"},
		{"pre-release", "0.4.2-rc.1", []string{"bump", "-part", "patch", "-file", versionFile}, "not MAJOR.MINOR.PATCH"},
		{"overflow", "0.4.99999999999999999999", []string{"bump", "-part", "patch", "-file", versionFile}, "not MAJOR.MINOR.PATCH"},
		{"two versions", "0.4.2", []string{"bump", "-part", "patch", "-file", "README.md", "-pattern", `(\d+\.\d+\.\d+)`}, "found 3 versions"},
		{"file outside", "0.4.2", []string{"bump", "-part", "patch", "-file", "../outside.txt", "-pattern", `^outside (.*)$`}, "relative path inside"},
		{"absolute file", "0.4.2", []string{"bump", "-part", "patch", "-file", outsideFile, "-pattern", `^outside (.*)$`}, "relative path inside"},
		{"replace outside", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-replace", "README.md,../outside.txt"}, "relative path inside"},
		{"absolute replace", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-replace", outsideFile}, "relative path inside"},
		{"replace through link", "0.4.2", []string{"bump", "-part", "patch", "-file", versionFile, "-replace", "README.md,link.txt"}, "escapes"},
		{"file through link", "0.4.2", []string{"bump", "-part", "patch", "-file", "link.txt", "-pattern", `^outside (.*)$`}, "escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := project(t, tc.version)
			if err := os.Symlink(outside, "link.txt"); err != nil {
				t.Fatal(err)
			}
			args := slices.Clone(tc.args)
			for i := range args {
				if args[i] == outsideFile {
					args[i] = outside
				}
			}
			if version, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run = %q, %v; want an error with %q", version, err, tc.want)
			}
			if readFile(t, versionFile) != testSource(tc.version) || readFile(t, "README.md") != testReadme || readFile(t, outside) != "outside 0.4.2\n" {
				t.Fatal("a refused call changed the files")
			}
		})
	}
}

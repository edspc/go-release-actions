package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const binaryManifest = `[
 {"type":"Metadata","name":"metadata.json"},
 {"type":"Binary","name":"app","path":"dist/linux/app"},
 {"type":"Binary","name":"app-linux-amd64","path":"dist/linux/app","goos":"linux","extra":{"Format":"binary"}},
 {"type":"Binary","name":"app-darwin-arm64","path":"dist/darwin/app","goos":"darwin","extra":{"Format":"binary"}}
]`

func binaryProject(t *testing.T) []string {
	t.Helper()
	project(t, "0.4.2")
	writeFile(t, "dist/artifacts.json", binaryManifest)
	writeFile(t, "dist/linux/app", "\x7fELFfixture")
	writeFile(t, "dist/darwin/app", "\xcf\xfa\xed\xfefixture")
	for _, path := range []string{"dist/linux/app", "dist/darwin/app"} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, "LICENSE", "Project license")
	writeFile(t, "notices/go/LICENSE", "Go license")
	writeFile(t, "notices/example.com/lib@v1.0.0/LICENSE", "Module license")
	writeFile(t, "notices/example.com/lib@v1.0.0/NOTICE", "Module notice")
	writeFile(t, "release-licenses.txt", "Project license\nGo license\nModule license\nModule notice\n")
	return []string{"check-binaries", "-names", "app-linux-amd64,\napp-darwin-arm64", "-notices", "notices",
		"-combined", "release-licenses.txt", "-required-notices", "example.com/lib@*/LICENSE"}
}

func TestCheckBinaries(t *testing.T) {
	args := binaryProject(t)
	if out, err := run(args, noGo(t), io.Discard); err != nil || out != "" {
		t.Fatalf("run = %q, %v", out, err)
	}
	if got := readFile(t, "dist/artifacts.json"); got != binaryManifest {
		t.Fatal("changed the artifact manifest")
	}
}

func TestCheckBinariesAcceptsThinAndUniversalMachO(t *testing.T) {
	for _, tc := range []struct{ name, header string }{
		{"thin64", "\xcf\xfa\xed\xfe"},
		{"universal", "\xca\xfe\xba\xbe"},
		{"universal64", "\xca\xfe\xba\xbf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := binaryProject(t)
			writeFile(t, "dist/darwin/app", tc.header+"fixture")
			if _, err := run(args, noGo(t), io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckBinariesRefusesInvalidReleases(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement, file, content, want string
	}{
		{name: "invalid JSON", file: "dist/artifacts.json", content: "[", want: "artifacts:"},
		{name: "missing asset", old: `"Format":"binary"`, replacement: `"Format":""`, want: "missing binary asset"},
		{name: "duplicate asset", old: "app-darwin-arm64", replacement: "app-linux-amd64", want: "duplicate binary asset"},
		{name: "unexpected asset", old: "app-linux-amd64", replacement: "app-other", want: "unexpected or duplicate"},
		{name: "archive", old: `"type":"Metadata"`, replacement: `"type":"Archive"`, want: "only binaries are allowed"},
		{name: "source", old: `"type":"Metadata"`, replacement: `"type":"Source"`, want: "only binaries are allowed"},
		{name: "checksums", old: `"type":"Metadata"`, replacement: `"type":"Checksum"`, want: "only binaries are allowed"},
		{name: "wrong signature", file: "dist/linux/app", content: "not ELF", want: "invalid linux executable signature"},
		{name: "invalid universal signature", file: "dist/darwin/app", content: "\xca\xfe\xba\xbdfixture", want: "invalid darwin executable signature"},
		{name: "short binary", file: "dist/linux/app", content: "x", want: "unexpected EOF"},
		{name: "wrong OS", old: `"goos":"linux"`, replacement: `"goos":"darwin"`, want: "invalid darwin executable signature"},
		{name: "unsupported OS", old: `"goos":"linux"`, replacement: `"goos":"windows"`, want: "unsupported binary OS"},
		{name: "outside binary", old: "dist/linux/app", replacement: "../outside.txt", want: "relative path inside"},
		{name: "missing combined notice", file: "release-licenses.txt", content: "Project license\nGo license\nModule license", want: "missing notices/example.com/lib@v1.0.0/NOTICE"},
		{name: "empty notice", file: "notices/go/LICENSE", content: "\n", want: "empty license notice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := binaryProject(t)
			if tc.old != "" {
				writeFile(t, "dist/artifacts.json", strings.ReplaceAll(binaryManifest, tc.old, tc.replacement))
			}
			if tc.file != "" {
				writeFile(t, tc.file, tc.content)
			}
			if _, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckBinariesRefusesMissingOrUnsafeFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T)
		want   string
	}{
		{"nonexecutable", func(t *testing.T) {
			if err := os.Chmod("dist/linux/app", 0o644); err != nil {
				t.Fatal(err)
			}
		}, "not a regular executable"},
		{"missing binary", func(t *testing.T) {
			if err := os.Remove("dist/linux/app"); err != nil {
				t.Fatal(err)
			}
		}, "no such file"},
		{"missing Go notice", func(t *testing.T) {
			if err := os.Remove("notices/go/LICENSE"); err != nil {
				t.Fatal(err)
			}
		}, "missing required notice"},
		{"missing module notice", func(t *testing.T) {
			if err := os.Remove("notices/example.com/lib@v1.0.0/LICENSE"); err != nil {
				t.Fatal(err)
			}
		}, "missing required notice"},
		{"binary link escapes", func(t *testing.T) {
			if err := os.Remove("dist/linux/app"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../../outside.txt", "dist/linux/app"); err != nil {
				t.Fatal(err)
			}
		}, "escapes"},
		{"notice link", func(t *testing.T) {
			if err := os.Symlink(filepath.Join("..", "..", "outside.txt"), "notices/leak"); err != nil {
				t.Fatal(err)
			}
		}, "not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := binaryProject(t)
			tc.change(t)
			if _, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckBinariesFlags(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{
		{"names", "", "-names is required"},
		{"names", "app-linux-amd64,app-linux-amd64", "duplicate expected asset"},
		{"artifacts", "../outside.txt", "relative path inside"},
		{"combined", "../outside.txt", "relative path inside"},
		{"notices", "../", "relative path inside"},
		{"license", "../outside.txt", "relative path inside"},
		{"required-notices", "[", "syntax error"},
		{"required-notices", "../outside.txt", "relative path inside"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			args := append(binaryProject(t), "-"+tc.name, tc.value)
			if _, err := run(args, noGo(t), io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	binaryProject(t)
	var help strings.Builder
	if _, err := run([]string{"check-binaries", "-h"}, noGo(t), &help); err == nil || !strings.Contains(help.String(), "-names") {
		t.Fatalf("err = %v; help %q", err, help.String())
	}
}

func TestCheckBinariesAcceptsRelativeNoticePathSpellings(t *testing.T) {
	for _, dir := range []string{"./notices", "notices/"} {
		t.Run(dir, func(t *testing.T) {
			args := append(binaryProject(t), "-notices", dir, "-required-notices", "./example.com/lib@*/LICENSE")
			if _, err := run(args, noGo(t), io.Discard); err != nil {
				t.Fatal(err)
			}
		})
	}
}

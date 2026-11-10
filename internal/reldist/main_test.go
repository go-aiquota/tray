// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBuild replaces the compiler: the "binary" names its program and target.
func fakeBuild(t *testing.T) {
	t.Helper()
	old := build
	t.Cleanup(func() { build = old })
	build = func(_ context.Context, p Program, tg Target, out string) error {
		return os.WriteFile(out, []byte(p.Name+" "+tg.String()), 0o755)
	}
}

func TestTargetsAreSixOSesTimesTwoArchitectures(t *testing.T) {
	got := map[string]int{}
	for _, tg := range Targets {
		got[tg.GOOS]++
	}
	for _, goos := range []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd"} {
		if got[goos] != 2 {
			t.Errorf("%s: %d targets, want amd64 and arm64", goos, got[goos])
		}
	}
	if len(Targets) != 12 {
		t.Errorf("%d targets", len(Targets))
	}
}

func TestProgramsIncludeThePluginUnderTheNameAiquotaLooksUp(t *testing.T) {
	var names []string
	for _, p := range Programs {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "aiquota,quotaclaim,go-aiquota-plugin-claude" {
		t.Fatalf("programs %v", names)
	}
}

func TestRunWritesArchivesAndMatchingSums(t *testing.T) {
	fakeBuild(t)
	out := t.TempDir()
	ts := []Target{{"linux", "amd64"}, {"windows", "arm64"}}
	var log bytes.Buffer
	archives, err := Run(context.Background(), "v1.2.3", out, ts, false, []byte("lic"), &log)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"aiquota_v1.2.3_linux_amd64.tar.gz", "aiquota_v1.2.3_windows_arm64.zip"}
	if strings.Join(archives, ",") != strings.Join(want, ",") {
		t.Fatalf("archives %v", archives)
	}

	// tar.gz: three executables with exec bit, LICENSE, one directory prefix.
	f, _ := os.Open(filepath.Join(out, want[0]))
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	seen := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name] = h.Mode
	}
	dir := "aiquota_v1.2.3_linux_amd64/"
	for _, n := range []string{"aiquota", "quotaclaim", "go-aiquota-plugin-claude"} {
		if seen[dir+n] != 0o755 {
			t.Errorf("%s: mode %o", n, seen[dir+n])
		}
	}
	if _, ok := seen[dir+"LICENSE"]; !ok || len(seen) != 4 {
		t.Errorf("entries %v", seen)
	}

	// zip: Windows names carry .exe.
	zr, err := zip.OpenReader(filepath.Join(out, want[1]))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var zn []string
	for _, zf := range zr.File {
		zn = append(zn, zf.Name)
	}
	if got := strings.Join(zn, " "); !strings.Contains(got, "go-aiquota-plugin-claude.exe") || !strings.Contains(got, "/aiquota.exe") {
		t.Errorf("zip entries %v", zn)
	}

	// SHA256SUMS: every line verifies against the file next to it.
	sums, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if len(lines) != 2 {
		t.Fatalf("SHA256SUMS %q", sums)
	}
	for _, l := range lines {
		sum, name, ok := strings.Cut(l, "  ")
		if !ok {
			t.Fatalf("line %q", l)
		}
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != sum {
			t.Errorf("%s: sum mismatch", name)
		}
	}
	// No scratch left behind.
	entries, _ := os.ReadDir(out)
	if len(entries) != 3 {
		t.Errorf("%d entries in out, want 2 archives + SHA256SUMS", len(entries))
	}
}

func TestArchivesAreReproducible(t *testing.T) {
	fakeBuild(t)
	read := func() []byte {
		out := t.TempDir()
		if _, err := Run(context.Background(), "v0.0.1", out, Targets[:1], false, nil, io.Discard); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
		return b
	}
	if !bytes.Equal(read(), read()) {
		t.Error("two runs differ")
	}
}

func TestCheckBuildsEverythingAndWritesNothing(t *testing.T) {
	fakeBuild(t)
	n := 0
	inner := build
	build = func(ctx context.Context, p Program, tg Target, out string) error { n++; return inner(ctx, p, tg, out) }
	out := t.TempDir()
	if _, err := Run(context.Background(), "", out, Targets, true, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if n != len(Targets)*len(Programs) {
		t.Errorf("%d builds", n)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("--check left %d entries", len(entries))
	}
}

func TestRefusals(t *testing.T) {
	fakeBuild(t)
	if _, err := Run(context.Background(), "0.8.0", t.TempDir(), Targets[:1], false, nil, io.Discard); err == nil {
		t.Error("a version without the v was accepted")
	}
	if _, err := parseOnly("plan9/amd64"); err == nil {
		t.Error("an unknown target was accepted")
	}
	old := build
	t.Cleanup(func() { build = old })
	build = func(context.Context, Program, Target, string) error { return io.ErrUnexpectedEOF }
	if _, err := Run(context.Background(), "v1.0.0", t.TempDir(), Targets[:1], false, nil, io.Discard); err == nil {
		t.Error("a failed build was not reported")
	}
}

func TestRootCmdCheck(t *testing.T) {
	fakeBuild(t)
	cmd := RootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--check", "--out", t.TempDir(), "--only", "linux/amd64,netbsd/arm64"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "built 2 targets\n" {
		t.Errorf("output %q", out.String())
	}
}

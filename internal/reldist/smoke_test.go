// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// packHost builds the archive of the host platform with fake programs and
// returns its path and the SHA256SUMS path.
func packHost(t *testing.T) (archive, sums string) {
	t.Helper()
	fakeBuild(t)
	out := t.TempDir()
	tg := Target{runtime.GOOS, runtime.GOARCH}
	names, err := Run(context.Background(), "v1.2.3", out, []Target{tg}, false, []byte("lic"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(out, names[0]), filepath.Join(out, "SHA256SUMS")
}

func fakeRun(t *testing.T, say func(exe string, args []string) runResult) *[][]string {
	t.Helper()
	old := runProgram
	t.Cleanup(func() { runProgram = old })
	var calls [][]string
	runProgram = func(_ context.Context, exe string, env []string, args ...string) (runResult, error) {
		calls = append(calls, append([]string{filepath.Base(exe)}, args...))
		for _, e := range env {
			if strings.HasPrefix(e, "PATH=") && strings.Contains(e, "empty") == false {
				t.Errorf("PATH is not the empty directory: %s", e)
			}
		}
		return say(exe, args), nil
	}
	return &calls
}

func good(exe string, args []string) runResult {
	base := strings.TrimSuffix(filepath.Base(exe), ".exe")
	if args[0] == "--version" {
		if base == "go-aiquota-plugin-claude" {
			return runResult{Out: base + " v0.2.1\n"}
		}
		return runResult{Out: base + " v1.2.3\n"}
	}
	return runResult{Out: "verdict: UNKNOWN\n", Code: 3}
}

func TestSmokePassesAndRunsEveryProgramWithAMinimalPath(t *testing.T) {
	archive, sums := packHost(t)
	calls := fakeRun(t, good)
	if err := Smoke(context.Background(), archive, sums, "v1.2.3", t.TempDir(), io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 4 {
		t.Fatalf("calls %v", *calls)
	}
}

func TestSmokeFailsOnWrongVersionMissingPluginAndBadChecksum(t *testing.T) {
	archive, sums := packHost(t)
	dest := func() string { return t.TempDir() }

	fakeRun(t, func(exe string, args []string) runResult {
		r := good(exe, args)
		if strings.Contains(exe, "quotaclaim") {
			r.Out = "quotaclaim v0.0.1\n"
		}
		return r
	})
	if err := Smoke(context.Background(), archive, sums, "v1.2.3", dest(), io.Discard); err == nil || !strings.Contains(err.Error(), "quotaclaim") {
		t.Errorf("a wrong version passed: %v", err)
	}

	fakeRun(t, func(exe string, args []string) runResult {
		if args[0] == "status" {
			return runResult{Out: "problem: no go-aiquota-plugin-claude on PATH\n", Code: 3}
		}
		return good(exe, args)
	})
	if err := Smoke(context.Background(), archive, sums, "v1.2.3", dest(), io.Discard); err == nil || !strings.Contains(err.Error(), "plugin") {
		t.Errorf("a missing plugin passed: %v", err)
	}

	fakeRun(t, good)
	if err := os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  "+filepath.Base(archive)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Smoke(context.Background(), archive, sums, "v1.2.3", dest(), io.Discard); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a wrong checksum passed: %v", err)
	}
	if err := os.WriteFile(sums, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Smoke(context.Background(), archive, sums, "v1.2.3", dest(), io.Discard); err == nil {
		t.Error("an archive absent from SHA256SUMS passed")
	}
}

func TestExtractRefusesTraversalLinksAndFloods(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, entries map[string]byte) string {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for n, typ := range entries {
			_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: typ, Mode: 0o755, Size: 0, Linkname: "/etc/passwd"})
		}
		tw.Close()
		gz.Close()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, entries := range map[string]map[string]byte{
		"aiquota_v1_linux_amd64.tar.gz": {"aiquota_v1_linux_amd64/../evil": tar.TypeReg},
		"aiquota_v2_linux_amd64.tar.gz": {"/abs": tar.TypeReg},
		"aiquota_v3_linux_amd64.tar.gz": {"aiquota_v3_linux_amd64/link": tar.TypeSymlink},
		"aiquota_v4_linux_amd64.tar.gz": {"other/aiquota": tar.TypeReg},
	} {
		if _, err := extract(mk(name, entries), t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("aiquota_v5_windows_amd64/../../evil.exe")
	_, _ = w.Write([]byte("x"))
	zw.Close()
	zp := filepath.Join(dir, "aiquota_v5_windows_amd64.zip")
	if err := os.WriteFile(zp, zb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extract(zp, t.TempDir()); err == nil {
		t.Error("zip traversal accepted")
	}
}

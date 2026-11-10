// Copyright (c) the go-aiquota authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command reldist cross-builds the programs a release carries, for every
// platform the release carries them for, and packs one archive per platform
// plus a SHA256SUMS file:
//
//	go run ./internal/reldist --version v0.8.0 --out dist
//	go run ./internal/reldist --check --out dist     # CI: build only
//
// The programs are aiquota, quotaclaim and go-aiquota-plugin-claude, the
// provider plugin aiquota starts: it is not part of this module's own
// packages but is pinned in go.mod (a tool dependency), so a release holds
// exactly the plugin this tree was tested with. Archives are .tar.gz, and
// .zip on Windows. Every build is CGO_ENABLED=0, -trimpath.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Target is one GOOS/GOARCH a release carries binaries for.
type Target struct{ GOOS, GOARCH string }

func (t Target) String() string { return t.GOOS + "/" + t.GOARCH }

// Targets is every platform built: Windows, Linux, macOS and the three BSDs,
// amd64 and arm64 each. The BSDs are cross-built only: GitHub has no runner
// for them, so nothing there runs the binaries.
var Targets = func() []Target {
	var ts []Target
	for _, goos := range []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd"} {
		for _, arch := range []string{"amd64", "arm64"} {
			ts = append(ts, Target{goos, arch})
		}
	}
	return ts
}()

// Program is one executable of a release: the package to build and the file
// name it is installed under (the plugin must be named exactly this: aiquota
// looks it up on PATH).
//
// VersionVar, when set, is the variable the release tag is linked into
// (-X); Version is that tag, filled in by Run. The plugin has none: it
// reports the version of its own module.
type Program struct{ Pkg, Name, VersionVar, Version string }

const buildverVar = "github.com/go-aiquota/tray/internal/buildver.version"

// Programs are the executables in every archive.
var Programs = []Program{
	{Pkg: "./cmd/aiquota", Name: "aiquota", VersionVar: buildverVar},
	{Pkg: "./cmd/quotaclaim", Name: "quotaclaim", VersionVar: buildverVar},
	{Pkg: "github.com/go-aiquota/plugin-claude", Name: "go-aiquota-plugin-claude"},
}

// ldflags is what is passed to the linker for p.
func ldflags(p Program) string {
	f := "-s -w"
	if p.VersionVar != "" && p.Version != "" {
		f += " -X " + p.VersionVar + "=" + p.Version
	}
	return f
}

// exeName is the file name of p on the target (Windows adds .exe).
func exeName(p Program, t Target) string {
	if t.GOOS == "windows" {
		return p.Name + ".exe"
	}
	return p.Name
}

// build compiles p for t into out. A var so a test can supply it.
var build = func(ctx context.Context, p Program, t Target, out string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags="+ldflags(p), "-o", out, p.Pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t.GOOS, "GOARCH="+t.GOARCH)
	var msg bytes.Buffer
	cmd.Stdout, cmd.Stderr = &msg, &msg
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s for %s: %w\n%s", p.Pkg, t, err, strings.TrimSpace(msg.String()))
	}
	return nil
}

var versionRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// archiveName is the base name (no extension) of the archive for t.
func archiveName(version string, t Target) string {
	return fmt.Sprintf("aiquota_%s_%s_%s", version, t.GOOS, t.GOARCH)
}

type file struct {
	name string
	data []byte
}

var epoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // reproducible archives

func writeTarGz(path, dir string, files []file) error {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		mode := int64(0o644)
		if !strings.HasSuffix(f.name, "LICENSE") {
			mode = 0o755
		}
		h := &tar.Header{Name: dir + "/" + f.name, Mode: mode, Size: int64(len(f.data)), ModTime: epoch, Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(f.data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeZip(path, dir string, files []file) error {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: dir + "/" + f.name, Method: zip.Deflate, Modified: epoch}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(f.data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// Run builds every program for every target in ts. With check set it only
// builds (nothing is archived). Otherwise it writes one archive per target
// and SHA256SUMS into out, and returns the archive names in order.
func Run(ctx context.Context, version, out string, ts []Target, check bool, license []byte, log io.Writer) ([]string, error) {
	if !check && !versionRE.MatchString(version) {
		return nil, fmt.Errorf("--version %q is not vMAJOR.MINOR.PATCH", version)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(out, "build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)

	var archives []string
	sums := map[string]string{}
	for _, t := range ts {
		var files []file
		for _, p := range Programs {
			if !check {
				p.Version = version
			}
			exe := filepath.Join(work, t.GOOS+"_"+t.GOARCH+"_"+exeName(p, t))
			fmt.Fprintf(log, "build %s %s\n", t, p.Name)
			if err := build(ctx, p, t, exe); err != nil {
				return nil, err
			}
			data, err := os.ReadFile(exe)
			if err != nil {
				return nil, err
			}
			if len(data) == 0 {
				return nil, fmt.Errorf("%s for %s: empty binary", p.Name, t)
			}
			files = append(files, file{exeName(p, t), data})
			os.Remove(exe)
		}
		if check {
			continue
		}
		if len(license) > 0 {
			files = append(files, file{"LICENSE", license})
		}
		base := archiveName(version, t)
		var name string
		if t.GOOS == "windows" {
			name = base + ".zip"
			err = writeZip(filepath.Join(out, name), base, files)
		} else {
			name = base + ".tar.gz"
			err = writeTarGz(filepath.Join(out, name), base, files)
		}
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			return nil, err
		}
		h := sha256.Sum256(data)
		sums[name] = hex.EncodeToString(h[:])
		archives = append(archives, name)
	}
	if check {
		return nil, nil
	}
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		fmt.Fprintf(&sb, "%s  %s\n", sums[n], n)
	}
	return archives, os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sb.String()), 0o644)
}

var osExit = os.Exit

// parseOnly turns "linux/amd64,windows/arm64" into targets, each of which
// must be one of Targets.
func parseOnly(s string) ([]Target, error) {
	if s == "" {
		return Targets, nil
	}
	var ts []Target
	for _, f := range strings.Split(s, ",") {
		ok := false
		for _, t := range Targets {
			if t.String() == f {
				ts, ok = append(ts, t), true
			}
		}
		if !ok {
			return nil, fmt.Errorf("--only: %q is not a target of this release", f)
		}
	}
	return ts, nil
}

// RootCmd is the command line.
func RootCmd() *cobra.Command {
	var version, out, only string
	var check bool
	cmd := &cobra.Command{
		Use:           "reldist",
		Short:         "cross-build and archive the programs of a release",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ts, err := parseOnly(only)
			if err != nil {
				return err
			}
			license, _ := os.ReadFile("LICENSE")
			archives, err := Run(cmd.Context(), version, out, ts, check, license, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if check {
				fmt.Fprintf(cmd.OutOrStdout(), "built %d targets\n", len(ts))
				return nil
			}
			for _, a := range archives {
				fmt.Fprintln(cmd.OutOrStdout(), a)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "SHA256SUMS")
			return nil
		},
	}
	cmd.AddCommand(smokeCmd())
	f := cmd.Flags()
	f.StringVar(&version, "version", "", "release tag, vMAJOR.MINOR.PATCH (required unless --check)")
	f.StringVar(&out, "out", "dist", "output directory")
	f.StringVar(&only, "only", "", "comma-separated GOOS/GOARCH subset (default: all)")
	f.BoolVar(&check, "check", false, "only build, write no archive")
	return cmd
}

func main() {
	cmd := RootCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "reldist:", err)
		osExit(1)
	}
}

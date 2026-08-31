package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type target struct {
	os   string
	arch string
}

var releaseVersion = regexp.MustCompile(`^v1\.0\.0(?:-preview\.[1-9][0-9]*)?$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: go run ./tools/package <v1.0.0-preview.N|v1.0.0> <output-directory>")
	}
	version, output := args[0], args[1]
	if !releaseVersion.MatchString(version) {
		return fmt.Errorf("unsupported release version %q", version)
	}
	plainVersion := strings.TrimPrefix(version, "v")
	if err := prepareOutputDirectory(output); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "tunlite-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	darwinAMD64 := filepath.Join(temporary, "darwin-amd64-tunlite")
	darwinARM64 := filepath.Join(temporary, "darwin-arm64-tunlite")
	if err := build(darwinAMD64, plainVersion, target{os: "darwin", arch: "amd64"}); err != nil {
		return err
	}
	if err := build(darwinARM64, plainVersion, target{os: "darwin", arch: "arm64"}); err != nil {
		return err
	}
	darwinUniversal := filepath.Join(temporary, "macos-universal-tunlite")
	if err := mergeDarwinUniversal(darwinUniversal, darwinAMD64, darwinARM64); err != nil {
		return err
	}
	darwinArchive := filepath.Join(output, fmt.Sprintf("tunlite_%s_macos.tar.gz", plainVersion))
	if err := writeTarGZ(darwinArchive, darwinUniversal, "tunlite"); err != nil {
		return err
	}

	targets := []target{
		{os: "linux", arch: "amd64"},
		{os: "linux", arch: "arm64"},
	}
	archives := []string{darwinArchive}
	for _, item := range targets {
		binaryName := "tunlite"
		binaryPath := filepath.Join(temporary, item.os+"-"+item.arch+"-"+binaryName)
		if err := build(binaryPath, plainVersion, item); err != nil {
			return err
		}
		publicArch := item.arch
		if publicArch == "amd64" {
			publicArch = "x64"
		}
		baseName := fmt.Sprintf("tunlite_%s_%s_%s", plainVersion, item.os, publicArch)
		archivePath := filepath.Join(output, baseName+".tar.gz")
		if err := writeTarGZ(archivePath, binaryPath, binaryName); err != nil {
			return err
		}
		archives = append(archives, archivePath)
	}
	sort.Strings(archives)
	return writeChecksums(filepath.Join(output, "SHA256SUMS"), archives)
}

func prepareOutputDirectory(output string) error {
	info, err := os.Stat(output)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(output, 0o755)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("output path %q is not a directory", output)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("output directory %q must be empty", output)
	}
	return nil
}

func mergeDarwinUniversal(output, amd64, arm64 string) error {
	lipo, err := exec.LookPath("lipo")
	if err != nil {
		return errors.New("creating the macOS Universal 2 binary requires Apple's lipo; package releases on macOS")
	}
	command := exec.Command(lipo, "-create", "-output", output, amd64, arm64)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("create macOS Universal 2 binary: %w", err)
	}
	return nil
}

func build(output, version string, item target) error {
	goBinary := os.Getenv("GO")
	if goBinary == "" {
		goBinary = "go"
	}
	linkerFlags := "-s -w -X github.com/tunlite/tunlite/internal/cli.Version=" + version
	command := exec.Command(goBinary, "build", "-buildvcs=false", "-trimpath", "-ldflags", linkerFlags, "-o", output, "./cmd/tunlite")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+item.os, "GOARCH="+item.arch)
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s/%s: %w", item.os, item.arch, err)
	}
	return nil
}

func writeTarGZ(path, binaryPath, binaryName string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = time.Unix(0, 0)
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range releaseEntries(binaryPath, binaryName) {
		raw, err := os.ReadFile(entry.source)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Size: int64(len(raw)), ModTime: time.Unix(0, 0), Uid: 0, Gid: 0}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(raw); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	return file.Sync()
}

type releaseEntry struct {
	source string
	name   string
	mode   int64
}

func releaseEntries(binaryPath, binaryName string) []releaseEntry {
	return []releaseEntry{
		{source: binaryPath, name: binaryName, mode: 0o755},
		{source: "LICENSE", name: "LICENSE", mode: 0o644},
		{source: "README.md", name: "README.md", mode: 0o644},
	}
}

func writeChecksums(path string, archives []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, archive := range archives {
		raw, err := os.ReadFile(archive)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if _, err := fmt.Fprintf(file, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(archive)); err != nil {
			return err
		}
	}
	return file.Sync()
}

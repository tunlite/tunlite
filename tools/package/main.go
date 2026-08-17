package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "tunlite-package-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	targets := []target{
		{os: "darwin", arch: "amd64"},
		{os: "darwin", arch: "arm64"},
		{os: "linux", arch: "amd64"},
		{os: "linux", arch: "arm64"},
		{os: "windows", arch: "amd64"},
	}
	archives := make([]string, 0, len(targets))
	for _, item := range targets {
		binaryName := "tunlite"
		if item.os == "windows" {
			binaryName += ".exe"
		}
		binaryPath := filepath.Join(temporary, item.os+"-"+item.arch+"-"+binaryName)
		if err := build(binaryPath, plainVersion, item); err != nil {
			return err
		}
		baseName := fmt.Sprintf("tunlite_%s_%s_%s", plainVersion, item.os, item.arch)
		archivePath := filepath.Join(output, baseName+".tar.gz")
		if item.os == "windows" {
			archivePath = filepath.Join(output, baseName+".zip")
			if err := writeZIP(archivePath, binaryPath, binaryName); err != nil {
				return err
			}
		} else if err := writeTarGZ(archivePath, binaryPath, binaryName); err != nil {
			return err
		}
		archives = append(archives, archivePath)
	}
	sort.Strings(archives)
	return writeChecksums(filepath.Join(output, "SHA256SUMS"), archives)
}

func build(output, version string, item target) error {
	goBinary := os.Getenv("GO")
	if goBinary == "" {
		goBinary = "go"
	}
	linkerFlags := "-s -w -X github.com/yuanyuanzijin/tunlite/internal/cli.Version=" + version
	command := exec.Command(goBinary, "build", "-trimpath", "-ldflags", linkerFlags, "-o", output, "./cmd/tunlite")
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

func writeZIP(path, binaryPath, binaryName string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	for _, entry := range releaseEntries(binaryPath, binaryName) {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(os.FileMode(entry.mode))
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		output, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		input, err := os.Open(entry.source)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := writer.Close(); err != nil {
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

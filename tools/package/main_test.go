package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Catch dropped, transformed, executable, or nondeterministic Skill entries in real archives.
func TestArchiveBundlesCanonicalSkillDeterministically(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "tunlite")
	if err := os.WriteFile(binary, []byte("binary fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantSkill, err := os.ReadFile("skill/use-tunlite/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for i := 0; i < 2; i++ {
		path := filepath.Join(t.TempDir(), "release.tar.gz")
		if err := writeTarGZ(path, binary, "tunlite"); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, raw) {
			t.Fatal("archive output is not deterministic")
		}
		previous = raw
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		var names []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, h.Name)
			if h.Name == "skills/use-tunlite/SKILL.md" {
				data, err := io.ReadAll(tr)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, wantSkill) || h.Mode != 0o644 || h.Typeflag != tar.TypeReg {
					t.Fatalf("invalid bundled Skill: mode=%o type=%d", h.Mode, h.Typeflag)
				}
			}
		}
		gz.Close()
		if got := strings.Join(names, "\n"); got != "tunlite\nLICENSE\nREADME.md\nskills/use-tunlite/SKILL.md" {
			t.Fatalf("archive entries = %q", got)
		}
	}
}

// Recomputed checksums must not let a missing/mismatched Skill or unsafe member mode pass.
func TestVerifierValidatesSkillInEveryPlatformArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("verify-release.sh is only supported on macOS and Linux")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(root, "skill/use-tunlite/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"valid", "missing", "mismatched", "executable", "symlink"} {
		for _, platform := range []string{"linux_arm64", "linux_x64", "macos"} {
			t.Run(mutation+"/"+platform, func(t *testing.T) {
				dist := t.TempDir()
				var sums strings.Builder
				for _, target := range []string{"linux_arm64", "linux_x64", "macos"} {
					name := "tunlite_1.0.0-preview.1_" + target + ".tar.gz"
					var archive bytes.Buffer
					gz := gzip.NewWriter(&archive)
					tw := tar.NewWriter(gz)
					entries := []struct {
						name string
						data []byte
						mode int64
						kind byte
					}{
						{"tunlite", []byte("#!/bin/sh\nprintf '%s\\n' '{\"outcome\":\"succeeded\",\"protocolVersion\":1,\"version\":\"1.0.0-preview.1\"}'\n"), 0o755, tar.TypeReg},
						{"LICENSE", []byte("license"), 0o644, tar.TypeReg}, {"README.md", []byte("readme"), 0o644, tar.TypeReg},
						{"skills/use-tunlite/SKILL.md", skill, 0o644, tar.TypeReg},
					}
					if target == platform {
						switch mutation {
						case "missing":
							entries = entries[:3]
						case "mismatched":
							entries[3].data = []byte("wrong Skill version")
						case "executable":
							entries[3].mode = 0o755
						case "symlink":
							entries[3].kind = tar.TypeSymlink
							entries[3].data = nil
						}
					}
					for _, entry := range entries {
						if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: entry.kind, Linkname: "README.md", Size: int64(len(entry.data))}); err != nil {
							t.Fatal(err)
						}
						if _, err := tw.Write(entry.data); err != nil {
							t.Fatal(err)
						}
					}
					if err := tw.Close(); err != nil {
						t.Fatal(err)
					}
					if err := gz.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dist, name), archive.Bytes(), 0o644); err != nil {
						t.Fatal(err)
					}
					fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(archive.Bytes()), name)
				}
				if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				// Fixture binaries are shell scripts; only the Mach-O architecture check is replaced.
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "lipo"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("sh", filepath.Join(root, "scripts/verify-release.sh"), "v1.0.0-preview.1", dist)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
				out, err := cmd.CombinedOutput()
				if mutation == "valid" && err != nil {
					t.Fatalf("valid archive rejected: %v\n%s", err, out)
				}
				if mutation != "valid" && err == nil {
					t.Fatalf("%s Skill accepted in %s:\n%s", mutation, platform, out)
				}
				if mutation != "valid" {
					message := "unexpected archive member type or mode"
					if mutation == "missing" {
						message = "unexpected archive contents"
					}
					if mutation == "mismatched" {
						message = "bundled Skill differs from canonical source"
					}
					if !strings.Contains(string(out), message+" in tunlite_1.0.0-preview.1_"+platform+".tar.gz") {
						t.Fatalf("refused the wrong archive/reason:\n%s", out)
					}
				}
			})
		}
	}
}

func TestPrepareOutputDirectoryCreatesMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dist")
	if err := prepareOutputDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("output directory was not created: info=%#v err=%v", info, err)
	}
}

func TestPrepareOutputDirectoryAcceptsEmptyDirectory(t *testing.T) {
	if err := prepareOutputDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareOutputDirectoryRejectsStaleArtifacts(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "stale.tar.gz"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareOutputDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("expected stale output rejection, got %v", err)
	}
}

func TestPrepareOutputDirectoryRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dist")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := prepareOutputDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected file output rejection, got %v", err)
	}
}

package build

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docksmith/internal/runtime"
)

// ── COPY Layer ────────────────────────────────────────────────────────────────

// ProduceCOPYLayer resolves srcs (glob patterns relative to contextDir), places
// each matched file at its destination path inside the tar, and writes a
// reproducible tar layer to layersDir named by its SHA-256 digest.
//
// Returns (digest, byteSize, error).
// The tar contains only the files being added — it is a delta, not a snapshot.
func ProduceCOPYLayer(contextDir string, srcs []string, dest, layersDir string) (string, int64, error) {
	entries, err := collectCOPYEntries(contextDir, srcs, dest)
	if err != nil {
		return "", 0, err
	}
	return writeTarLayer(entries, layersDir)
}

// collectCOPYEntries resolves all source patterns and maps each file to its
// destination path inside the tar.
//
// Destination rules:
//   - Source is a directory → each file lands at dest/<relPath from that dir>
//   - Source is a single file, dest ends with '/' → dest/<basename>
//   - Source is a single file, dest is explicit → exact dest path
func collectCOPYEntries(contextDir string, srcs []string, dest string) ([]tarEntry, error) {
	var entries []tarEntry
	seen := make(map[string]struct{}) // guard against duplicate tar paths from overlapping globs

	for _, pattern := range srcs {
		absPattern := filepath.Join(contextDir, pattern)
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("pattern %q matched no files in build context", pattern)
		}

		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil {
				return nil, fmt.Errorf("stat %s: %w", match, err)
			}

			if info.IsDir() {
				// Walk the directory: each file goes to dest/<relPathFromMatch>
				if err := filepath.Walk(match, func(path string, fi os.FileInfo, err error) error {
					if err != nil {
						return err
					}
					rel, err := filepath.Rel(match, path)
					if err != nil {
						return err
					}
					tarPath := cleanTarPath(filepath.Join(dest, rel))
					if _, dup := seen[tarPath]; dup {
						return nil
					}
					seen[tarPath] = struct{}{}

					if fi.IsDir() {
						entries = append(entries, tarEntry{
							name:  tarPath,
							mode:  fi.Mode(),
							isDir: true,
						})
						return nil
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return fmt.Errorf("reading %s: %w", path, err)
					}
					entries = append(entries, tarEntry{name: tarPath, content: data, mode: fi.Mode()})
					return nil
				}); err != nil {
					return nil, err
				}
			} else {
				// Single file: dest is exact unless it ends with '/'
				tarPath := dest
				if strings.HasSuffix(dest, "/") {
					tarPath = filepath.Join(dest, filepath.Base(match))
				}
				tarPath = cleanTarPath(tarPath)
				if _, dup := seen[tarPath]; !dup {
					seen[tarPath] = struct{}{}
					data, err := os.ReadFile(match)
					if err != nil {
						return nil, fmt.Errorf("reading %s: %w", match, err)
					}
					entries = append(entries, tarEntry{name: tarPath, content: data, mode: info.Mode()})
				}
			}
		}
	}

	return entries, nil
}

// ── RUN Layer ─────────────────────────────────────────────────────────────────

// RUNLayerConfig is the contract between the build engine and the RUN layer producer.
type RUNLayerConfig struct {
	Command    []string // already-split command, e.g. ["/bin/sh", "-c", "pip install flask"]
	LayerPaths []string // ordered layer tar paths that form the rootfs (base first, newest last)
	WorkDir    string   // WORKDIR inside the container; "" defaults to "/"
	Env        []string // KEY=VALUE environment variables for the command
	LayersDir  string   // ~/.docksmith/layers — where to write the new layer tar
}

// ProduceRUNLayer implements the delta-capture build pattern:
//
//  1. Assemble rootfs from all existing layers.
//  2. Create WORKDIR inside the rootfs if it doesn't already exist (spec §3).
//  3. Snapshot the rootfs — record every regular file's sha256 (the "before" state).
//  4. Run the command in isolation using the same primitive as docksmith run.
//  5. Snapshot again — the "after" state.
//  6. Compute the delta: files that are new or whose content changed.
//  7. Write a deterministic tar of only the delta files → content-addressed layer.
//
// Returns (digest, byteSize, error).
// A non-zero exit code from the command is treated as a build failure.
func ProduceRUNLayer(cfg RUNLayerConfig) (string, int64, error) {
	// Step 1: assemble rootfs from existing layers
	rootfsPath, err := runtime.AssembleRootfs(cfg.LayerPaths)
	if err != nil {
		return "", 0, fmt.Errorf("assembling rootfs: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(rootfsPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to clean up rootfs %s: %v\n", rootfsPath, err)
		}
	}()

	// Step 2: create WORKDIR inside rootfs (spec §3 — silent, not stored as a layer)
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "/"
	}
	if err := os.MkdirAll(filepath.Join(rootfsPath, workDir), 0755); err != nil {
		return "", 0, fmt.Errorf("creating workdir %s in rootfs: %w", workDir, err)
	}

	// Step 3: snapshot before
	before, err := snapshotDir(rootfsPath)
	if err != nil {
		return "", 0, fmt.Errorf("pre-run snapshot failed: %w", err)
	}

	// Step 4: run the command using the same isolation as docksmith run
	runtimeCfg := runtime.ContainerConfig{
		Command: cfg.Command,
		Env:     cfg.Env,
		WorkDir: workDir,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
	}
	exitCode, err := runtime.RunIsolated(rootfsPath, runtimeCfg)
	if err != nil {
		return "", 0, fmt.Errorf("RUN command failed: %w", err)
	}
	if exitCode != 0 {
		return "", 0, fmt.Errorf("RUN command exited with non-zero code %d", exitCode)
	}

	// Step 5: snapshot after
	after, err := snapshotDir(rootfsPath)
	if err != nil {
		return "", 0, fmt.Errorf("post-run snapshot failed: %w", err)
	}

	// Step 6: compute delta — files that are new or have changed content
	var entries []tarEntry
	for relPath, newMeta := range after {
		oldMeta, existed := before[relPath]
		if existed && oldMeta.Hash == newMeta.Hash && oldMeta.Mode == newMeta.Mode {
			continue // unchanged: skip
		}
		absPath := filepath.Join(rootfsPath, relPath)
		fi, err := os.Lstat(absPath)
		if err != nil {
			continue // disappeared between snapshot and read (race); skip
		}
		if fi.IsDir() {
			entries = append(entries, tarEntry{
				name:  cleanTarPath(relPath),
				mode:  fi.Mode(),
				isDir: true,
			})
			continue
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return "", 0, fmt.Errorf("reading delta file %s: %w", relPath, err)
		}
		entries = append(entries, tarEntry{
			name:    cleanTarPath(relPath),
			content: data,
			mode:    fi.Mode(),
		})
	}

	// Step 7: write reproducible tar layer to layersDir
	return writeTarLayer(entries, cfg.LayersDir)
}

// ── Deterministic Tar Creation ─────────────────────────────────────────────────

// tarEntry is a single file or directory to be written into a layer tar.
type tarEntry struct {
	name    string      // path inside the tar, no leading '/'
	content []byte      // file bytes; nil for directories
	mode    os.FileMode // unix permissions
	isDir   bool
}

// writeTarLayer writes a reproducible tar of entries to layersDir.
//
// Reproducibility guarantees (spec §8):
//   - Entries are written in lexicographically sorted order by name.
//   - All timestamps (ModTime, AccessTime, ChangeTime) are zeroed — so the same
//     file content always produces the same tar bytes and therefore the same hash.
//   - UID/GID are forced to 0; Uname/Gname are cleared.
//
// Content-addressing: the tar is first written to an in-memory buffer, then
// SHA-256'd. The resulting file is named "sha256:<hash>" in layersDir.
// If a layer with that digest already exists, the write is skipped (idempotent).
//
// Returns (digest, byteSize, error).
func writeTarLayer(entries []tarEntry, layersDir string) (string, int64, error) {
	// Sort all entries by name — the single most critical reproducibility step.
	// Without this, map iteration order and directory walk order can vary between
	// runs, producing different tar byte sequences for identical content.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	zeroTime := time.Time{} // epoch zero — all timestamps set to this

	for _, e := range entries {
		if e.isDir {
			hdr := &tar.Header{
				Typeflag:   tar.TypeDir,
				Name:       e.name + "/",
				Mode:       int64(e.mode.Perm()),
				ModTime:    zeroTime,
				AccessTime: zeroTime,
				ChangeTime: zeroTime,
				// UID/GID left at 0 (root) — consistent across builds and machines
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return "", 0, fmt.Errorf("writing tar dir header %s: %w", e.name, err)
			}
			continue
		}

		hdr := &tar.Header{
			Typeflag:   tar.TypeReg,
			Name:       e.name,
			Size:       int64(len(e.content)),
			Mode:       int64(e.mode.Perm()),
			ModTime:    zeroTime,
			AccessTime: zeroTime,
			ChangeTime: zeroTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return "", 0, fmt.Errorf("writing tar header for %s: %w", e.name, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			return "", 0, fmt.Errorf("writing tar content for %s: %w", e.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return "", 0, fmt.Errorf("closing tar writer: %w", err)
	}

	tarBytes := buf.Bytes()

	// Content-address: SHA-256 of the raw tar bytes → filename
	h := sha256.New()
	h.Write(tarBytes)
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))

	layerPath := filepath.Join(layersDir, digest)

	// Idempotent write: if a layer with this digest already exists, the content
	// is guaranteed identical (same hash), so we skip the write.
	if _, err := os.Stat(layerPath); os.IsNotExist(err) {
		if err := os.WriteFile(layerPath, tarBytes, 0644); err != nil {
			return "", 0, fmt.Errorf("writing layer file %s: %w", digest, err)
		}
	}

	return digest, int64(len(tarBytes)), nil
}

// ── Filesystem Snapshot ────────────────────────────────────────────────────────

type snapshotMeta struct {
	Hash string
	Mode os.FileMode
}

// snapshotDir walks root and records every regular file as relPath → sha256 digest.
// Used to compute the filesystem delta before and after a RUN command.
// Directories are not hashed — their existence is implied by their files.
func snapshotDir(root string) (map[string]snapshotMeta, error) {
	result := make(map[string]snapshotMeta)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Only hash regular files. Symlinks, devices, sockets, etc. are excluded
		// from snapshot hashing to avoid open() failures on dangling links.
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, err := digestFile(path)
		if err != nil {
			return fmt.Errorf("hashing %s for snapshot: %w", path, err)
		}
		result[rel] = snapshotMeta{Hash: digest, Mode: info.Mode().Perm()}
		return nil
	})
	return result, err
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// cleanTarPath strips leading slashes and ./ prefixes from a path so it is
// valid as a tar entry name. Also normalises backslashes to forward slashes
// for portability.
//
// Examples:
//
//	"/app/main.py" → "app/main.py"
//	"./src/lib.go" → "src/lib.go"
func cleanTarPath(p string) string {
	p = filepath.ToSlash(p)          // Windows: \ → /
	p = strings.TrimLeft(p, "/")     // strip leading /
	p = strings.TrimPrefix(p, "./")  // strip ./
	return p
}

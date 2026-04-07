package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// cacheIndex maps a cache key (sha256:...) to the layer digest (sha256:...) produced
// by that step. Persisted as ~/.docksmith/cache/index.json.
type cacheIndex map[string]string

func loadCacheIndex(baseDir string) (cacheIndex, error) {
	path := filepath.Join(baseDir, "cache", "index.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(cacheIndex), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading cache index: %w", err)
	}
	var idx cacheIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing cache index: %w", err)
	}
	return idx, nil
}

func saveCacheIndex(baseDir string, idx cacheIndex) error {
	path := filepath.Join(baseDir, "cache", "index.json")
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// computeCacheKey builds the deterministic SHA-256 key described in spec §5.1.
//
// The five inputs are NUL-separated to prevent collisions between adjacent fields:
//
//  1. prevDigest  — digest of the last layer produced, or the base image manifest
//     digest for the very first layer-producing instruction. Ensures FROM changes
//     invalidate all downstream cache entries.
//
//  2. raw         — verbatim instruction text as written in the Docksmithfile.
//     Instruction text changes → that step and all below are misses.
//
//  3. workdir     — current WORKDIR value at this step (empty string if not yet set).
//     WORKDIR changes → that step and all below are misses.
//
//  4. envState    — all accumulated ENV key=value pairs, serialised in
//     lexicographically sorted key order. ENV changes → that step and all below.
//
//  5. fileSrcs    — COPY only: map of (sorted) relPath → SHA-256 of that file.
//     Source file content changes → that COPY step and all below. Nil for RUN.
func computeCacheKey(
	prevDigest, raw, workdir string,
	env map[string]string,
	fileSrcs map[string]string,
) string {
	h := sha256.New()

	fmt.Fprintf(h, "%s\x00", prevDigest)
	fmt.Fprintf(h, "%s\x00", raw)
	fmt.Fprintf(h, "%s\x00", workdir)

	// Sorted ENV pairs: deterministic regardless of insertion order
	envKeys := make([]string, 0, len(env))
	for k := range env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envPairs := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		envPairs = append(envPairs, k+"="+env[k])
	}
	fmt.Fprintf(h, "%s\x00", strings.Join(envPairs, "\n"))

	// COPY only: sorted file digests (nil means this is a RUN step)
	if fileSrcs != nil {
		paths := make([]string, 0, len(fileSrcs))
		for p := range fileSrcs {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			fmt.Fprintf(h, "%s:%s\x00", p, fileSrcs[p])
		}
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// digestFile returns "sha256:<hex>" of a file's raw bytes.
func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

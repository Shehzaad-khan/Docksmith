package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ── Manifest types ────────────────────────────────────────────────────────────
// These match the JSON format specified in §4.1 and are used by all members.

// ImageManifest is the complete JSON record stored in ~/.docksmith/images/<name>_<tag>.json.
type ImageManifest struct {
	Name    string         `json:"name"`
	Tag     string         `json:"tag"`
	Digest  string         `json:"digest"`
	Created string         `json:"created"`
	Config  ManifestConfig `json:"config"`
	Layers  []ManifestLayer `json:"layers"`
}

// ManifestConfig holds the per-image runtime configuration block.
type ManifestConfig struct {
	Env        []string `json:"Env"`        // ["KEY=value", ...]
	Cmd        []string `json:"Cmd"`        // default command; null if not set
	WorkingDir string   `json:"WorkingDir"` // defaults to "/" at runtime
}

// ManifestLayer is one entry in the layers array.
type ManifestLayer struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	CreatedBy string `json:"createdBy"`
}

// ── Live Build State ──────────────────────────────────────────────────────────

// BuildState tracks the mutable "live state" that evolves as each instruction
// is processed. It is the single source of truth for cache key inputs.
type BuildState struct {
	WorkDir string            // current WORKDIR; updated by WORKDIR instructions
	Env     map[string]string // accumulated ENV vars; updated by FROM (inherited) and ENV

	// internal fields — not exported; managed by the engine
	lastLayerDigest string // digest of the last COPY/RUN layer (or base manifest digest)
	cascadeMiss     bool   // once true, all remaining steps skip the cache (spec §5.2)
}

// buildConfig accumulates the non-layer fields for the final image manifest.
// Populated during the instruction loop; written to the manifest at the end.
type buildConfig struct {
	WorkingDir string
	Cmd        []string
	Env        map[string]string // only vars explicitly set in this Docksmithfile
}

// ── Build Engine ──────────────────────────────────────────────────────────────

// BuildEngine orchestrates one docksmith build invocation.
type BuildEngine struct {
	tag        string // "name:tag"
	contextDir string // directory containing Docksmithfile and all source files
	noCache    bool
	baseDir    string // ~/.docksmith
}

// NewEngine constructs a BuildEngine. Returns an error only if the home
// directory cannot be determined (i.e. the system is broken).
func NewEngine(tag, contextDir string, noCache bool) (*BuildEngine, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &BuildEngine{
		tag:        tag,
		contextDir: contextDir,
		noCache:    noCache,
		baseDir:    filepath.Join(homeDir, ".docksmith"),
	}, nil
}

// Run is the single entry point: parse → execute instructions → write manifest.
//
// Output format matches spec §5.2 example:
//
//	Step 1/3 : FROM alpine:3.18
//	Step 2/3 : COPY . /app [CACHE MISS] 0.09s
//	Step 3/3 : RUN echo "build complete" [CACHE MISS] 3.82s
//	Successfully built sha256:a3f9b2c1 myapp:latest (3.91s)
func (e *BuildEngine) Run() error {
	docksmithfilePath := filepath.Join(e.contextDir, "Docksmithfile")
	instructions, err := ParseFile(docksmithfilePath)
	if err != nil {
		return err
	}

	total := len(instructions)
	state := &BuildState{
		Env: make(map[string]string),
	}
	cfg := &buildConfig{
		Env: make(map[string]string),
	}

	// Load cache index once at the start; skip entirely when --no-cache is set.
	var cache cacheIndex
	if !e.noCache {
		if cache, err = loadCacheIndex(e.baseDir); err != nil {
			return fmt.Errorf("loading cache index: %w", err)
		}
	}

	// layers accumulates all layer entries for the final manifest.
	// FROM seeds it with the base image's layers; COPY/RUN append to it.
	var layers []ManifestLayer

	// allHits tracks whether every layer-producing step was a cache hit.
	// Used to preserve the original `created` timestamp on full-cache-hit rebuilds (spec §8).
	allHits := true

	buildStart := time.Now()

	for i, instr := range instructions {
		stepNum := i + 1

		switch v := instr.(type) {

		// ── FROM ──────────────────────────────────────────────────────────────
		// FROM never shows cache status or timing — it is not a layer-producing
		// step and performs no cache lookup (spec §5.2 note).
		case FromInstr:
			fmt.Printf("Step %d/%d : %s\n", stepNum, total, v.Raw())

			baseManifest, err := e.loadBaseImage(v.Image, v.Tag)
			if err != nil {
				return fmt.Errorf("step %d: %w", stepNum, err)
			}

			// Seed layer list from base image — FROM reuses these unchanged.
			layers = append(layers, baseManifest.Layers...)

			// The base image manifest digest anchors the cache chain.
			// Changing FROM (different image or tag) changes this digest, which
			// propagates to every downstream cache key.
			state.lastLayerDigest = baseManifest.Digest

			// Inherit base image runtime config as defaults.
			// Subsequent WORKDIR / ENV / CMD instructions override these.
			state.WorkDir = baseManifest.Config.WorkingDir
			for _, kv := range baseManifest.Config.Env {
				if parts := strings.SplitN(kv, "=", 2); len(parts) == 2 {
					state.Env[parts[0]] = parts[1]
				}
			}
			cfg.Cmd = baseManifest.Config.Cmd // default; overridden by CMD if present

		// ── WORKDIR ───────────────────────────────────────────────────────────
		// Updates live state only; does not produce a layer.
		// If the path does not exist in the assembled filesystem, the OS-level
		// executor will create it as an empty directory before the next COPY/RUN.
		case WorkdirInstr:
			fmt.Printf("Step %d/%d : %s\n", stepNum, total, v.Raw())
			state.WorkDir = v.Path
			cfg.WorkingDir = v.Path

		// ── ENV ───────────────────────────────────────────────────────────────
		// Stored in the image config; injected into every RUN during build and
		// every container started from this image. Does not produce a layer.
		case EnvInstr:
			fmt.Printf("Step %d/%d : %s\n", stepNum, total, v.Raw())
			state.Env[v.Key] = v.Value
			cfg.Env[v.Key] = v.Value

		// ── CMD ───────────────────────────────────────────────────────────────
		// Stores the default command in the image config. Does not produce a layer.
		case CmdInstr:
			fmt.Printf("Step %d/%d : %s\n", stepNum, total, v.Raw())
			cfg.Cmd = v.Parts

		// ── COPY ──────────────────────────────────────────────────────────────
		// Layer-producing step. Cache key includes file content hashes.
		case CopyInstr:
			stepStart := time.Now()

			// Hash every source file so the cache key reflects content, not just text.
			// A single changed byte → different hash → cache miss for this step and all below.
			fileSrcs, err := e.hashCopySources(v.Srcs)
			if err != nil {
				return fmt.Errorf("step %d: COPY: %w", stepNum, err)
			}

			cacheKey := computeCacheKey(
				state.lastLayerDigest, v.Raw(), state.WorkDir, state.Env, fileSrcs,
			)
			forceSkip := e.noCache || state.cascadeMiss

			hit, layerDigest := e.checkCache(cache, cacheKey, forceSkip)
			var layerSize int64

			if hit {
				// Size from disk — the tar already exists, just read its size.
				if fi, statErr := os.Stat(filepath.Join(e.baseDir, "layers", layerDigest)); statErr == nil {
					layerSize = fi.Size()
				}
				fmt.Printf("Step %d/%d : %s [CACHE HIT] %.2fs\n", stepNum, total, v.Raw(), time.Since(stepStart).Seconds())
			} else {
				allHits = false
				state.cascadeMiss = true

				// Produce a real tar layer from the copied files.
				newDigest, newSize, execErr := ProduceCOPYLayer(
					e.contextDir, v.Srcs, v.Dest,
					filepath.Join(e.baseDir, "layers"),
				)
				if execErr != nil {
					return fmt.Errorf("step %d: COPY failed: %w", stepNum, execErr)
				}
				layerDigest, layerSize = newDigest, newSize

				if !e.noCache && cache != nil {
					cache[cacheKey] = layerDigest
				}
				fmt.Printf("Step %d/%d : %s [CACHE MISS] %.2fs\n", stepNum, total, v.Raw(), time.Since(stepStart).Seconds())
			}

			state.lastLayerDigest = layerDigest
			layers = append(layers, ManifestLayer{Digest: layerDigest, Size: layerSize, CreatedBy: v.Raw()})

		// ── RUN ───────────────────────────────────────────────────────────────
		// Layer-producing step. Executes inside the assembled filesystem in isolation.
		case RunInstr:
			stepStart := time.Now()

			// RUN cache key does not include file hashes — only the command text,
			// workdir, env state, and the previous layer digest matter.
			cacheKey := computeCacheKey(
				state.lastLayerDigest, v.Raw(), state.WorkDir, state.Env, nil,
			)
			forceSkip := e.noCache || state.cascadeMiss

			hit, layerDigest := e.checkCache(cache, cacheKey, forceSkip)
			var layerSize int64

			if hit {
				if fi, statErr := os.Stat(filepath.Join(e.baseDir, "layers", layerDigest)); statErr == nil {
					layerSize = fi.Size()
				}
				fmt.Printf("Step %d/%d : %s [CACHE HIT] %.2fs\n", stepNum, total, v.Raw(), time.Since(stepStart).Seconds())
			} else {
				allHits = false
				state.cascadeMiss = true

				// Build the ordered list of layer tar paths that form the rootfs.
				// This is everything accumulated so far: base image layers + prior COPY/RUN layers.
				existingPaths := make([]string, 0, len(layers))
				for _, l := range layers {
					if l.Digest != "" {
						existingPaths = append(existingPaths, filepath.Join(e.baseDir, "layers", l.Digest))
					}
				}

				// Convert env map to KEY=VALUE slice for the runtime.
				envSlice := make([]string, 0, len(state.Env))
				for k, val := range state.Env {
					envSlice = append(envSlice, k+"="+val)
				}

				runCfg := RUNLayerConfig{
					// Wrap in /bin/sh -c so RUN works like shell commands, not raw exec.
					Command:    []string{"/bin/sh", "-c", v.Command},
					LayerPaths: existingPaths,
					WorkDir:    state.WorkDir,
					Env:        envSlice,
					LayersDir:  filepath.Join(e.baseDir, "layers"),
				}
				newDigest, newSize, execErr := ProduceRUNLayer(runCfg)
				if execErr != nil {
					return fmt.Errorf("step %d: RUN failed: %w", stepNum, execErr)
				}
				layerDigest, layerSize = newDigest, newSize

				if !e.noCache && cache != nil {
					cache[cacheKey] = layerDigest
				}
				fmt.Printf("Step %d/%d : %s [CACHE MISS] %.2fs\n", stepNum, total, v.Raw(), time.Since(stepStart).Seconds())
			}

			state.lastLayerDigest = layerDigest
			layers = append(layers, ManifestLayer{Digest: layerDigest, Size: layerSize, CreatedBy: v.Raw()})
		}
	}

	// Persist the updated cache index.
	if !e.noCache && cache != nil {
		if err := saveCacheIndex(e.baseDir, cache); err != nil {
			return fmt.Errorf("saving cache index: %w", err)
		}
	}

	manifest, err := e.writeManifest(state, cfg, layers, allHits)
	if err != nil {
		return err
	}

	// Print final summary: "sha256:" + first 12 hex chars = 19 chars total
	shortDigest := manifest.Digest
	if len(shortDigest) > 19 {
		shortDigest = shortDigest[:19]
	}
	fmt.Printf("Successfully built %s %s (%.2fs)\n", shortDigest, e.tag, time.Since(buildStart).Seconds())
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// loadBaseImage reads ~/.docksmith/images/<name>_<tag>.json and returns the parsed manifest.
// Fails with a clear error if the image is not found (spec §3 FROM requirement).
func (e *BuildEngine) loadBaseImage(name, tag string) (*ImageManifest, error) {
	path := filepath.Join(e.baseDir, "images", name+"_"+tag+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("FROM %s:%s — image not found in local store (~/.docksmith/images/)", name, tag)
		}
		return nil, fmt.Errorf("reading manifest for %s:%s: %w", name, tag, err)
	}
	var m ImageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest for %s:%s: %w", name, tag, err)
	}
	return &m, nil
}

// hashCopySources resolves every source glob relative to contextDir and returns
// a map of relPath → SHA-256 digest for every matched regular file.
// Directories are walked recursively; only regular files are hashed.
func (e *BuildEngine) hashCopySources(srcs []string) (map[string]string, error) {
	result := make(map[string]string)

	for _, pattern := range srcs {
		absPattern := filepath.Join(e.contextDir, pattern)
		matches, err := filepath.Glob(absPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("pattern %q matched no files in build context", pattern)
		}

		for _, match := range matches {
			if err := filepath.Walk(match, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil // descend into directories, but don't hash them
				}
				rel, err := filepath.Rel(e.contextDir, path)
				if err != nil {
					return err
				}
				digest, err := digestFile(path)
				if err != nil {
					return fmt.Errorf("hashing source file %s: %w", path, err)
				}
				result[rel] = digest
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

// checkCache returns (hit bool, layerDigest string).
//
// A cache HIT requires two conditions (spec §5.1):
//  1. The cache index has an entry for the given key.
//  2. The corresponding layer file exists on disk.
//
// Condition 2 guards against a rmi that deleted a shared layer — if the file
// is gone the cache entry is stale and we treat it as a miss.
//
// forceSkip is true when --no-cache was passed or a cascade miss is in effect.
func (e *BuildEngine) checkCache(idx cacheIndex, cacheKey string, forceSkip bool) (bool, string) {
	if forceSkip || idx == nil {
		return false, ""
	}
	digest, ok := idx[cacheKey]
	if !ok {
		return false, ""
	}
	// Verify the layer tar exists on disk — missing file = forced miss (spec §5.3)
	if _, err := os.Stat(filepath.Join(e.baseDir, "layers", digest)); err != nil {
		return false, ""
	}
	return true, digest
}

// writeManifest serialises the complete image manifest and writes it to
// ~/.docksmith/images/<name>_<tag>.json.
//
// Digest computation (spec §4.1):
//  1. Serialise the manifest with digest field set to "".
//  2. SHA-256 the resulting bytes.
//  3. Set digest to "sha256:<hex>" and write the final file.
//
// Created timestamp (spec §8):
// When allHits is true (every COPY/RUN was a cache hit), the existing manifest's
// `created` value is preserved so that identical rebuilds produce an identical
// manifest digest on the same machine.
func (e *BuildEngine) writeManifest(
	state *BuildState,
	cfg *buildConfig,
	layers []ManifestLayer,
	allHits bool,
) (*ImageManifest, error) {
	name, tag := e.tag, "latest"
	if idx := strings.Index(e.tag, ":"); idx != -1 {
		name = e.tag[:idx]
		tag = e.tag[idx+1:]
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	manifestPath := filepath.Join(e.baseDir, "images", name+"_"+tag+".json")

	if allHits {
		// Attempt to reuse the original `created` field from the existing manifest.
		// If it doesn't exist yet (first build), fall through to the current time.
		if data, err := os.ReadFile(manifestPath); err == nil {
			var existing ImageManifest
			if json.Unmarshal(data, &existing) == nil && existing.Created != "" {
				createdAt = existing.Created
			}
		}
	}

	// The final Config.Env is the full accumulated state (inherited + Docksmithfile ENVs).
	// Using state.Env ensures the child image carries all parent env vars forward.
	envKeys := make([]string, 0, len(state.Env))
	for k := range state.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	envList := make([]string, 0, len(envKeys))
	for _, k := range envKeys {
		envList = append(envList, k+"="+state.Env[k])
	}

	workingDir := cfg.WorkingDir
	if workingDir == "" {
		workingDir = state.WorkDir
	}

	manifest := ImageManifest{
		Name:    name,
		Tag:     tag,
		Digest:  "", // computed below
		Created: createdAt,
		Config: ManifestConfig{
			Env:        envList,
			Cmd:        cfg.Cmd,
			WorkingDir: workingDir,
		},
		Layers: layers,
	}

	// Step 1 & 2: serialise with digest="" → SHA-256
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("serialising manifest for digest computation: %w", err)
	}
	h := sha256.New()
	h.Write(raw)
	manifest.Digest = "sha256:" + hex.EncodeToString(h.Sum(nil))

	// Step 3: write final file with digest filled in
	finalData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("serialising final manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, finalData, 0644); err != nil {
		return nil, fmt.Errorf("writing manifest to %s: %w", manifestPath, err)
	}

	return &manifest, nil
}


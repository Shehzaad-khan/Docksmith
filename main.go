package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ==========================================
// REPEATABLE FLAG TYPE (for -e KEY=VALUE)
// ==========================================

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ", ")
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// ==========================================
// MANIFEST TYPES
// ==========================================

type Layer struct {
	Digest string `json:"digest"`
}

type Manifest struct {
	Name    string  `json:"name"`
	Tag     string  `json:"tag"`
	Digest  string  `json:"digest"`
	Created string  `json:"created"`
	Layers  []Layer `json:"layers"`
}

// ==========================================
// TASK 2: Local State Initialization
// ==========================================

func initStateDirs() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".docksmith")

	dirs := []string{
		filepath.Join(baseDir, "images"),
		filepath.Join(baseDir, "layers"),
		filepath.Join(baseDir, "cache"),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("could not create directory %s: %w", dir, err)
		}
	}

	return baseDir, nil
}

// ==========================================
// COMMAND HANDLERS
// ==========================================

func cmdBuild(args []string) {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	var tag string
	fs.StringVar(&tag, "t", "", "Name and optionally a tag (name:tag)")
	fs.StringVar(&tag, "tag", "", "Name and optionally a tag (name:tag)")
	noCache := fs.Bool("no-cache", false, "Skip all cache lookups and writes")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: docksmith build -t <name:tag> [--no-cache] <context>")
		fs.PrintDefaults()
	}

	fs.Parse(args)

	if tag == "" {
		fmt.Fprintln(os.Stderr, "Error: -t/--tag is required")
		fs.Usage()
		os.Exit(1)
	}

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: build context directory is required")
		fs.Usage()
		os.Exit(1)
	}

	context := fs.Arg(0)
	fmt.Printf("[Build Engine] Building image '%s' from context '%s'\n", tag, context)
	if *noCache {
		fmt.Println("[Build Engine] Notice: --no-cache flag detected. Skipping cache.")
	}
}

func cmdImages() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	imagesDir := filepath.Join(homeDir, ".docksmith", "images")

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error reading images directory:", err)
		os.Exit(1)
	}

	var manifestPaths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			manifestPaths = append(manifestPaths, filepath.Join(imagesDir, entry.Name()))
		}
	}

	if len(manifestPaths) == 0 {
		fmt.Println("No images found.")
		return
	}

	sort.Strings(manifestPaths)

	fmt.Printf("%-20s %-15s %-12s %s\n", "NAME", "TAG", "IMAGE ID", "CREATED")
	for _, path := range manifestPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		imageID := strings.TrimPrefix(m.Digest, "sha256:")
		if len(imageID) > 12 {
			imageID = imageID[:12]
		}
		fmt.Printf("%-20s %-15s %-12s %s\n", m.Name, m.Tag, imageID, m.Created)
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var envVars stringSlice
	fs.Var(&envVars, "e", "Override or add an environment variable (KEY=VALUE)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: docksmith run [-e KEY=VALUE] <image> [cmd...]")
		fs.PrintDefaults()
	}

	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: image name is required")
		fs.Usage()
		os.Exit(1)
	}

	image := fs.Arg(0)
	cmd := fs.Args()[1:]

	fmt.Printf("[Runtime] Assembling filesystem and running image '%s'\n", image)
	if len(envVars) > 0 {
		fmt.Printf("[Runtime] Environment Overrides applied: %v\n", []string(envVars))
	}
	if len(cmd) > 0 {
		fmt.Printf("[Runtime] Command Override applied: %s\n", strings.Join(cmd, " "))
	}
}

func cmdRmi(args []string) {
	fs := flag.NewFlagSet("rmi", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: docksmith rmi <image>")
	}

	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: image name is required")
		fs.Usage()
		os.Exit(1)
	}

	image := fs.Arg(0)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	imagesDir := filepath.Join(homeDir, ".docksmith", "images")
	layersDir := filepath.Join(homeDir, ".docksmith", "layers")

	name, tag := image, "latest"
	if strings.Contains(image, ":") {
		parts := strings.SplitN(image, ":", 2)
		name, tag = parts[0], parts[1]
	}

	manifestPath := filepath.Join(imagesDir, fmt.Sprintf("%s_%s.json", name, tag))
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: image '%s' not found.\n", image)
		os.Exit(1)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading manifest: %v\n", err)
		os.Exit(1)
	}

	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing manifest: %v\n", err)
		os.Exit(1)
	}

	for _, layer := range manifest.Layers {
		layerFile := filepath.Join(layersDir, layer.Digest)
		os.Remove(layerFile)
	}

	if err := os.Remove(manifestPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error removing manifest: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Untagged: %s\n", image)
}

// ==========================================
// TASK 1: Entry Point (CLI Parser)
// ==========================================

func main() {
	if _, err := initStateDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "Error initializing state:", err)
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: docksmith <command> [options]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  build    Build an image from a Docksmithfile")
		fmt.Fprintln(os.Stderr, "  images   List all images in the local store")
		fmt.Fprintln(os.Stderr, "  run      Run a command in a new container")
		fmt.Fprintln(os.Stderr, "  rmi      Remove an image manifest and its layers")
		os.Exit(1)
	}

	command := os.Args[1]
	subArgs := os.Args[2:]

	switch command {
	case "build":
		cmdBuild(subArgs)
	case "images":
		cmdImages()
	case "run":
		cmdRun(subArgs)
	case "rmi":
		cmdRmi(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown command '%s'\n", command)
		fmt.Fprintln(os.Stderr, "Run 'docksmith' for usage.")
		os.Exit(1)
	}
}

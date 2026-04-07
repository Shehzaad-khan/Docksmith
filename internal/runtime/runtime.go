package runtime

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// ContainerConfig is the single contract between Aahil's runtime
// and the rest of the team. Nobody touches isolation code except this file.
type ContainerConfig struct {
	LayerPaths []string  // ordered: base layer first, top layer last
	Command    []string  // command + args to exec inside container
	Env        []string  // KEY=VALUE strings, already merged (image + overrides)
	WorkDir    string    // working dir inside container, defaults to "/"
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
}

// AssembleRootfs is the exported interface for the build layer producer.
// It creates a fresh temp directory, extracts all layer tars in order into it,
// and returns the path. The caller is responsible for os.RemoveAll when done.
// Exported so the build engine can snapshot the rootfs before and after RUN
// without going through the full RunInContainer flow.
func AssembleRootfs(layerPaths []string) (string, error) {
	return assembleRootfs(layerPaths)
}

// RunIsolated is the exported interface for running a command inside a
// pre-assembled rootfs. The caller manages the rootfs lifecycle.
// Exported so the build RUN layer producer and docksmith run share
// the exact same isolation primitive (hard requirement per spec §6 and §8).
func RunIsolated(rootfsPath string, cfg ContainerConfig) (int, error) {
	return runIsolated(rootfsPath, cfg)
}

// RunInContainer is the one function the entire team calls.
// It assembles the rootfs, isolates the process, waits for exit,
// cleans up, and returns the exit code.
func RunInContainer(cfg ContainerConfig) (int, error) {
	rootfsPath, err := assembleRootfs(cfg.LayerPaths)
	if err != nil {
		return -1, fmt.Errorf("assembleRootfs: %w", err)
	}

	defer func() {
		if err := os.RemoveAll(rootfsPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cleanup failed for %s: %v\n", rootfsPath, err)
		}
	}()

	return runIsolated(rootfsPath, cfg)
}

// assembleRootfs extracts all layer tars in order into a fresh temp directory.
// Later layers overwrite earlier ones at the same path (union-fs behaviour).
func assembleRootfs(layerPaths []string) (string, error) {
	rootfsPath, err := os.MkdirTemp("", "docksmith-rootfs-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp rootfs dir: %w", err)
	}

	for _, layerPath := range layerPaths {
		if err := extractTar(layerPath, rootfsPath); err != nil {
			os.RemoveAll(rootfsPath)
			return "", fmt.Errorf("extracting layer %s: %w", layerPath, err)
		}
	}

	// Ensure essential mount-point directories exist inside the rootfs
	for _, dir := range []string{"proc", "dev", "sys", "tmp"} {
		os.MkdirAll(filepath.Join(rootfsPath, dir), 0755)
	}

	return rootfsPath, nil
}

// extractTar extracts a single tar archive into destDir.
func extractTar(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Prevent path traversal
		target := filepath.Join(destDir, filepath.Clean("/"+hdr.Name))

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
			os.Chmod(target, os.FileMode(hdr.Mode))
		case tar.TypeSymlink:
			os.Remove(target)
			os.Symlink(hdr.Linkname, target)
		}
	}
	return nil
}

// runIsolated spawns the process inside a new mount + PID + UTS namespace,
// chrooted into the assembled rootfs.
func runIsolated(rootfsPath string, cfg ContainerConfig) (int, error) {
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "/"
	}

	if len(cfg.Command) == 0 {
		return -1, fmt.Errorf("no command specified and no CMD defined in image")
	}

	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS |
			syscall.CLONE_NEWPID |
			syscall.CLONE_NEWUTS,
		Chroot: rootfsPath,
	}

	cmd.Dir    = workDir
	cmd.Env    = cfg.Env
	cmd.Stdout = cfg.Stdout
	cmd.Stderr = cfg.Stderr
	cmd.Stdin  = cfg.Stdin

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Non-zero exit is valid container behaviour, not a Go error
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("container process failed: %w", err)
	}
	return 0, nil
}

// MergeEnv merges image-level env with runtime -e overrides.
// Override values win. Called by Shehzaad's CLI before passing cfg to RunInContainer.
func MergeEnv(imageEnv []string, overrides []string) []string {
	envMap := map[string]string{}
	for _, kv := range imageEnv {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for _, kv := range overrides {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

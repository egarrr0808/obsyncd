package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func MaybeRelaunch(name string, args []string) error {
	if os.Getenv("OBSYNCD_NO_UPDATE") == "1" || os.Getenv("OBSYNCD_UPDATED") == "1" {
		return nil
	}
	repo, err := sourceDir()
	if err != nil {
		return nil
	}
	changed, err := updateRepo(repo)
	if err != nil || !changed {
		return err
	}
	installDir, err := installDir()
	if err != nil {
		return err
	}
	if err := build(repo, installDir); err != nil {
		return err
	}
	return relaunch(filepath.Join(installDir, name), args)
}

func sourceDir() (string, error) {
	if dir := os.Getenv("OBSYNCD_SRC_DIR"); dir != "" {
		return validSource(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return validSource(filepath.Join(home, "obsyncd"))
}

func validSource(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return "", err
	}
	return dir, nil
}

func updateRepo(repo string) (bool, error) {
	before, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	if err := git(repo, "fetch", "--quiet", "origin", "main"); err != nil {
		return false, err
	}
	afterRemote, err := gitOutput(repo, "rev-parse", "origin/main")
	if err != nil {
		return false, err
	}
	if before == afterRemote {
		return false, nil
	}
	if err := git(repo, "pull", "--ff-only", "--quiet", "origin", "main"); err != nil {
		return false, err
	}
	after, err := gitOutput(repo, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	return before != after, nil
}

func installDir() (string, error) {
	if dir := os.Getenv("OBSYNCD_INSTALL_DIR"); dir != "" {
		return mkdir(dir)
	}
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		if writable(dir) {
			return dir, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return mkdir(filepath.Join(home, ".local", "bin"))
}

func mkdir(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".obsyncd-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func build(repo, installDir string) error {
	for _, target := range []struct{ name, pkg string }{
		{"obsyncd", "./cmd/obsyncd"},
		{"obsyncctl", "./cmd/obsyncctl"},
	} {
		tmp := filepath.Join(installDir, "."+target.name+".new")
		cmd := exec.Command("go", "build", "-tags", "noassets", "-o", tmp, target.pkg)
		cmd.Dir = repo
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
		if err := os.Chmod(tmp, 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(installDir, target.name)); err != nil {
			return err
		}
	}
	return nil
}

func relaunch(exe string, args []string) error {
	if _, err := os.Stat(exe); err != nil {
		return err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(), "OBSYNCD_UPDATED=1")
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil
}

func git(repo string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func gitOutput(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	bs, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(bs)), nil
}

package promptinstall

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/promptfmt"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

type Result struct {
	Agent  runtimeconfig.AgentKind
	Name   string
	Path   string
	Status string
}

func InstallAll(agent runtimeconfig.AgentKind, force bool) ([]Result, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return nil, unsupportedAgentError(agent)
	}
	return installDescriptor(descriptor, force)
}

// InstallDiscovered installs prompts for each discovered agent in the supplied
// order. The descriptor list is normally produced by agent.DiscoverInstalled.
func InstallDiscovered(descriptors []agentpkg.Descriptor, force bool) ([]Result, error) {
	return operateDiscovered(descriptors, func(descriptor agentpkg.Descriptor) ([]Result, error) {
		return installDescriptor(descriptor, force)
	})
}

func installDescriptor(descriptor agentpkg.Descriptor, force bool) ([]Result, error) {
	target, err := descriptor.PromptDir()
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(prompts.Definitions()))
	for _, prompt := range prompts.Definitions() {
		if descriptor.UsesExtensionPrompts && isPiExtensionPrompt(prompt.Name) {
			path, err := piExtensionTarget()
			if err != nil {
				return nil, err
			}
			if err := installPiExtension(path, force); err != nil {
				return nil, err
			}
			results = append(results, Result{Agent: descriptor.Kind, Name: prompt.CommandName, Path: path, Status: "installed"})
			continue
		}
		content, err := installContent(descriptor, prompt)
		if err != nil {
			return nil, err
		}
		path := Path(target, prompt.CommandName)
		if err := Install(path, content, force); err != nil {
			return nil, err
		}
		results = append(results, Result{Agent: descriptor.Kind, Name: prompt.CommandName, Path: path, Status: "installed"})
	}
	if err := removeRetiredManagedPrompts(target); err != nil {
		return nil, err
	}
	return results, nil
}

func removeRetiredManagedPrompts(target string) error {
	for _, name := range [...]string{"web-slice"} {
		if _, err := removeManaged(Path(target, name), name); err != nil {
			return fmt.Errorf("remove retired prompt %q: %w", name, err)
		}
	}
	return nil
}

func CheckAll(agent runtimeconfig.AgentKind) ([]Result, error) {
	descriptor, ok := descriptorForAgent(agent)
	if !ok {
		return nil, unsupportedAgentError(agent)
	}
	return checkDescriptor(descriptor)
}

// CheckDiscovered checks prompts for each discovered agent in the supplied
// order. It preserves each agent's ordinary single-agent check semantics.
func CheckDiscovered(descriptors []agentpkg.Descriptor) ([]Result, error) {
	return operateDiscovered(descriptors, checkDescriptor)
}

func operateDiscovered(descriptors []agentpkg.Descriptor, operation func(agentpkg.Descriptor) ([]Result, error)) ([]Result, error) {
	var combined []Result
	for _, descriptor := range descriptors {
		results, err := operation(descriptor)
		if err != nil {
			return nil, fmt.Errorf("%s prompts: %w", descriptor.Label, err)
		}
		combined = append(combined, results...)
	}
	return combined, nil
}

func checkDescriptor(descriptor agentpkg.Descriptor) ([]Result, error) {
	target, err := descriptor.PromptDir()
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(prompts.Definitions()))
	for _, prompt := range prompts.Definitions() {
		if descriptor.UsesExtensionPrompts && isPiExtensionPrompt(prompt.Name) {
			path, err := piExtensionTarget()
			if err != nil {
				return nil, err
			}
			status, err := piExtensionStatus(path)
			if err != nil {
				return nil, err
			}
			results = append(results, Result{Agent: descriptor.Kind, Name: prompt.CommandName, Path: path, Status: status})
			continue
		}
		content, err := installContent(descriptor, prompt)
		if err != nil {
			return nil, err
		}
		path := Path(target, prompt.CommandName)
		status, err := Status(path, content)
		if err != nil {
			return nil, err
		}
		results = append(results, Result{Agent: descriptor.Kind, Name: prompt.CommandName, Path: path, Status: status})
	}
	return results, nil
}

func installContent(descriptor agentpkg.Descriptor, prompt prompts.Definition) (string, error) {
	if strings.TrimSpace(prompt.Template) == "" {
		return "", fmt.Errorf("prompt %q has empty %s content", prompt.Name, descriptor.TargetDescription)
	}
	content, err := renderInstallContent(descriptor, prompt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("prompt %q has empty %s content", prompt.Name, descriptor.TargetDescription)
	}
	return content, nil
}

func renderInstallContent(descriptor agentpkg.Descriptor, prompt prompts.Definition) (string, error) {
	if prompt.Name == prompts.PromptNote {
		content, err := descriptor.RenderPrompt(prompt.CommandName, prompt.Name, prompt.Template)
		if err != nil {
			return "", err
		}
		if descriptor.Kind == runtimeconfig.AgentClaude {
			content = strings.Replace(content, "allowed-tools: Bash(tao prompt note:*)", "allowed-tools: Bash(tao prompt note:*), Bash(tao note:*)", 1)
		}
		return content, nil
	}
	if prompt.Name != prompts.PromptCommit || descriptor.UsesExtensionPrompts {
		return descriptor.RenderPrompt(prompt.CommandName, prompt.Name, prompt.Template)
	}
	// Commit commands must run the Tao boundary from the provider's current
	// session. Inline this one prompt instead of dynamically invoking `tao
	// prompt`, which would hide the binary permissions and handoff contract.
	content, err := promptfmt.ManagedCodexCommand(prompt.CommandName, prompt.Name, prompt.Template)
	if err != nil {
		return "", err
	}
	switch descriptor.Kind {
	case runtimeconfig.AgentClaude:
		return strings.Replace(content, "---\n", "---\nallowed-tools: Bash(tao commit:*), Bash(mktemp:*), Bash(rm:*), Bash(rmdir:*), Read, Write\nargument-hint: [arguments]\n", 1), nil
	case runtimeconfig.AgentOpenCode:
		return strings.Replace(content, "---\n", "---\nagent: build\n", 1), nil
	default:
		return content, nil
	}
}

func isPiExtensionPrompt(name string) bool {
	return name == "commit"
}

func piExtensionTarget() (string, error) {
	agentDir, err := promptfmt.PiAgentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(agentDir, "extensions", "tao"), nil
}

func piExtensionSource() (string, error) {
	if dir := os.Getenv("TAO_PI_EXTENSION_DIR"); dir != "" {
		if ok, err := isPiExtensionSource(dir); ok || err != nil {
			return dir, err
		}
	}

	searched := make([]string, 0, 4)
	if wd, err := os.Getwd(); err == nil {
		searched = append(searched, wd)
		if source, ok := findPiExtensionSourceFrom(wd); ok {
			return source, nil
		}
	} else {
		return "", err
	}

	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		for _, dir := range []string{exeDir, filepath.Dir(exeDir)} {
			searched = append(searched, dir)
			if source, ok := findPiExtensionSourceFrom(dir); ok {
				return source, nil
			}
		}
	}

	if _, file, _, ok := runtime.Caller(0); ok {
		dir := filepath.Dir(file)
		searched = append(searched, dir)
		if source, ok := findPiExtensionSourceFrom(dir); ok {
			return source, nil
		}
	}

	return "", fmt.Errorf("could not find Tao Pi extension package from %s", strings.Join(searched, ", "))
}

func findPiExtensionSourceFrom(dir string) (string, bool) {
	for {
		candidate := filepath.Join(dir, "extensions", "pi")
		if ok, _ := isPiExtensionSource(candidate); ok {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func isPiExtensionSource(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, "package.json")) //nolint:gosec // G703: dir is a resolved local extension source path
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}

func installPiExtension(path string, force bool) error {
	source, err := piExtensionSource()
	if err != nil {
		return err
	}
	if existing, err := os.Readlink(path); err == nil {
		if samePath(existing, source) {
			return nil
		}
		if !force {
			return fmt.Errorf("%s exists and is not the Tao Pi extension; use --force to overwrite", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		if !force {
			return fmt.Errorf("%s exists and is not tao-managed; use --force to overwrite", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.Symlink(source, path)
}

func piExtensionStatus(path string) (string, error) {
	source, err := piExtensionSource()
	if err != nil {
		return "", err
	}
	existing, err := os.Readlink(path)
	if os.IsNotExist(err) {
		return "missing", nil
	}
	if err != nil {
		return "unmanaged", nil //nolint:nilerr // a readlink failure means the path is not a managed symlink
	}
	if samePath(existing, source) {
		return "current", nil
	}
	return "stale", nil
}

func samePath(a string, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return a == b
	}
	resolvedA, resolveErrA := filepath.EvalSymlinks(absA)
	resolvedB, resolveErrB := filepath.EvalSymlinks(absB)
	if resolveErrA == nil && resolveErrB == nil {
		return resolvedA == resolvedB
	}
	return absA == absB
}

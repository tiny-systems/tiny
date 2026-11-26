package sessions

import (
	"context"
	"fmt"

	"github.com/tiny-systems/tiny/internal/settings"
	"github.com/tiny-systems/tiny/internal/workload"
)

// DefaultSandbox is the runtime class `tiny sandbox on` picks when none is
// named: gVisor, as installed by k3s recipes and GKE Sandbox alike.
const DefaultSandbox = "gvisor"

// SandboxOn makes every new session in the namespace run under the named
// runtime class. It refuses a class the cluster doesn't have, since every
// new session would then fail to start. Running sessions keep the runtime
// they started with.
func (s *Store) SandboxOn(ctx context.Context, class string) error {
	if class == "" {
		class = DefaultSandbox
	}
	if class == workload.NoRuntimeClass {
		return fmt.Errorf("%q means no sandbox: use tiny sandbox off", class)
	}
	ok, err := workload.RuntimeClassExists(ctx, s.Kube.Client, class)
	if err != nil {
		return fmt.Errorf("runtime class %q: %w", class, err)
	}
	if !ok {
		return fmt.Errorf("runtime class %q does not exist on this cluster: install the sandbox first (gVisor: runsc and a RuntimeClass named gvisor)", class)
	}
	return s.setSettingKeys(ctx, map[string]string{settings.KeyRuntimeClass: class})
}

// SandboxOff puts new sessions back on the node's default runtime.
func (s *Store) SandboxOff(ctx context.Context) error {
	return s.setSettingKeys(ctx, map[string]string{settings.KeyRuntimeClass: ""})
}

// SandboxState returns the runtime class new sessions get, "" when off.
func (s *Store) SandboxState(ctx context.Context) (string, error) {
	cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
	return cfg.RuntimeClass, err
}

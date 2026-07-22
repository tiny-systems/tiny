package repo

import (
	"context"
	"fmt"
	"strings"
)

// InstalledModules reports what already occupies a release name in the
// namespace. It exists for the two things a rename cannot do without looking
// first (design §7.1):
//
//   - ADOPT — a cluster installed before the publisher coordinate holds
//     `http-module-v0`. Installing the newly-computed
//     `tinysystems-http-module-v0` would stand a second copy beside it while
//     every existing flow node stays bound to the old name. Adopting the legacy
//     release keeps those flows working and makes the change a non-event.
//   - GUARD — publisher names may contain dashes, so two different pairs can
//     generate one release name ("a-b"+"c" and "a"+"b-c"). Installing over it
//     would silently replace someone else's module.
//
// Optional: nil skips both (fresh-cluster semantics, and what unit tests use).
type InstalledModules interface {
	// Lookup returns the module image ref of whatever occupies release in
	// namespace (`ghcr.io/<org>/<module>:<tag>`) — the authoritative answer to
	// "whose module is this?", since it is literally what the pod runs. Returns
	// found=false when no such release exists, and an empty image when the
	// release exists but its image could not be read.
	//
	// Primitives, not a struct, so implementations satisfy this structurally
	// without importing this package — same pattern as BaseValues and Helm.
	Lookup(ctx context.Context, namespace, release string) (image string, found bool, err error)
}

// reconcileIdentity settles which release name this install writes to, mutating
// the plan in place.
//
// Adoption is deliberately narrow: it only ever moves the plan BACK to a legacy
// unqualified name already running this same image. It never invents a name and
// never hops between two publisher-qualified names, so it cannot be used to
// take over another publisher's release.
func reconcileIdentity(ctx context.Context, installed InstalledModules, resolved *Resolved, plan *InstallPlan) error {
	if installed == nil {
		return nil
	}

	// Whatever currently owns the name we intend to write to.
	currentImage, found, err := installed.Lookup(ctx, plan.Namespace, plan.ReleaseName)
	if err != nil {
		return fmt.Errorf("check installed release %s: %w", plan.ReleaseName, err)
	}
	if found {
		if !sameModuleImage(currentImage, plan.Image) {
			return fmt.Errorf(
				"release %q in namespace %s already runs %s — refusing to overwrite another publisher's module; "+
					"rename one of the repos in repos.yaml so the two identities differ",
				plan.ReleaseName, plan.Namespace, displayImage(currentImage))
		}
		return nil // ours: plain upgrade
	}

	// Nothing at the qualified name. Adopt a legacy release still serving this
	// module rather than standing a duplicate beside it.
	major, err := resolved.Version.Major()
	if err != nil {
		return err
	}
	legacy := ReleaseName("", resolved.Name, major)
	if legacy == plan.ReleaseName {
		return nil // no publisher coordinate in play; nothing to adopt
	}

	priorImage, found, err := installed.Lookup(ctx, plan.Namespace, legacy)
	if err != nil {
		return fmt.Errorf("check legacy release %s: %w", legacy, err)
	}
	if !found || !sameModuleImage(priorImage, plan.Image) {
		return nil // fresh install under the qualified name
	}

	plan.ReleaseName = legacy
	for i := range plan.Bundles {
		plan.Bundles[i].ReleaseName = legacy + "-" + plan.Bundles[i].Name
	}
	return nil
}

// sameModuleImage compares two image refs ignoring the tag/digest, so an
// upgrade (0.5.25 → 0.5.26) still counts as the same module while a different
// org or module name does not.
//
// An unknown installed image (empty) is treated as a match: we could not read
// it, and refusing to upgrade a release we probably own is worse than the rare
// case of adopting one we don't.
func sameModuleImage(installed, planned string) bool {
	if installed == "" {
		return true
	}
	return imageRepo(installed) == imageRepo(planned)
}

// imageRepo strips the tag or digest from an image ref, leaving host/path.
func imageRepo(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	// A colon is only a tag separator after the last slash (host:port exists).
	if slash := strings.LastIndexByte(ref, '/'); slash >= 0 {
		if colon := strings.IndexByte(ref[slash:], ':'); colon >= 0 {
			ref = ref[:slash+colon]
		}
	} else if colon := strings.IndexByte(ref, ':'); colon >= 0 {
		ref = ref[:colon]
	}
	return ref
}

func displayImage(image string) string {
	if image == "" {
		return "an unknown image"
	}
	return image
}

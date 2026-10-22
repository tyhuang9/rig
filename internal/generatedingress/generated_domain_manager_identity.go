package generatedingress

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// generatedLegacyRootObservation is descriptive, read-only construction
// evidence. It neither owns the path nor grants gateway mutation authority.
type generatedLegacyRootObservation struct {
	dataRootPath string
	ingressPath  string
	dataRoot     os.FileInfo
	ingressRoot  os.FileInfo
	ancestors    map[string]os.FileInfo
	runner       runtimeprocess.CommandRunner
	dockerEnv    []string
	options      Options
	namespace    generatedGatewayNamespace
}

func inspectGeneratedLegacyRootObservation(
	runner runtimeprocess.CommandRunner,
	options Options,
) (generatedLegacyRootObservation, error) {
	if runner == nil || !validOptions(options) {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction identity is invalid")
	}
	store, present, ancestors, err := inspectStateStoreReadOnly(options.DataRoot)
	if err != nil {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction path is unsafe or unreadable")
	}
	ingressPath := filepath.Join(options.DataRoot, "runtime", "generated-ingress")
	observation := generatedLegacyRootObservation{
		dataRootPath: options.DataRoot,
		ingressPath:  ingressPath,
		dataRoot:     ancestors[options.DataRoot],
		ancestors:    cloneGeneratedDomainPathIdentities(ancestors),
		runner:       runner,
		options:      options,
		namespace:    fixedGeneratedGatewayNamespace(),
	}
	if present {
		if store == nil || store.root != ingressPath || ancestors[ingressPath] == nil {
			return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity is invalid")
		}
		observation.ingressRoot = ancestors[ingressPath]
	}
	return observation, nil
}

// recheck permits only the Manager constructor's missing-to-directory
// transition. Every identity already observed at admission must remain exact.
func (o generatedLegacyRootObservation) recheck(requireIngress bool) (generatedLegacyRootObservation, error) {
	current, err := inspectGeneratedLegacyRootObservation(o.runner, o.options)
	if err != nil || current.dataRootPath != o.dataRootPath || current.ingressPath != o.ingressPath ||
		current.namespace != o.namespace || o.dataRoot != nil && !sameGeneratedDomainFileIdentity(o.dataRoot, current.dataRoot) ||
		o.dataRoot == nil && !requireIngress && current.dataRoot != nil {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
	}
	if !requireIngress && len(o.ancestors) != len(current.ancestors) {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
	}
	for path, previous := range o.ancestors {
		present := current.ancestors[path]
		if !sameGeneratedDomainFileIdentity(previous, present) {
			return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
		}
		if previous.Mode() != present.Mode() {
			// Existing Manager construction has always tightened its exact state
			// directory to 0700. Admit only that protection change on the same
			// retained directory; ancestor modes and every other transition stay
			// immutable.
			protectionOnly := requireIngress && path == o.ingressPath && o.ingressRoot != nil &&
				present.Mode().Perm() == 0o700 &&
				previous.Mode()&^os.ModePerm == present.Mode()&^os.ModePerm
			if !protectionOnly {
				return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
			}
		}
	}
	if o.ingressRoot != nil {
		if !sameGeneratedDomainFileIdentity(o.ingressRoot, current.ingressRoot) {
			return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
		}
	} else if requireIngress && current.ingressRoot == nil {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction path is absent")
	} else if !requireIngress && current.ingressRoot != nil {
		return generatedLegacyRootObservation{}, errors.New("generated ingress construction path identity changed")
	}
	if requireIngress && current.dataRoot == nil {
		return generatedLegacyRootObservation{}, errors.New("generated ingress data root is absent")
	}
	current.dockerEnv = append([]string(nil), o.dockerEnv...)
	return current, nil
}

func inspectGeneratedLegacyManagerIdentity(
	ctx context.Context,
	manager *Manager,
	expectedView *generatedManagerView,
) (generatedManagerPhysicalIdentity, generatedLegacyRootObservation, error) {
	if ctx == nil || manager == nil || expectedView == nil || manager.domainView != expectedView || manager.store == nil ||
		manager.store.root != filepath.Join(manager.options.DataRoot, "runtime", "generated-ingress") ||
		!manager.validWorkingDirectory() || manager.runner == nil {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{},
			errors.New("generated ingress manager identity is invalid")
	}
	expectedView.mu.Lock()
	root := expectedView.root
	expectedView.mu.Unlock()
	if !sameGeneratedPointer(root.runner, manager.runner) || !sameGeneratedManagerIdentityOptions(root.options, manager.options) {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{},
			errors.New("generated ingress manager configuration changed")
	}
	if !slices.Equal(root.dockerEnv, manager.dockerEnv) {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{},
			errors.New("generated ingress Docker environment changed")
	}
	first, err := root.recheck(true)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{}, err
	}
	daemonID, err := inspectGeneratedDockerDaemonIdentity(
		ctx,
		manager.runner,
		manager.options.DockerExecutable,
		manager.dockerEnv,
		manager.options.CommandTimeout,
	)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{}, err
	}
	second, err := first.recheck(true)
	if err != nil {
		return generatedManagerPhysicalIdentity{}, generatedLegacyRootObservation{}, err
	}
	return generatedManagerPhysicalIdentity{
		dataRoot: second.dataRoot, ingressRoot: second.ingressRoot,
		daemonID: daemonID, namespace: second.namespace,
	}, second, nil
}

func sameGeneratedManagerIdentityOptions(left, right Options) bool {
	return left.DockerExecutable == right.DockerExecutable && left.DockerEndpoint == right.DockerEndpoint &&
		left.DockerConfigDirectory == right.DockerConfigDirectory && left.WorkingDirectory == right.WorkingDirectory &&
		left.DataRoot == right.DataRoot && left.HostPort == right.HostPort &&
		left.CommandTimeout == right.CommandTimeout && left.PullTimeout == right.PullTimeout &&
		left.OutputLimit == right.OutputLimit
}

func generatedLegacyObservationRootConflict(
	legacy generatedLegacyRootObservation,
	domain generatedDomainReservationInput,
) bool {
	return legacy.dataRootPath == domain.options.DataRoot ||
		sameGeneratedDomainFileIdentity(legacy.dataRoot, domain.physical.manager.dataRoot) ||
		sameGeneratedDomainFileIdentity(legacy.ingressRoot, domain.physical.manager.ingressRoot)
}

package generatedingress

import "context"

const gatewayRebindFinalHandoverPlanContext = "hostd/generated-ingress/rebind/final-handover-plan/v1"

type gatewayRebindHandoverContainerState string

const (
	gatewayRebindHandoverContainerAbsent  gatewayRebindHandoverContainerState = "absent"
	gatewayRebindHandoverContainerStopped gatewayRebindHandoverContainerState = "stopped"
	gatewayRebindHandoverContainerRunning gatewayRebindHandoverContainerState = "running"
)

type gatewayRebindPredecessorAddressState string

const (
	gatewayRebindPredecessorAddressPresent   gatewayRebindPredecessorAddressState = "present"
	gatewayRebindPredecessorAddressAbsent    gatewayRebindPredecessorAddressState = "absent"
	gatewayRebindPredecessorAddressAmbiguous gatewayRebindPredecessorAddressState = "ambiguous"
)

type gatewayRebindHandoverApplicationNetwork struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// The plan pins the exact final create command and every application network
// before withdrawing the bound stage. Config bytes and the complete route plan
// remain in sequence eleven; the digest of sequence twelve pins that history.
type gatewayRebindFinalHandoverPlan struct {
	PredecessorObservationDigest string                                    `json:"predecessorObservationDigest"`
	PredecessorInitiallyRunning  bool                                      `json:"predecessorInitiallyRunning"`
	Version                      int                                       `json:"version"`
	Context                      string                                    `json:"context"`
	ProtectedIntentDigest        string                                    `json:"protectedIntentDigest"`
	SequenceTwelveDigest         string                                    `json:"sequenceTwelveDigest"`
	PredecessorDigest            string                                    `json:"predecessorDigest"`
	RoutePlanDigest              string                                    `json:"routePlanDigest"`
	FinalConfigDigest            string                                    `json:"finalConfigDigest"`
	FinalOwnershipDigest         string                                    `json:"finalOwnershipDigest"`
	FinalConfigurationDigest     string                                    `json:"finalConfigurationDigest"`
	LocalHostPort                uint16                                    `json:"localHostPort"`
	ApplicationNetworks          []gatewayRebindHandoverApplicationNetwork `json:"applicationNetworks"`
	Digest                       string                                    `json:"digest"`
}

type gatewayRebindFinalContainerBinding struct {
	ID                  string `json:"id"`
	OwnershipDigest     string `json:"ownershipDigest"`
	ConfigurationDigest string `json:"configurationDigest"`
}

// Context is invocation-local. CreatedFinalID is allowed only while reading
// back a successful create response before installing its immutable binding.
// It never authorizes a mutation and must never be recovered by name or label.
type gatewayRebindFinalHandoverContext struct {
	Intent         gatewayRebindProtectedIntent
	SequenceTwelve gatewayRebindProgressRecord
	Predecessor    gatewayUpgradeGenerationSelection
	Source         routeState
	Phase          gatewayRebindProgressPhase
	Plan           *gatewayRebindFinalHandoverPlan
	Final          *gatewayRebindFinalContainerBinding
	CreatedFinalID string
}

// Observation contains only independently validated, normalized evidence.
// Failed application-route proof leaves RoutesDigest empty while retaining an
// exact owned physical state, allowing guarded compensation. Unknown ownership,
// unreadable observations and unstable inventories instead return an error.
type gatewayRebindFinalHandoverObservation struct {
	PredecessorObservationDigest string                                    `json:"predecessorObservationDigest"`
	Stage                        gatewayRebindHandoverContainerState       `json:"stage"`
	Final                        gatewayRebindHandoverContainerState       `json:"final"`
	FinalID                      string                                    `json:"finalId,omitempty"`
	PredecessorRunning           bool                                      `json:"predecessorRunning"`
	PredecessorAddress           gatewayRebindPredecessorAddressState      `json:"predecessorAddress"`
	ConfigVolumePresent          bool                                      `json:"configVolumePresent"`
	DataVolumePresent            bool                                      `json:"dataVolumePresent"`
	IngressNetworkPresent        bool                                      `json:"ingressNetworkPresent"`
	ApplicationNetworks          []gatewayRebindHandoverApplicationNetwork `json:"applicationNetworks"`
	ConfigDigest                 string                                    `json:"configDigest,omitempty"`
	RoutesDigest                 string                                    `json:"routesDigest,omitempty"`
	PredecessorRoutesDigest      string                                    `json:"predecessorRoutesDigest,omitempty"`
	PredecessorStopDigest        string                                    `json:"predecessorStopDigest,omitempty"`
	HostDigest                   string                                    `json:"hostDigest"`
	InventoryDigest              string                                    `json:"inventoryDigest"`
	ApplicationEndpointsDigest   string                                    `json:"applicationEndpointsDigest"`
	Digest                       string                                    `json:"digest"`
}

type gatewayRebindFinalHandoverDriver interface {
	gatewayRebindFinalConfigCopyDriver
	observeHandover(context.Context, gatewayRebindFinalHandoverContext) (gatewayRebindFinalHandoverObservation, error)
	createFinal(context.Context, gatewayRebindFinalHandoverContext) (string, error)
	stopStage(context.Context, gatewayRebindFinalHandoverContext) error
	removeStage(context.Context, gatewayRebindFinalHandoverContext) error
	stopPredecessor(context.Context, gatewayRebindFinalHandoverContext) error
	startPredecessor(context.Context, gatewayRebindFinalHandoverContext) error
	startFinal(context.Context, gatewayRebindFinalHandoverContext) error
	stopFinal(context.Context, gatewayRebindFinalHandoverContext) error
	removeFinal(context.Context, gatewayRebindFinalHandoverContext) error
	removeConfigVolume(context.Context, gatewayRebindFinalHandoverContext) error
	removeDataVolume(context.Context, gatewayRebindFinalHandoverContext) error
	removeIngressNetwork(context.Context, gatewayRebindFinalHandoverContext) error
}

func gatewayRebindFinalHandoverObservationDigest(value gatewayRebindFinalHandoverObservation) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

func gatewayRebindFinalHandoverPlanDigest(value gatewayRebindFinalHandoverPlan) (string, error) {
	value.Digest = ""
	return canonicalDigest(value)
}

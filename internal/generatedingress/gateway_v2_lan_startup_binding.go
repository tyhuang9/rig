package generatedingress

import "github.com/hostd/hostd/internal/appaccess"

// GatewayV2LANStartupBindingProjection carries the SQL resolver's effective
// binding without changing the immutable grant or disable request. Current
// and retained projections have separate fields on startup claims: retained
// history cannot authorize serving. The runtime must validate this projection
// against protected lineage before using it.
type GatewayV2LANStartupBindingProjection struct {
	EffectiveProfile       GatewayV2ProfileBinding
	GatewaySource          appaccess.GatewayCurrentAuthorityRef
	TransferChain          []appaccess.GatewayRebindAllocationTransfer
	TransferChainTipDigest string
	TerminalReceiptDigest  string
}

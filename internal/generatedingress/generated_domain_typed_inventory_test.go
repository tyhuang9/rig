package generatedingress

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"testing"
)

func TestGeneratedDomainTypedMutationInventoryIsClosed(t *testing.T) {
	expected := []string{
		"AttestGatewayV2LANDisableSuccessor",
		"CommitGatewayRebind",
		"DisableGatewayV2LAN",
		"FinalizeGatewayV2PreparationAbort",
		"FinalizeGatewayV2Rollback",
		"GrantGatewayV2LAN",
		"QuarantineGatewayV2LANAccessRecoveryBatch",
		"QuarantineGatewayV2LANAccessStartup",
		"QuarantineGatewayV2LANStartup",
		"ReconcileGatewayRebind",
		"RecoverGatewayRebindStartup",
		"RecoverGatewayV2LAN",
		"RestoreGatewayCurrentServingStartup",
		"RetireGatewayCurrentPredecessorsStartup",
		"RetireGatewayV2LANRecoveryBatch",
		"StageGatewayV2",
		"TransferGatewayV2",
		"UpgradeGatewayV2",
		"WithGatewayV2LANAbsenceResolution",
		"WithGatewayV2LANCommitRecovery",
		"WithGatewayV2LANCommitResolution",
		"WithGatewayV2LANDisableFinalization",
		"WithGatewayV2LANDisableResolution",
		"WithGatewayV2LANRecoveryDisableFinalization",
		"WithGatewayV2LANRecoveryGrantFinalization",
		"WithGatewayV2LANResolution",
		"WithGatewayV2LANRollbackResolution",
	}
	sort.Strings(expected)

	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info fs.FileInfo) bool {
		return len(info.Name()) < 8 || info.Name()[len(info.Name())-8:] != "_test.go"
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	parsed := packages["generatedingress"]
	if parsed == nil {
		t.Fatal("generatedingress package was not parsed")
	}
	calls := make(map[string][]string)
	for _, file := range parsed.Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Body == nil || len(function.Recv.List) != 1 {
				continue
			}
			isManager := false
			switch receiver := function.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				identifier, _ := receiver.X.(*ast.Ident)
				isManager = identifier != nil && identifier.Name == "Manager"
			case *ast.Ident:
				isManager = receiver.Name == "Manager"
			}
			if !isManager {
				continue
			}
			if _, present := calls[function.Name.Name]; !present {
				calls[function.Name.Name] = nil
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok {
					calls[function.Name.Name] = append(calls[function.Name.Name], selector.Sel.Name)
				}
				return true
			})
		}
	}

	for _, method := range expected {
		if !generatedDomainInventoryReachesGate(method, calls, make(map[string]bool)) {
			t.Errorf("typed Manager method %s does not reach enterGeneratedDomainTypedMutation", method)
		}
	}
	ordinaryMethods := []string{"Switch", "Provision", "Recover"}
	for _, ordinary := range ordinaryMethods {
		if generatedDomainInventoryReachesGate(ordinary, calls, make(map[string]bool)) {
			t.Errorf("ordinary Manager method %s unexpectedly reaches the typed domain gate", ordinary)
		}
	}
	allowedReadOnly := []string{
		"HasGatewayV2LANRecoveryBatch", "InspectGatewayRebindCommitReview", "InspectGatewayRebindCurrent",
		"InspectGatewayRebindOperation", "InspectGatewayRebindPreclaimDockerPredecessor",
		"InspectGatewayRebindPreclaimPredecessor", "InspectGatewayRebindPreclaimSuccessorPreflight",
		"InspectGatewayRebindPredecessor", "InspectGatewayRebindPreparedDockerPredecessor",
		"InspectGatewayRebindProposal", "InspectGatewayRebindRecoveryReview",
		"InspectGatewayRebindSuccessorPreflight", "InspectGatewayV2LANAccessStartup",
		"InspectGatewayV2LANStartup", "InspectGatewayV2Startup", "InspectGatewayV2StartupPreparedRebind",
		"Observe", "ObserveGatewayV2LAN", "ObserveGatewayV2LANDisable", "ObserveGatewayV2LANRecoveryHead",
		"ObserveGatewayV2Operation", "Snapshot", "TraceGatewayV2LANGrantForLiveDocker", "URL",
		"WithGatewayV2LANObservation", "WithObservation",
	}
	classified := make(map[string]bool)
	for _, group := range [][]string{expected, ordinaryMethods, allowedReadOnly} {
		for _, method := range group {
			classified[method] = true
		}
	}
	for method := range calls {
		if ast.IsExported(method) && !classified[method] {
			t.Errorf("exported Manager method %s is absent from the typed/read-only/ordinary inventory", method)
		}
	}
}

func generatedDomainInventoryReachesGate(method string, calls map[string][]string, visited map[string]bool) bool {
	if method == "enterGeneratedDomainTypedMutation" {
		return true
	}
	if visited[method] {
		return false
	}
	visited[method] = true
	for _, called := range calls[method] {
		if generatedDomainInventoryReachesGate(called, calls, visited) {
			return true
		}
	}
	return false
}

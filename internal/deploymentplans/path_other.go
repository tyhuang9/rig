//go:build !windows

package deploymentplans

func deploymentPlanPathIsReparsePoint(string) bool { return false }

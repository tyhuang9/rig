package generatedingress

func testRouteCandidateMayBeLive(err error) bool {
	return registeredLiveRuntimeTestAdapter().RouteCandidateMayBeLive(err)
}

func testRouteGatewayReadinessFailed(err error) bool {
	return registeredLiveRuntimeTestAdapter().RouteGatewayReadinessFailed(err)
}

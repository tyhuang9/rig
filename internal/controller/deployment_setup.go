package controller

import (
	"errors"
	"net/http"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/projectanalysis"
)

func setupPresent(input *apicontract.DeploymentSetupInput) bool {
	return input != nil
}

func deploymentSetupInput(input *apicontract.DeploymentSetupInput) projectanalysis.DeploymentSetup {
	setup := projectanalysis.DeploymentSetup{MigrationCommand: input.MigrationCommand}
	for _, c := range input.Components {
		setup.Components = append(setup.Components, projectanalysis.SetupComponent{
			ID: c.ID, Technology: c.Technology, RootDirectory: c.RootDirectory, PackageManager: c.PackageManager,
			NodeVersion: c.NodeVersion, InstallCommand: c.InstallCommand, BuildCommand: c.BuildCommand,
			StartCommand: c.StartCommand, OutputDirectory: c.OutputDirectory, InternalPort: c.InternalPort, HealthProbe: c.HealthProbe,
		})
	}
	return setup
}

func contractDeploymentSetup(input projectanalysis.DeploymentSetup) *apicontract.DeploymentSetupInput {
	setup := apicontract.DeploymentSetupInput{MigrationCommand: input.MigrationCommand, Components: make([]apicontract.DeploymentSetupComponentInput, 0, len(input.Components))}
	for _, c := range input.Components {
		setup.Components = append(setup.Components, apicontract.DeploymentSetupComponentInput{
			ID: c.ID, Technology: c.Technology, RootDirectory: c.RootDirectory, PackageManager: c.PackageManager,
			NodeVersion: c.NodeVersion, InstallCommand: c.InstallCommand, BuildCommand: c.BuildCommand,
			StartCommand: c.StartCommand, OutputDirectory: c.OutputDirectory, InternalPort: c.InternalPort, HealthProbe: c.HealthProbe,
		})
	}
	return &setup
}

func deploymentSetupProblem(w http.ResponseWriter, r *http.Request, err error) bool {
	var invalid *projectanalysis.SetupError
	if !errors.As(err, &invalid) {
		return false
	}
	problem(w, r, http.StatusUnprocessableEntity, "invalid_deployment_setup", "Check the deployment settings", invalid.Fields)
	return true
}

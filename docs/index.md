---
layout: accessible-home
sidebar: false

hero:
  name: Rig
  text: Local-first application deployments
  tagline: Inspect, deploy, and recover Docker Compose and supported generated applications from one controller without handing workload execution to a remote service.
  actions:
    - theme: brand
      text: Get started
      link: /getting-started
    - theme: alt
      text: Connect GitHub
      link: /connect-github

features:
  - title: Keep control local
    details: The controller, protected credentials, release workspaces, and Docker endpoint stay on the workload machine.
  - title: Review before execution
    details: Rig inspects effective Compose configuration or reviews an editable generated-app plan before execution.
  - title: Recover deliberately
    details: Releases and configuration revisions are retained so an administrator can explicitly redeploy known source and configuration.
---

## What Rig does

Rig manages Docker Compose applications and supported generated JavaScript or TypeScript applications from a local folder or a selected GitHub.com repository. Its authenticated dashboard records configuration revisions, immutable releases, deployment history, policy findings, and recovery choices. A keyboard-driven terminal and `hostctl` provide operator and automation paths to the same local controller.

Real execution is opt-in. `--compose-runtime` enables the controller-local Docker Compose executor, while `--generated-runtime` enables the separate controller-local generated runtime. The development-only fake runtime never executes a workload and cannot be combined with either real runtime.

## Start with the right path

- New to Rig? Follow [Getting started](./getting-started.md).
- Want to select a repository from your GitHub account? Read [Connect GitHub](./connect-github.md).
- Enabling real Docker execution? Review [Docker Compose runtime operations](./compose-runtime.md).
- Deploying a supported JavaScript or TypeScript application? Review [Generated JavaScript runtime operations](./generated-runtime.md) and [editable deployment setup verification](./deployment-setup-verification.md).
- Operating source connections or automatic deployment? Use [GitHub-connected deployments](./github-connected-deployments.md).
- Deploying the optional webhook relay? Use [Official webhook relay operations](./relay-operations.md).

::: warning Current scope
Rig is under active development. Full live GitHub-to-Docker execution, recovery, and infrastructure controls must be verified in your own environment before production use. Rig manages Caddy only for generated-runtime ingress, does not target remote deployment agents, and never performs an automatic rollback.
:::

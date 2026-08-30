---
layout: accessible-home
sidebar: false

hero:
  name: Rig
  text: Local-first Docker Compose deployments
  tagline: Inspect, deploy, and recover applications from one controller without handing workload execution to a remote service.
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
    details: Rig inspects effective Compose configuration and requires exact approval for supported elevated capabilities.
  - title: Recover deliberately
    details: Releases and configuration revisions are retained so an administrator can explicitly redeploy known source and configuration.
---

## What Rig does

Rig manages Docker Compose applications from a local folder or a selected GitHub.com repository. Its authenticated dashboard records configuration revisions, immutable releases, deployment history, policy findings, and recovery choices. A keyboard-driven terminal and `hostctl` provide operator and automation paths to the same local controller.

Real execution is opt-in. Without `--compose-runtime`, Rig does not start a deployment worker. The development-only fake runtime never executes a workload.

## Start with the right path

- New to Rig? Follow [Getting started](./getting-started.md).
- Want to select a repository from your GitHub account? Read [Connect GitHub](./connect-github.md).
- Enabling real Docker execution? Review [Docker Compose runtime operations](./compose-runtime.md).
- Operating source connections or automatic deployment? Use [GitHub-connected deployments](./github-connected-deployments.md).
- Deploying the optional webhook relay? Use [Official webhook relay operations](./relay-operations.md).

::: warning Current scope
Rig is under active development. Live GitHub authorization, production Compose execution, recovery, and infrastructure controls must be verified in your own environment before production use. Rig does not currently manage Caddy or remote deployment agents, and it never performs an automatic rollback.
:::

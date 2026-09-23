# Connect GitHub

Rig can create an application from a GitHub.com repository without an installed Git CLI or a user-managed checkout. Authentication uses GitHub's device flow: Rig displays a short-lived code, and you approve the connection on GitHub.

## Before you connect

Standard Rig builds enable GitHub connections by default through the official `rig-deployment-connector`. No GitHub command-line flags are needed for standard use.

Forks and custom deployments can override the official GitHub App identity by supplying both public identifiers:

```text
--github-client-id <public-client-id> --github-app-slug <github-app-slug>
```

The two flags are an atomic pair. Supplying only one is invalid rather than combining a custom value with an official default. These values identify the app; they are not credentials. A client secret, GitHub App private key, access token, refresh token, or webhook secret must never be placed in this command, committed to the repository, or pasted into Rig's application configuration.

Administrators can deliberately turn the connector off with `--github-connections=false`. If the dashboard says **GitHub connections are disabled**, refreshing the page will not enable the feature. An administrator must restart the controller without that opt-out or, for a fork or custom deployment, with a valid `--github-client-id` and `--github-app-slug` pair. Do not invent placeholder values.

::: info Verification status
The official default identity and deterministic coverage for the connection flow, storage boundaries, and user interface are implemented. Recorded external checks cover device authorization, reload/resume and restart persistence, repository-access installation, private-repository selection, and branch discovery. Positive end-to-end GitHub-to-Docker Compose execution and disconnect-cleanup confirmation remain external promotion checks.
:::

## Connect an account and repository

1. In the dashboard, open **Connections**.
2. In the GitHub card, select **Connect GitHub**. If access was lost, select **Reconnect** instead. Rig saves one reusable connector per signed-in user and reuses it across that user's applications on this controller.
3. Open the GitHub device-authorization link shown by Rig and enter the displayed code.
4. Review the GitHub identity and permissions before authorizing.
5. Use Rig's **Manage repository access** link to grant the app access to the intended account and repositories.
6. In **Add application**, choose **GitHub repository**.
7. Use the single repository picker, which aggregates repositories available through the saved connection's personal and organization installations. Select the repository and tracked branch. For a Compose application, also choose a discovered Compose file; a generated application continues to build and run setup instead.
8. Run the exact-source inspection, resolve any findings, and save the application.

Rig polls only while the short-lived authorization is pending. If checking is paused, select **Retry authorization check**. If the code expires or authorization is denied, select **Start new authorization** from the same saved connector to receive a fresh code. Do not send the code to another person.

## What access Rig uses

A correctly configured GitHub App uses repository metadata and read-only repository contents to list installations, repositories, branches, trees, and source snapshots. Repository installation scope is controlled on GitHub; grant only the repositories that this controller needs.

The rotating user credential is stored in a purpose-bound protected file on the controller. SQLite stores safe connection identity, status, and expiry metadata, not the credential. Provider error bodies are not returned to the browser or persisted as diagnostics.

## What a GitHub release contains

When a deployment is requested, Rig resolves the tracked branch to one commit, downloads a bounded archive from GitHub, and records immutable provenance including the commit SHA, archive hash, managed workspace state, and configuration revision. Compose releases also record the selected Compose path. Generated releases use the accepted generated setup and plan revision instead; see [Generated JavaScript runtime operations](./generated-runtime.md).

The current source model does not support GitHub Enterprise Server, pull-request refs, Git submodules, or Git LFS expansion.

## Disconnect or recover access

- Use **Disconnect** when a controller connection is no longer needed. Verify cleanup before treating it as a production credential-revocation control.
- If GitHub access is removed or expires, use **Reconnect** in **Connections** and authorize the same GitHub identity. Rig retains the saved connection ID and its source bindings; a different identity leaves the prior credential and bindings unchanged.
- If an expected repository is missing, update the GitHub App installation's repository access, then retry the list in Rig.
- A relay outage does not prevent manual GitHub deployment when the controller still has source access. Automatic deployment is a separate, opt-in feature.

For execution policy, failure behavior, relay enrollment, and the staging checklist, continue to [GitHub-connected deployments](./github-connected-deployments.md).

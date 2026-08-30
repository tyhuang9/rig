# Connect GitHub

Rig can create an application from a GitHub.com repository without an installed Git CLI or a user-managed checkout. Authentication uses GitHub's device flow: Rig displays a short-lived code, and you approve the connection on GitHub.

## Before you connect

The controller must report that GitHub connections are available. On the current release, an administrator enables this by configuring both public identifiers for a GitHub App:

```text
--github-client-id <public-client-id> --github-app-slug <github-app-slug>
```

These values identify the app; they are not credentials. A client secret, GitHub App private key, access token, refresh token, or webhook secret must never be placed in this command, committed to the repository, or pasted into Rig's application configuration.

If the dashboard says **GitHub connections are unavailable**, refreshing the page will not enable the feature. The controller must be restarted with a valid public client ID and app slug. Administrators should follow [GitHub-connected deployments](./github-connected-deployments.md#controller-setup) rather than inventing placeholder values.

::: info Verification status
The repository includes deterministic coverage for the connection flow, storage boundaries, and user interface. Live authorization against a production GitHub App is still an external verification step.
:::

## Connect an account and repository

1. In the dashboard, select **Add application**.
2. Enter the application name and choose **GitHub repository**.
3. Select **Connect GitHub**.
4. Open the GitHub device-authorization link shown by Rig and enter the displayed code.
5. Review the GitHub identity and permissions before authorizing.
6. Use Rig's **Install or configure the Rig GitHub App** link to grant the app access to the intended account and repositories.
7. Return to Rig and choose the GitHub App installation, repository, tracked branch, and discovered Compose file.
8. Run the exact-source inspection, resolve any findings, and save the application.

Rig polls only while the short-lived authorization is pending. If the code expires or authorization is denied, start a new connection. Do not send the code to another person.

## What access Rig uses

A correctly configured GitHub App uses repository metadata and read-only repository contents to list installations, repositories, branches, trees, and source snapshots. Repository installation scope is controlled on GitHub; grant only the repositories that this controller needs.

The rotating user credential is stored in a purpose-bound protected file on the controller. SQLite stores safe connection identity, status, and expiry metadata, not the credential. Provider error bodies are not returned to the browser or persisted as diagnostics.

## What a GitHub release contains

When a deployment is requested, Rig resolves the tracked branch to one commit, downloads a bounded archive from GitHub, and records immutable provenance including the commit SHA, archive hash, Compose path, managed workspace state, and configuration revision. It does not fetch Git history.

The current source model does not support GitHub Enterprise Server, pull-request refs, Git submodules, or Git LFS expansion.

## Disconnect or recover access

- Use **Disconnect** to remove a controller connection when it is no longer needed.
- If GitHub access is removed or expires, create a new connection before source inspection or deployment.
- If an expected repository is missing, update the GitHub App installation's repository access, then retry the list in Rig.
- A relay outage does not prevent manual GitHub deployment when the controller still has source access. Automatic deployment is a separate, opt-in feature.

For execution policy, failure behavior, relay enrollment, and the staging checklist, continue to [GitHub-connected deployments](./github-connected-deployments.md).

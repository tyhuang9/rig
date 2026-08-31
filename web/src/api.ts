import {
  operations,
  type Application,
  type ApplicationAutoDeployStatus,
  type ApplicationConfiguration,
  type ApplicationList,
  type BootstrapRequest,
  type BootstrapStatus,
  type CreateApplicationRequest,
  type CSRFResponse,
  type DeploymentList,
  type DeployReleaseRequest,
  type GitHubBranchPage,
  type GitHubDeviceAuthorization,
  type GitHubInstallationPage,
  type GitHubRepositoryPage,
  type InspectRequest,
  type InspectResponse,
  type JobList,
  type JobMutationResponse,
  type JobResponse,
  type LoginRequest,
  type MachineList,
  type MeResponse,
  type ReplaceApplicationConfigurationRequest,
  type RelayStatus,
  type RelayBindingStatus,
  type RelayEnrollmentStart,
  type RelayEnrollmentStatus,
  type RelayKeyRotationStatus,
  type ResumeApplicationAutoDeployRequest,
  type ReleaseList,
  type RuntimeApprovalList,
  type RuntimeApprovalMutationResponse,
  type RuntimeApprovalResponse,
  type GrantRuntimeApprovalRequest,
  type SessionResponse,
  type SourceConnection,
  type SourceConnectionList,
  type SystemStatus,
  type StartRelayEnrollmentRequest,
  type UpdateApplicationAutoDeployRequest,
} from "./generated/api-contract";

export type {
  Application,
  ApplicationAutoDeployStatus,
  ApplicationConfiguration,
  CreateApplicationRequest,
  GitHubBranch,
  GitHubDeviceAuthorization,
  GitHubInstallation,
  GitHubRepository,
  GitHubSource,
  InspectRequest,
  InspectResponse,
  Job,
  Machine,
  SourceConnection,
  SystemStatus,
  User,
  ReplaceApplicationConfigurationRequest,
  Deployment,
  Release,
  RuntimeApproval,
  RelayStatus,
  RelayBindingStatus,
  RelayEnrollmentStart,
  RelayEnrollmentStatus,
  RelayKeyRotationStatus,
  ResumeApplicationAutoDeployRequest,
  UpdateApplicationAutoDeployRequest,
  StartRelayEnrollmentRequest,
} from "./generated/api-contract";

export class APIError extends Error {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly errors: Record<string, string>;
  readonly retryAfterSeconds?: number;

  constructor({ status, code, detail, errors = {}, retryAfterSeconds }: {
    status: number;
    code: string;
    detail: string;
    errors?: Record<string, string>;
    retryAfterSeconds?: number;
  }) {
    super(detail);
    this.name = "APIError";
    this.status = status;
    this.code = code;
    this.detail = detail;
    this.errors = errors;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

let csrfToken = window.sessionStorage.getItem("hostd-csrf") ?? "";

export function setCSRF(token: string) {
  csrfToken = token;
  window.sessionStorage.setItem("hostd-csrf", token);
}

export function clearCSRF() {
  csrfToken = "";
  window.sessionStorage.removeItem("hostd-csrf");
}

async function rotateCSRF(): Promise<string> {
  const response = await fetch(operations.rotateCSRF.path, {
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) throw new Error("Authentication required");
  const body = (await response.json()) as CSRFResponse;
  setCSRF(body.csrfToken);
  return body.csrfToken;
}

async function request<T>(path: string, init: RequestInit = {}, retryCSRF = true): Promise<T> {
  const mutating = init.method && !["GET", "HEAD", "OPTIONS"].includes(init.method);
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      ...(mutating ? { "X-CSRF-Token": csrfToken } : {}),
      ...init.headers,
    },
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({ code: "request_failed", detail: "Request failed" }));
    if (response.status === 403 && body.code === "csrf_failed" && mutating && retryCSRF) {
      await rotateCSRF();
      return request<T>(path, init, false);
    }
    const retryAfter = response.headers.get("Retry-After");
    const retryAfterSeconds = retryAfter && /^\d+$/.test(retryAfter) ? Number(retryAfter) : undefined;
    throw new APIError({
      status: response.status,
      code: typeof body.code === "string" ? body.code : "request_failed",
      detail: typeof body.detail === "string" ? body.detail : "Request failed",
      errors: body.errors && typeof body.errors === "object"
        ? Object.fromEntries(Object.entries(body.errors).filter((entry): entry is [string, string] => typeof entry[1] === "string"))
        : {},
      retryAfterSeconds,
    });
  }
  return response.status === 204 ? (undefined as T) : response.json();
}

function operationPath(path: string, values: Record<string, string | number>) {
  return path.replace(/\{([^}]+)\}/g, (_, key: string) => encodeURIComponent(String(values[key])));
}

function pagedPath(path: string, values: Record<string, string | number>, page: number, perPage: number) {
  const query = new URLSearchParams({ page: String(page), perPage: String(perPage) });
  return `${operationPath(path, values)}?${query.toString()}`;
}

function normalizeInspectionResponse(value: InspectResponse): InspectResponse {
  return {
    ...value,
    composeCandidates: Array.isArray(value.composeCandidates) ? value.composeCandidates : [],
    services: Array.isArray(value.services) ? value.services : [],
    findings: Array.isArray(value.findings) ? value.findings : [],
  };
}

export const api = {
  bootstrapStatus: () => request<BootstrapStatus>(operations.bootstrapStatus.path),
  bootstrap: (data: BootstrapRequest) =>
    request<SessionResponse>(operations.bootstrap.path, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  login: (data: LoginRequest) =>
    request<SessionResponse>(operations.login.path, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  me: () => request<MeResponse>(operations.me.path),
  csrf: rotateCSRF,
  logout: () => request<void>(operations.logout.path, { method: "DELETE" }),
  status: () => request<SystemStatus>(operations.systemStatus.path),
  apps: () => request<ApplicationList>(operations.listApplications.path),
  app: (id: string) => request<Application>(operationPath(operations.getApplication.path, { appId: id })),
  getApplicationAutoDeploy: (id: string) =>
    request<ApplicationAutoDeployStatus>(operationPath(operations.getApplicationAutoDeploy.path, { appId: id })),
  updateApplicationAutoDeploy: (id: string, data: UpdateApplicationAutoDeployRequest) =>
    request<ApplicationAutoDeployStatus>(operationPath(operations.updateApplicationAutoDeploy.path, { appId: id }), {
      method: operations.updateApplicationAutoDeploy.method,
      body: JSON.stringify(data),
    }),
  resumeApplicationAutoDeploy: (id: string, data: ResumeApplicationAutoDeployRequest) =>
    request<ApplicationAutoDeployStatus>(operationPath(operations.resumeApplicationAutoDeploy.path, { appId: id }), {
      method: operations.resumeApplicationAutoDeploy.method,
      body: JSON.stringify(data),
    }),
  relayStatus: () => request<RelayStatus>(operations.getRelayStatus.path),
  startRelayEnrollment: (data: StartRelayEnrollmentRequest) =>
    request<RelayEnrollmentStart>(operations.startRelayEnrollment.path, {
      method: operations.startRelayEnrollment.method,
      body: JSON.stringify(data),
    }),
  pollRelayEnrollment: (enrollmentId: string) =>
    request<RelayEnrollmentStatus>(operationPath(operations.pollRelayEnrollment.path, { enrollmentId }), {
      method: operations.pollRelayEnrollment.method,
    }),
  removeRelayBinding: (bindingId: string) =>
    request<RelayBindingStatus>(operationPath(operations.removeRelayBinding.path, { bindingId }), {
      method: operations.removeRelayBinding.method,
    }),
  startRelayKeyRotation: () =>
    request<RelayKeyRotationStatus>(operations.startRelayKeyRotation.path, {
      method: operations.startRelayKeyRotation.method,
    }),
  applicationConfiguration: (id: string) =>
    request<ApplicationConfiguration>(operationPath(operations.getApplicationConfiguration.path, { appId: id })),
  replaceApplicationConfiguration: (id: string, data: ReplaceApplicationConfigurationRequest) =>
    request<ApplicationConfiguration>(operationPath(operations.replaceApplicationConfiguration.path, { appId: id }), {
      method: operations.replaceApplicationConfiguration.method,
      body: JSON.stringify(data),
    }),
  createApp: (data: CreateApplicationRequest) =>
    request<Application>(operations.createApplication.path, { method: "POST", body: JSON.stringify(data) }),
  inspect: async (data: InspectRequest) =>
    normalizeInspectionResponse(await request<InspectResponse>(operations.inspectImport.path, {
      method: "POST",
      body: JSON.stringify(data),
    })),
  sourceConnections: () => request<SourceConnectionList>(operations.listSourceConnections.path),
  startGitHubConnection: () =>
    request<GitHubDeviceAuthorization>(operations.startGitHubDeviceConnection.path, {
      method: operations.startGitHubDeviceConnection.method,
    }),
  pollGitHubConnection: (connectionId: string) =>
    request<SourceConnection>(operationPath(operations.pollGitHubDeviceConnection.path, { connectionId }), {
      method: operations.pollGitHubDeviceConnection.method,
    }),
  refreshSourceConnection: (connectionId: string) =>
    request<SourceConnection>(operationPath(operations.refreshSourceConnection.path, { connectionId }), {
      method: operations.refreshSourceConnection.method,
    }),
  disconnectSourceConnection: (connectionId: string) =>
    request<void>(operationPath(operations.disconnectSourceConnection.path, { connectionId }), {
      method: operations.disconnectSourceConnection.method,
    }),
  githubInstallations: (connectionId: string, page = 1, perPage = 30) =>
    request<GitHubInstallationPage>(pagedPath(operations.listGitHubInstallations.path, { connectionId }, page, perPage)),
  githubRepositories: (connectionId: string, installationId: number, page = 1, perPage = 30) =>
    request<GitHubRepositoryPage>(pagedPath(operations.listGitHubRepositories.path, { connectionId, installationId }, page, perPage)),
  githubBranches: (connectionId: string, installationId: number, repositoryId: number, page = 1, perPage = 30) =>
    request<GitHubBranchPage>(pagedPath(operations.listGitHubBranches.path, { connectionId, installationId, repositoryId }, page, perPage)),
  machines: () => request<MachineList>(operations.listMachines.path),
  jobs: () => request<JobList>(operations.listJobs.path),
  cancelJob: (id: string) =>
    request<JobResponse>(operationPath(operations.cancelJob.path, { jobId: id }), {
      method: operations.cancelJob.method,
    }),
  deployments: (appId: string) =>
    request<DeploymentList>(operationPath(operations.listDeployments.path, { appId })),
  releases: (appId: string) =>
    request<ReleaseList>(operationPath(operations.listReleases.path, { appId })),
  runtimeApprovals: (appId: string) =>
    request<RuntimeApprovalList>(operationPath(operations.listRuntimeApprovals.path, { appId })),
  deployApplication: (appId: string) =>
    request<JobMutationResponse>(operationPath(operations.deployApplication.path, { appId }), {
      method: operations.deployApplication.method,
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
  deployRelease: (appId: string, releaseId: string, data: DeployReleaseRequest) =>
    request<JobMutationResponse>(operationPath(operations.deployRelease.path, { appId, releaseId }), {
      method: operations.deployRelease.method,
      body: JSON.stringify(data),
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
  grantRuntimeApproval: (appId: string, data: GrantRuntimeApprovalRequest) =>
    request<RuntimeApprovalMutationResponse>(operationPath(operations.grantRuntimeApproval.path, { appId }), {
      method: operations.grantRuntimeApproval.method,
      body: JSON.stringify(data),
    }),
  revokeRuntimeApproval: (appId: string, approvalId: string) =>
    request<RuntimeApprovalResponse>(operationPath(operations.revokeRuntimeApproval.path, { appId, approvalId }), {
      method: operations.revokeRuntimeApproval.method,
    }),
  resumeJob: (jobId: string) =>
    request<JobResponse>(operationPath(operations.resumeJob.path, { jobId }), {
      method: operations.resumeJob.method,
    }),
  action: (id: string, type: "deploy" | "start" | "stop" | "restart") =>
    request<JobMutationResponse>(`/api/v1/apps/${id}/${type === "deploy" ? "deployments" : type}`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
};

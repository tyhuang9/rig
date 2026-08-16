import {
  operations,
  type Application,
  type ApplicationList,
  type BootstrapRequest,
  type BootstrapStatus,
  type CreateApplicationRequest,
  type CSRFResponse,
  type InspectRequest,
  type InspectResponse,
  type JobList,
  type JobMutationResponse,
  type JobResponse,
  type LoginRequest,
  type MachineList,
  type MeResponse,
  type SessionResponse,
  type SystemStatus,
} from "./generated/api-contract";

export type { Application, Job, Machine, SystemStatus, User } from "./generated/api-contract";

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
    const body = await response.json().catch(() => ({ detail: "Request failed" }));
    if (response.status === 403 && body.code === "csrf_failed" && mutating && retryCSRF) {
      await rotateCSRF();
      return request<T>(path, init, false);
    }
    throw new Error(body.detail || "Request failed");
  }
  return response.status === 204 ? (undefined as T) : response.json();
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
  app: (id: string) => request<Application>(`/api/v1/apps/${id}`),
  createApp: (data: CreateApplicationRequest) =>
    request<Application>(operations.createApplication.path, { method: "POST", body: JSON.stringify(data) }),
  inspect: (sourcePath: string) =>
    request<InspectResponse>(operations.inspectImport.path, {
      method: "POST",
      body: JSON.stringify({ sourcePath } satisfies InspectRequest),
    }),
  machines: () => request<MachineList>(operations.listMachines.path),
  jobs: () => request<JobList>(operations.listJobs.path),
  cancelJob: (id: string) =>
    request<JobResponse>(operations.cancelJob.path.replace("{jobId}", encodeURIComponent(id)), {
      method: operations.cancelJob.method,
    }),
  action: (id: string, type: "deploy" | "start" | "stop" | "restart") =>
    request<JobMutationResponse>(`/api/v1/apps/${id}/${type === "deploy" ? "deployments" : type}`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
};

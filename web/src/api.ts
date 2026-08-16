import { operations } from "./generated/api-contract";

export type User = {
  id: string;
  username: string;
  role: string;
};

export type Application = {
  id: string;
  slug: string;
  name: string;
  description: string;
  status: string;
  machineName: string;
  createdAt: string;
};

export type Job = {
  id: string;
  type: string;
  resourceType: string;
  resourceId: string;
  status: string;
  phase: string;
  progress: number;
  createdAt: string;
  updatedAt: string;
};

export type Machine = {
  id: string;
  name: string;
  status: string;
  os: string;
  architecture: string;
  hostname: string;
  dockerVersion: string;
  composeVersion: string;
  resources: Record<string, number>;
};

export type SystemStatus = {
  daemon: string;
  capabilities: { fakeRuntime: boolean };
};

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
  const body = (await response.json()) as { csrfToken: string };
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
  bootstrapStatus: () =>
    request<{ bootstrapRequired: boolean }>(operations.bootstrapStatus.path),
  bootstrap: (data: { token: string; username: string; passphrase: string }) =>
    request<{ user: User; csrfToken: string }>(operations.bootstrap.path, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  login: (data: { username: string; passphrase: string }) =>
    request<{ user: User; csrfToken: string }>(operations.login.path, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  me: () => request<{ user: User }>(operations.me.path),
  csrf: rotateCSRF,
  logout: () => request<void>(operations.logout.path, { method: "DELETE" }),
  status: () => request<SystemStatus>(operations.systemStatus.path),
  apps: () => request<{ items: Application[] }>(operations.listApplications.path),
  app: (id: string) => request<Application>(`/api/v1/apps/${id}`),
  createApp: (data: { name: string; description: string; sourcePath: string }) =>
    request<Application>(operations.createApplication.path, { method: "POST", body: JSON.stringify(data) }),
  inspect: (sourcePath: string) =>
    request<{ message: string }>(operations.inspectImport.path, {
      method: "POST",
      body: JSON.stringify({ sourcePath }),
    }),
  machines: () => request<{ items: Machine[] }>(operations.listMachines.path),
  jobs: () => request<{ items: Job[] }>(operations.listJobs.path),
  cancelJob: (id: string) =>
    request<{ job: Job }>(operations.cancelJob.path.replace("{jobId}", encodeURIComponent(id)), {
      method: operations.cancelJob.method,
    }),
  action: (id: string, type: "deploy" | "start" | "stop" | "restart") =>
    request<{ job: Job }>(`/api/v1/apps/${id}/${type === "deploy" ? "deployments" : type}`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
};

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

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
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
    throw new Error(body.detail || "Request failed");
  }
  return response.status === 204 ? (undefined as T) : response.json();
}

export const api = {
  bootstrapStatus: () =>
    request<{ bootstrapRequired: boolean }>("/api/v1/auth/bootstrap/status"),
  bootstrap: (data: { token: string; username: string; passphrase: string }) =>
    request<{ user: User; csrfToken: string }>("/api/v1/auth/bootstrap", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  login: (data: { username: string; passphrase: string }) =>
    request<{ user: User; csrfToken: string }>("/api/v1/auth/sessions", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  me: () => request<{ user: User }>("/api/v1/auth/me"),
  logout: () => request<void>("/api/v1/auth/sessions/current", { method: "DELETE" }),
  status: () => request<SystemStatus>("/api/v1/system/status"),
  apps: () => request<{ items: Application[] }>("/api/v1/apps"),
  app: (id: string) => request<Application>(`/api/v1/apps/${id}`),
  createApp: (data: { name: string; description: string; sourcePath: string }) =>
    request<Application>("/api/v1/apps", { method: "POST", body: JSON.stringify(data) }),
  inspect: (sourcePath: string) =>
    request<{ message: string }>("/api/v1/apps/import/inspect", {
      method: "POST",
      body: JSON.stringify({ sourcePath }),
    }),
  machines: () => request<{ items: Machine[] }>("/api/v1/machines"),
  jobs: () => request<{ items: Job[] }>("/api/v1/jobs"),
  action: (id: string, type: "deploy" | "start" | "stop" | "restart") =>
    request<{ job: Job }>(`/api/v1/apps/${id}/${type === "deploy" ? "deployments" : type}`, {
      method: "POST",
      headers: { "Idempotency-Key": crypto.randomUUID() },
    }),
};

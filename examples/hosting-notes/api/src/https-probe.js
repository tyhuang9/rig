import { request as httpsRequest } from "node:https";
import { configuredValue, tlsCertificateAuthority } from "./config.js";

const requestTimeoutMs = 2_000;
const maximumResponseBytes = 8_192;

function dependencyOptions(env) {
  let endpoint;
  try {
    endpoint = new URL(configuredValue(
      env,
      "HTTPS_DEPENDENCY_URL_ENV",
      "HTTPS_DEPENDENCY_URL",
      "HTTPS dependency URL"
    ));
  } catch {
    throw new Error("The configured HTTPS dependency URL is invalid");
  }
  if (endpoint.protocol !== "https:" || endpoint.username || endpoint.password) {
    throw new Error("The configured HTTPS dependency URL is unsupported");
  }
  const ca = tlsCertificateAuthority(env, "HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64");
  return {
    protocol: "https:",
    hostname: endpoint.hostname,
    port: endpoint.port || 443,
    path: `${endpoint.pathname}${endpoint.search}`,
    method: "GET",
    servername: endpoint.hostname,
    headers: {
      accept: "application/json",
      authorization: `Bearer ${configuredValue(env, "HTTPS_DEPENDENCY_TOKEN_ENV", "HTTPS_DEPENDENCY_TOKEN", "HTTPS dependency token")}`
    },
    rejectUnauthorized: true,
    ...(ca ? { ca } : {})
  };
}

export function probeHttpsDependency(env, {
  request = httpsRequest,
  setTimer = setTimeout,
  clearTimer = clearTimeout
} = {}) {
  const options = dependencyOptions(env);
  return new Promise((resolve, reject) => {
    let settled = false;
    let responseBytes = 0;
    let requestHandle;
    let deadline;
    const finish = (callback, value) => {
      if (settled) return false;
      settled = true;
      if (deadline !== undefined) {
        clearTimer(deadline);
        deadline = undefined;
      }
      callback(value);
      return true;
    };
    const stop = (error) => {
      if (finish(reject, error)) requestHandle?.destroy(error);
    };
    deadline = setTimer(() => {
      stop(new Error("HTTPS dependency request exceeded its deadline"));
    }, requestTimeoutMs);
    try {
      requestHandle = request(options, (response) => {
        response.on("error", () => stop(new Error("HTTPS dependency response failed")));
        response.on("data", (chunk) => {
          responseBytes += Buffer.byteLength(chunk);
          if (responseBytes > maximumResponseBytes) {
            stop(new Error("HTTPS dependency response exceeded its size limit"));
          }
        });
        response.on("end", () => {
          if (response.statusCode >= 200 && response.statusCode < 300) {
            finish(resolve);
            return;
          }
          finish(reject, new Error("HTTPS dependency returned an unsuccessful response"));
        });
      });
      requestHandle.once("error", () => stop(new Error("HTTPS dependency request failed")));
      requestHandle.setTimeout(requestTimeoutMs, () => {
        stop(new Error("HTTPS dependency request timed out"));
      });
      requestHandle.end();
    } catch {
      stop(new Error("HTTPS dependency request failed"));
    }
  });
}

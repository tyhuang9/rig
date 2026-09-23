import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { request as httpsRequest } from "node:https";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { probeHttpsDependency } from "../api/src/https-probe.js";

const harnessDirectory = dirname(fileURLToPath(import.meta.url));
const certDirectory = join(harnessDirectory, "certs");
const testCA = readFileSync(join(certDirectory, "test-ca.crt"));
const token = "disposable-local-https-probe-token";
const stub = spawn(process.execPath, [join(harnessDirectory, "https-stub.mjs")], {
  env: {
    ...process.env,
    FIXTURE_HTTPS_TOKEN: token,
    FIXTURE_HTTPS_CERT_DIRECTORY: certDirectory,
    FIXTURE_HTTPS_LISTEN_HOST: "127.0.0.1",
    FIXTURE_HTTPS_LISTEN_PORT: "0",
  },
  stdio: ["ignore", "pipe", "pipe"],
});

function waitForStub() {
  return new Promise((resolve, reject) => {
    let output = "";
    const timeout = setTimeout(() => finish(reject, new Error("HTTPS fixture did not start")), 5_000);
    const finish = (callback, value) => {
      clearTimeout(timeout);
      stub.stdout.off("data", onData);
      stub.off("error", onError);
      stub.off("exit", onExit);
      callback(value);
    };
    const onData = (chunk) => {
      output += chunk.toString();
      const match = output.match(/fixture-https-ready:(\d+)/);
      if (match) finish(resolve, Number(match[1]));
      else if (output.length > 1024) finish(reject, new Error("HTTPS fixture readiness output is invalid"));
    };
    const onError = () => finish(reject, new Error("HTTPS fixture failed to start"));
    const onExit = () => finish(reject, new Error("HTTPS fixture exited before readiness"));
    stub.stdout.on("data", onData);
    stub.once("error", onError);
    stub.once("exit", onExit);
  });
}

// Route only this disposable test hostname to the local stub. The client still
// verifies the certificate against https.fixture.test, not 127.0.0.1.
const localRequest = (options, callback) => httpsRequest({
  ...options,
  lookup: (_hostname, lookupOptions, done) => lookupOptions.all
    ? done(null, [{ address: "127.0.0.1", family: 4 }])
    : done(null, "127.0.0.1", 4),
}, callback);

try {
  const port = await waitForStub();
  const base = {
    HTTPS_DEPENDENCY_URL: `https://https.fixture.test:${port}/`,
    HTTPS_DEPENDENCY_TOKEN: token,
    HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64: testCA.toString("base64"),
  };
  await probeHttpsDependency(base, { request: localRequest });
  await assert.rejects(probeHttpsDependency({ ...base, HTTPS_DEPENDENCY_TOKEN: "wrong-token" }, { request: localRequest }));
  await assert.rejects(probeHttpsDependency({ ...base, HTTPS_DEPENDENCY_TLS_CA_PEM_BASE64: "" }, { request: localRequest }));
  await assert.rejects(probeHttpsDependency({ ...base, HTTPS_DEPENDENCY_URL: `https://wrong.fixture.test:${port}/` }, { request: localRequest }));
  console.log("Local HTTPS fixture: trusted CA and token accepted; wrong token, missing CA, and wrong hostname rejected.");
} finally {
  stub.kill();
}

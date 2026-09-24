import { readFileSync } from "node:fs";
import { createServer } from "node:https";
import { join } from "node:path";

const expectedToken = `Bearer ${process.env.FIXTURE_HTTPS_TOKEN}`;
const certDirectory = process.env.FIXTURE_HTTPS_CERT_DIRECTORY || "/certs";
const listenHost = process.env.FIXTURE_HTTPS_LISTEN_HOST || "0.0.0.0";
const listenPort = Number(process.env.FIXTURE_HTTPS_LISTEN_PORT ?? 8443);
if (!process.env.FIXTURE_HTTPS_TOKEN || !Number.isInteger(listenPort) || listenPort < 0 || listenPort > 65535) {
  throw new Error("HTTPS fixture configuration is invalid");
}

createServer({
  cert: readFileSync(join(certDirectory, "https.fixture.test.crt")),
  key: readFileSync(join(certDirectory, "https.fixture.test.key"))
}, (request, response) => {
  if (request.headers.authorization !== expectedToken) {
    response.writeHead(401, { "content-type": "application/json", "cache-control": "no-store" });
    response.end(JSON.stringify({ status: "unauthorized" }));
    return;
  }
  response.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
  response.end(JSON.stringify({ service: "fixture-https-dependency", status: "ok" }));
}).listen(listenPort, listenHost, function () {
  console.log(`fixture-https-ready:${this.address().port}`);
});

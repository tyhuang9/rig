import { readFileSync } from "node:fs";
import { createServer } from "node:https";

const expectedToken = `Bearer ${process.env.FIXTURE_HTTPS_TOKEN}`;

createServer({
  cert: readFileSync("/certs/https.fixture.test.crt"),
  key: readFileSync("/certs/https.fixture.test.key")
}, (request, response) => {
  if (request.headers.authorization !== expectedToken) {
    response.writeHead(401, { "content-type": "application/json", "cache-control": "no-store" });
    response.end(JSON.stringify({ status: "unauthorized" }));
    return;
  }
  response.writeHead(200, { "content-type": "application/json", "cache-control": "no-store" });
  response.end(JSON.stringify({ service: "fixture-https-dependency", status: "ok" }));
}).listen(8443, "0.0.0.0");

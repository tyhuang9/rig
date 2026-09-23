import { createServer } from "node:http";
import { WebSocketServer } from "ws";
import { createApp } from "./app.js";
import { createDatabase } from "./database.js";
import { runtimeMetadata } from "./config.js";

const port = Number.parseInt(process.env.PORT || "3000", 10);
const database = createDatabase(process.env);
const app = createApp({ database, env: process.env });
const server = createServer(app);
const sockets = new WebSocketServer({ noServer: true });
const metadata = runtimeMetadata(process.env);

sockets.on("connection", (socket) => {
  const timeout = setTimeout(() => socket.close(1008, "message timeout"), 5_000);
  socket.once("message", (message) => {
    clearTimeout(timeout);
    if (message.length > 1_024) {
      socket.close(1009, "message too large");
      return;
    }
    socket.send(JSON.stringify({ echo: message.toString(), ...metadata }));
    socket.close(1000);
  });
});

server.on("upgrade", (request, socket, head) => {
  if (new URL(request.url, "http://fixture.invalid").pathname !== "/api/ws") {
    socket.destroy();
    return;
  }
  sockets.handleUpgrade(request, socket, head, (websocket) => sockets.emit("connection", websocket, request));
});

server.listen(port, "0.0.0.0", () => {
  console.info(`hosting-notes API listening on 0.0.0.0:${port}`);
});

async function close(signal) {
  console.info(`received ${signal}; stopping hosting-notes API`);
  sockets.clients.forEach((socket) => socket.close(1001));
  server.close(async () => {
    await database.end();
    process.exit(0);
  });
}

process.once("SIGINT", () => void close("SIGINT"));
process.once("SIGTERM", () => void close("SIGTERM"));

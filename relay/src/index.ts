/**
 * opencapy-relay — Cloudflare Durable Objects WebSocket relay
 *
 * Routes terminal I/O between the opencapy Mac daemon and the iOS app.
 * Each pairing token maps to one RelayRoom Durable Object. Both sides
 * connect as WebSocket clients; the DO forwards messages between them.
 *
 * URL format:
 *   wss://relay.opencapy.dev/relay/<TOKEN>?role=mac|ios
 *
 * Token is a 32-char hex string generated once by the daemon and shared
 * via QR code. It acts as both the room identifier and the auth secret —
 * 128 bits of entropy makes it practically unguessable.
 */

export interface Env {
  RELAY: DurableObjectNamespace;
}

// ─── Worker entry point (stateless router) ───────────────────────────────────

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);

    // Health check
    if (url.pathname === "/health") {
      return new Response("ok", { status: 200 });
    }

    // Relay endpoint: /relay/<token>
    const match = url.pathname.match(/^\/relay\/([a-f0-9]{32,64})$/i);
    if (!match) {
      return new Response("Not found", { status: 404 });
    }

    if (request.headers.get("Upgrade") !== "websocket") {
      return new Response("Expected WebSocket upgrade", { status: 426 });
    }

    const role = url.searchParams.get("role");
    if (role !== "mac" && role !== "ios") {
      return new Response('Missing or invalid role (must be "mac" or "ios")', {
        status: 400,
      });
    }

    // Route to the Durable Object for this token.
    // idFromName() is deterministic — same token always hits the same DO.
    const id = env.RELAY.idFromName(match[1].toLowerCase());
    return env.RELAY.get(id).fetch(request);
  },
} satisfies ExportedHandler<Env>;

// ─── RelayRoom Durable Object ─────────────────────────────────────────────────

type Role = "mac" | "ios";

interface Attachment {
  role: Role;
  connectedAt: number;
}

export class RelayRoom {
  private ctx: DurableObjectState;
  // Rebuilt from hibernated WebSocket attachments on every wakeup.
  private clients = new Map<WebSocket, Attachment>();

  constructor(ctx: DurableObjectState, _env: Env) {
    this.ctx = ctx;

    // Restore client map after hibernation. The constructor runs on every
    // wakeup — never rely on this.clients surviving between messages.
    for (const ws of ctx.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment | null;
      if (att) this.clients.set(ws, att);
    }

    // Handle "ping" frames automatically without waking the DO.
    // Keeps long-lived connections alive at zero CPU cost.
    this.ctx.setWebSocketAutoResponse(
      new WebSocketRequestResponsePair("ping", "pong")
    );
  }

  async fetch(request: Request): Promise<Response> {
    const role = new URL(request.url).searchParams.get("role") as Role;

    // Kick any existing connection with the same role (reconnect case).
    for (const [ws, att] of this.clients) {
      if (att.role === role) {
        try { ws.close(1000, "replaced by new connection"); } catch { /* already closed */ }
        this.clients.delete(ws);
      }
    }

    const pair = new WebSocketPair();
    const [client, server] = [pair[0], pair[1]];

    // IMPORTANT: use ctx.acceptWebSocket(), NOT server.accept().
    // Using the wrong method silently disables hibernation.
    this.ctx.acceptWebSocket(server);

    const att: Attachment = { role, connectedAt: Date.now() };
    // serializeAttachment survives hibernation (2KB limit — only store identifiers).
    server.serializeAttachment(att);
    this.clients.set(server, att);

    // Tell the connecting client whether its peer is already present.
    const peerPresent = [...this.clients.values()].some(
      (a) => a.role !== role
    );
    server.send(
      JSON.stringify({ type: "relay_connected", role, peerPresent })
    );

    // If peer is already here, tell it too.
    if (peerPresent) {
      for (const [ws, a] of this.clients) {
        if (a.role !== role) {
          ws.send(JSON.stringify({ type: "peer_connected", role }));
        }
      }
    }

    return new Response(null, { status: 101, webSocket: client });
  }

  // ── Hibernation callbacks ─────────────────────────────────────────────────

  async webSocketMessage(
    ws: WebSocket,
    message: string | ArrayBuffer
  ): Promise<void> {
    const sender = this.clients.get(ws);
    if (!sender) return;

    const targetRole: Role = sender.role === "mac" ? "ios" : "mac";

    for (const [targetWs, att] of this.clients) {
      if (att.role === targetRole) {
        targetWs.send(message); // forward raw — binary or text, unchanged
        return;
      }
    }
    // Peer not connected — drop the message. The peer will re-request
    // terminal state when it reconnects (daemon sends a fresh snapshot).
  }

  async webSocketClose(
    ws: WebSocket,
    code: number,
    _reason: string
  ): Promise<void> {
    const att = this.clients.get(ws);
    this.clients.delete(ws);

    if (att) {
      // Notify the peer that its partner disconnected.
      for (const [peerWs, peerAtt] of this.clients) {
        if (peerAtt.role !== att.role) {
          peerWs.send(
            JSON.stringify({ type: "peer_disconnected", role: att.role, code })
          );
        }
      }
    }
  }

  async webSocketError(ws: WebSocket, _error: unknown): Promise<void> {
    this.clients.delete(ws);
  }
}

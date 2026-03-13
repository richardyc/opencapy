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
 * Also pushes Live Activity updates via APNs when the iOS client is
 * disconnected (phone locked / app suspended). Set Cloudflare secrets:
 *   wrangler secret put APNS_KEY_P8    (full .p8 file content)
 *   wrangler secret put APNS_KEY_ID    (10-char Key ID)
 *   wrangler secret put APNS_TEAM_ID   (10-char Team ID)
 */

export interface Env {
  RELAY:        DurableObjectNamespace;
  APNS_KEY_P8:  string;
  APNS_KEY_ID:  string;
  APNS_TEAM_ID: string;
}

// ─── Worker entry point ───────────────────────────────────────────────────────

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url);
    if (url.pathname === "/health") return new Response("ok");

    const match = url.pathname.match(/^\/relay\/([a-f0-9]{32,64})$/i);
    if (!match) return new Response("Not found", { status: 404 });
    if (request.headers.get("Upgrade") !== "websocket")
      return new Response("Expected WebSocket upgrade", { status: 426 });

    const role = url.searchParams.get("role");
    if (role !== "mac" && role !== "ios")
      return new Response('role must be "mac" or "ios"', { status: 400 });

    const id = env.RELAY.idFromName(match[1].toLowerCase());
    // Hint the DO location based on the connecting client's continent so the
    // room is created close to its users (e.g. "apac" for HK/SG/TYO users).
    // This is best-effort on first creation; subsequent requests go to the
    // same DO regardless of origin.
    const hint = continentToLocationHint((request as any).cf?.continent);
    return env.RELAY.get(id, hint ? { locationHint: hint } : undefined).fetch(request);
  },
} satisfies ExportedHandler<Env>;

/** Map Cloudflare continent code → DO location hint. */
function continentToLocationHint(continent?: string): DurableObjectLocationHint | undefined {
  switch (continent) {
    case "AS": return "apac";   // Asia (HK, SG, TYO, …)
    case "OC": return "apac";   // Oceania → closest is apac
    case "EU": return "weur";
    case "AF": return "afr";
    case "SA": return "sam";
    case "ME": return "me";
    case "NA": return "enam";
    default:   return undefined; // let Cloudflare decide
  }
}

// ─── APNs helpers ─────────────────────────────────────────────────────────────

/** Build a short-lived APNs JWT signed with ES256. */
async function makeAPNsJWT(keyP8: string, keyId: string, teamId: string): Promise<string> {
  const pem = keyP8.replace(/-----BEGIN PRIVATE KEY-----|-----END PRIVATE KEY-----|\s/g, "");
  const keyData = Uint8Array.from(atob(pem), c => c.charCodeAt(0));
  const key = await crypto.subtle.importKey(
    "pkcs8", keyData,
    { name: "ECDSA", namedCurve: "P-256" },
    false, ["sign"],
  );
  const b64url = (obj: object) =>
    btoa(JSON.stringify(obj)).replace(/=/g,"").replace(/\+/g,"-").replace(/\//g,"_");
  const header  = b64url({ alg: "ES256", kid: keyId });
  const payload = b64url({ iss: teamId, iat: Math.floor(Date.now() / 1000) });
  const signing = `${header}.${payload}`;
  const rawSig  = await crypto.subtle.sign(
    { name: "ECDSA", hash: "SHA-256" }, key,
    new TextEncoder().encode(signing),
  );
  const sigB64 = btoa(String.fromCharCode(...new Uint8Array(rawSig)))
    .replace(/=/g,"").replace(/\+/g,"-").replace(/\//g,"_");
  return `${signing}.${sigB64}`;
}

/** Send a Live Activity push update via APNs HTTP/2. */
async function pushLiveActivity(
  activityToken: string,
  state: Record<string, unknown>,
  needsApproval: boolean,
  env: Env,
): Promise<void> {
  if (!env.APNS_KEY_P8 || !env.APNS_KEY_ID || !env.APNS_TEAM_ID) return;
  try {
    const jwt = await makeAPNsJWT(env.APNS_KEY_P8, env.APNS_KEY_ID, env.APNS_TEAM_ID);
    const aps: Record<string, unknown> = {
      timestamp: Math.floor(Date.now() / 1000),
      event: "update",
      "content-state": state,
    };
    if (needsApproval) {
      aps.alert = { title: "Claude needs your OK", body: state.sessionName ?? "Approval needed" };
      aps.sound = "default";
    }
    const res = await fetch(
      `https://api.push.apple.com/3/device/${activityToken}`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${jwt}`,
          "apns-topic": "dev.opencapy.app.push-type.liveactivity",
          "apns-push-type": "liveactivity",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ aps }),
      },
    );
    if (!res.ok) console.error(`[relay apns] ${res.status}: ${await res.text()}`);
  } catch (e) {
    console.error("[relay apns] error:", e);
  }
}

// ─── RelayRoom Durable Object ─────────────────────────────────────────────────

type Role = "mac" | "ios";
interface Attachment { role: Role; connectedAt: number; }

// DO storage keys:
//   "la:<session>" → Live Activity push token (from iOS register_live_activity)

export class RelayRoom {
  private ctx: DurableObjectState;
  private env: Env;
  private clients = new Map<WebSocket, Attachment>();

  constructor(ctx: DurableObjectState, env: Env) {
    this.ctx = ctx;
    this.env = env;
    for (const ws of ctx.getWebSockets()) {
      const att = ws.deserializeAttachment() as Attachment | null;
      if (att) this.clients.set(ws, att);
    }
    this.ctx.setWebSocketAutoResponse(new WebSocketRequestResponsePair("ping", "pong"));
  }

  async fetch(request: Request): Promise<Response> {
    const role = new URL(request.url).searchParams.get("role") as Role;

    // Kick any existing connection with the same role (reconnect).
    for (const [ws, att] of this.clients) {
      if (att.role === role) {
        try { ws.close(1000, "replaced"); } catch { /* already closed */ }
        this.clients.delete(ws);
      }
    }

    const pair = new WebSocketPair();
    const [client, server] = [pair[0], pair[1]];
    this.ctx.acceptWebSocket(server);
    const att: Attachment = { role, connectedAt: Date.now() };
    server.serializeAttachment(att);
    this.clients.set(server, att);

    const peerPresent = [...this.clients.values()].some(a => a.role !== role);
    server.send(JSON.stringify({ type: "relay_connected", role, peerPresent }));
    if (peerPresent) {
      for (const [ws, a] of this.clients) {
        if (a.role !== role) ws.send(JSON.stringify({ type: "peer_connected", role }));
      }
    }
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const sender = this.clients.get(ws);
    if (!sender) return;

    // Inspect JSON messages for Live Activity bookkeeping.
    if (typeof message === "string") {
      try {
        const msg = JSON.parse(message) as Record<string, unknown>;

        // iOS → daemon: cache the Live Activity push token in persistent storage.
        if (msg.type === "register_live_activity" &&
            typeof msg.session === "string" && typeof msg.token === "string") {
          await this.ctx.storage.put(`la:${msg.session}`, msg.token);
        }

        // Daemon → iOS: on key events, push a Live Activity update via APNs
        // so the lock screen reflects the new state even when iOS is suspended.
        if (sender.role === "mac" && typeof msg.type === "string") {
          const evType = msg.type as string;
          if (evType === "approval" || evType === "done" || evType === "crash") {
            const session = msg.session as string | undefined;
            if (session) {
              const laToken = await this.ctx.storage.get<string>(`la:${session}`);
              if (laToken) {
                const isApproval = evType === "approval";
                const state: Record<string, unknown> = {
                  sessionName:      session,
                  machineName:      msg.machine ?? "",
                  workingDirectory: msg.workingDirectory ?? "",
                  status:           isApproval ? "approval" : (evType === "done" ? "done" : "crashed"),
                  lastOutput:       typeof msg.content === "string" ? msg.content : "",
                  needsApproval:    isApproval,
                  ...(isApproval && { approvalContent: msg.content }),
                };
                // Fire-and-forget — don't block message forwarding.
                this.ctx.waitUntil(pushLiveActivity(laToken, state, isApproval, this.env));
              }
            }
          }
        }
      } catch { /* not JSON or unrecognised — forward unchanged */ }
    }

    // Forward to peer.
    const targetRole: Role = sender.role === "mac" ? "ios" : "mac";
    for (const [targetWs, att] of this.clients) {
      if (att.role === targetRole) { targetWs.send(message); return; }
    }
    // Peer not connected — drop. It will re-sync on reconnect.
  }

  async webSocketClose(ws: WebSocket, code: number): Promise<void> {
    const att = this.clients.get(ws);
    this.clients.delete(ws);
    if (att) {
      for (const [peerWs, peerAtt] of this.clients) {
        if (peerAtt.role !== att.role)
          peerWs.send(JSON.stringify({ type: "peer_disconnected", role: att.role, code }));
      }
    }
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    this.clients.delete(ws);
  }
}

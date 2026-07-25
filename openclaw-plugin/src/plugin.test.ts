import * as fs from "node:fs";
import * as http from "node:http";
import * as os from "node:os";
import * as path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { AllowmailClient, DaemonError } from "./client.js";
import {
  SendEmailGate,
  executeSendEmail,
  newIdempotencyKey,
  type BeforeToolCallEvent,
} from "./plugin.js";

interface RecordedSend {
  recipient: string;
  subject: string;
  text: string;
  idempotency_key: string;
  approved?: boolean;
}

// mockDaemon serves /v1/recipients and /v1/send on a Unix socket, recording
// every send request it receives.
function mockDaemon(recipients: Array<{ alias: string; requires_approval: boolean }>) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "am-"));
  const socketPath = path.join(dir, "d.sock");
  const sends: RecordedSend[] = [];
  const server = http.createServer((req, res) => {
    if (req.method === "GET" && req.url === "/v1/recipients") {
      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ recipients }));
      return;
    }
    if (req.method === "POST" && req.url === "/v1/send") {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8")) as RecordedSend;
        sends.push(body);
        const entry = recipients.find((r) => r.alias === body.recipient);
        if (!entry) {
          res.writeHead(400, { "Content-Type": "application/json" });
          res.end(
            JSON.stringify({
              error: {
                code: "unknown_recipient",
                message: "unknown recipient alias",
                valid_recipients: recipients.map((r) => r.alias),
              },
            }),
          );
          return;
        }
        if (entry.requires_approval && body.approved !== true) {
          res.writeHead(403, { "Content-Type": "application/json" });
          res.end(
            JSON.stringify({
              error: { code: "approval_required", message: "approval required" },
            }),
          );
          return;
        }
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            request_id: "req-1",
            status: "sent",
            recipient: body.recipient,
            detail: "message accepted by the SMTP server",
          }),
        );
      });
      return;
    }
    res.writeHead(404, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ error: { code: "not_found", message: "not found" } }));
  });
  return new Promise<{
    socketPath: string;
    sends: RecordedSend[];
    close: () => Promise<void>;
  }>((resolve) => {
    server.listen(socketPath, () =>
      resolve({
        socketPath,
        sends,
        close: () => new Promise((r) => server.close(() => r())),
      }),
    );
  });
}

function sendEvent(overrides?: Partial<BeforeToolCallEvent>): BeforeToolCallEvent {
  return {
    toolName: "send_email",
    params: { recipient: "dad", subject: "Hello", text: "Hi" },
    toolCallId: "call-1",
    ...overrides,
  };
}

describe("SendEmailGate", () => {
  let daemon: Awaited<ReturnType<typeof mockDaemon>>;
  let client: AllowmailClient;
  let gate: SendEmailGate;

  beforeEach(async () => {
    daemon = await mockDaemon([
      { alias: "dad", requires_approval: true },
      { alias: "self-gmail", requires_approval: false },
    ]);
    client = new AllowmailClient(daemon.socketPath);
    gate = new SendEmailGate(client);
  });

  afterEach(async () => {
    await daemon.close();
  });

  it("prompts only for flagged recipients", async () => {
    const flagged = await gate.beforeToolCall(sendEvent());
    expect(flagged?.requireApproval).toBeDefined();
    expect(flagged?.requireApproval?.allowedDecisions).toEqual(["allow-once", "deny"]);
    expect(flagged?.requireApproval?.title).toContain("dad");
    expect(flagged?.requireApproval?.description).toContain("Hello");

    const unflagged = await gate.beforeToolCall(
      sendEvent({ params: { recipient: "self-gmail", subject: "s", text: "t" } }),
    );
    expect(unflagged).toBeUndefined();
  });

  it("never puts the message body in the prompt", async () => {
    const body = "very-secret-body-content";
    const res = await gate.beforeToolCall(
      sendEvent({ params: { recipient: "dad", subject: "Hello", text: body } }),
    );
    expect(JSON.stringify(res)).not.toContain(body);
  });

  it("ignores other tools", async () => {
    const res = await gate.beforeToolCall(
      sendEvent({ toolName: "web_search", params: { query: "x" } }),
    );
    expect(res).toBeUndefined();
  });

  it("blocks the call when recipient discovery fails", async () => {
    await daemon.close();
    const res = await gate.beforeToolCall(sendEvent());
    expect(res?.block).toBe(true);
    expect(res?.requireApproval).toBeUndefined();
  });

  it("asserts approval only after allow-once", async () => {
    const res = await gate.beforeToolCall(sendEvent());
    res?.requireApproval?.onResolution?.("allow-once");

    const result = await executeSendEmail(client, gate, "call-1", {
      recipient: "dad",
      subject: "Hello",
      text: "Hi",
    });
    expect(result.status).toBe("sent");
    expect(daemon.sends).toHaveLength(1);
    expect(daemon.sends[0].approved).toBe(true);
  });

  it("does not assert approval after deny", async () => {
    const res = await gate.beforeToolCall(sendEvent());
    res?.requireApproval?.onResolution?.("deny");

    // The framework never executes a denied call; if a buggy caller executes
    // anyway, the assertion is absent and the daemon's tripwire rejects it.
    await expect(
      executeSendEmail(client, gate, "call-1", {
        recipient: "dad",
        subject: "Hello",
        text: "Hi",
      }),
    ).rejects.toMatchObject({ code: "approval_required", status: 403 });
    expect(daemon.sends[0].approved).toBeUndefined();
  });

  it("does not reuse an approval across calls", async () => {
    const res = await gate.beforeToolCall(sendEvent());
    res?.requireApproval?.onResolution?.("allow-once");

    await executeSendEmail(client, gate, "call-1", {
      recipient: "dad",
      subject: "Hello",
      text: "Hi",
    });
    await expect(
      executeSendEmail(client, gate, "call-2", {
        recipient: "dad",
        subject: "Hello",
        text: "Hi",
      }),
    ).rejects.toMatchObject({ code: "approval_required" });
  });

  it("sends unflagged recipients without assertion", async () => {
    const result = await executeSendEmail(client, gate, "call-1", {
      recipient: "self-gmail",
      subject: "Hello",
      text: "Hi",
    });
    expect(result.status).toBe("sent");
    expect(daemon.sends[0].approved).toBeUndefined();
    expect(daemon.sends[0].idempotency_key).toMatch(/^openclaw-[0-9a-f]{32}$/);
  });

  it("passes daemon errors through with valid_recipients", async () => {
    let caught: unknown;
    try {
      await executeSendEmail(client, gate, "call-1", {
        recipient: "nope",
        subject: "s",
        text: "t",
      });
    } catch (err) {
      caught = err;
    }
    expect(caught).toBeInstanceOf(DaemonError);
    const derr = caught as DaemonError;
    expect(derr.code).toBe("unknown_recipient");
    expect(derr.validRecipients).toEqual(["dad", "self-gmail"]);
  });

  it("truncates long subjects in the prompt description", async () => {
    const res = await gate.beforeToolCall(
      sendEvent({
        params: { recipient: "dad", subject: "x".repeat(1000), text: "t" },
      }),
    );
    expect(res?.requireApproval?.description.length).toBeLessThanOrEqual(512);
  });
});

describe("newIdempotencyKey", () => {
  it("generates unique keys", () => {
    expect(newIdempotencyKey()).not.toBe(newIdempotencyKey());
  });
});

// Minimal HTTP client for the allowmaild Unix-socket API.
import * as http from "node:http";

export const DEFAULT_SOCKET_PATH = "/run/allowmail/allowmail.sock";

export interface RecipientEntry {
  alias: string;
  requires_approval: boolean;
}

export interface SendParams {
  recipient: string;
  subject: string;
  text: string;
  idempotency_key: string;
  approved?: boolean;
}

export interface SendResult {
  request_id: string;
  status: string;
  recipient: string;
  message_id?: string;
  result_code?: string;
  detail: string;
}

/** A structured error response from the daemon (non-200 status). */
export class DaemonError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly validRecipients?: string[],
  ) {
    super(message);
    this.name = "DaemonError";
  }
}

export class AllowmailClient {
  constructor(readonly socketPath: string = DEFAULT_SOCKET_PATH) {}

  async recipients(): Promise<RecipientEntry[]> {
    const res = await this.request("GET", "/v1/recipients");
    if (res.status !== 200) {
      throw daemonError(res.status, res.body);
    }
    const body = res.body as { recipients?: RecipientEntry[] };
    if (!Array.isArray(body.recipients)) {
      throw new Error("allowmaild returned a malformed recipients response");
    }
    return body.recipients;
  }

  async send(params: SendParams): Promise<SendResult> {
    const res = await this.request("POST", "/v1/send", params);
    if (res.status !== 200) {
      throw daemonError(res.status, res.body);
    }
    return res.body as SendResult;
  }

  private request(
    method: string,
    path: string,
    payload?: unknown,
  ): Promise<{ status: number; body: unknown }> {
    return new Promise((resolve, reject) => {
      const req = http.request(
        {
          socketPath: this.socketPath,
          method,
          path,
          headers: { "Content-Type": "application/json" },
        },
        (res) => {
          const chunks: Buffer[] = [];
          res.on("data", (c: Buffer) => chunks.push(c));
          res.on("end", () => {
            const raw = Buffer.concat(chunks).toString("utf8");
            try {
              resolve({ status: res.statusCode ?? 0, body: JSON.parse(raw) });
            } catch {
              reject(
                new Error(
                  `allowmaild returned invalid JSON (status ${res.statusCode})`,
                ),
              );
            }
          });
        },
      );
      req.on("error", reject);
      if (payload !== undefined) {
        req.write(JSON.stringify(payload));
      }
      req.end();
    });
  }
}

function daemonError(status: number, body: unknown): DaemonError {
  const err = (body as { error?: { code?: string; message?: string; valid_recipients?: string[] } })
    ?.error;
  return new DaemonError(
    status,
    err?.code ?? "unknown",
    err?.message ?? `allowmaild rejected the request (status ${status})`,
    err?.valid_recipients,
  );
}

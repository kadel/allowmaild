// Approval gating and send logic for the send_email tool, kept free of the
// OpenClaw SDK so it can be tested against a mock daemon socket.
import { randomBytes } from "node:crypto";

import {
  AllowmailClient,
  DaemonError,
  type SendParams,
  type SendResult,
} from "./client.js";

export const TOOL_NAME = "send_email";

// OpenClaw caps requireApproval descriptions at 512 characters.
const MAX_DESCRIPTION_CHARS = 512;

export interface BeforeToolCallEvent {
  toolName: string;
  params: Record<string, unknown>;
  toolCallId?: string;
}

export type ApprovalDecision =
  | "allow-once"
  | "allow-always"
  | "deny"
  | "timeout"
  | "cancelled";

export interface BeforeToolCallResult {
  block?: boolean;
  blockReason?: string;
  requireApproval?: {
    title: string;
    description: string;
    severity?: "info" | "warning" | "critical";
    allowedDecisions?: Array<"allow-once" | "allow-always" | "deny">;
    onResolution?: (decision: ApprovalDecision) => void;
  };
}

export interface SendEmailParams {
  recipient: string;
  subject: string;
  text: string;
}

// SendEmailGate implements the approval flow: the before_tool_call hook asks
// the daemon which recipients require approval and routes flagged sends
// through the framework prompt; execute() then asserts approval to the daemon
// only for calls whose prompt resolved allow-once. Persistent trust
// ("allow-always") is deliberately not offered — it lives only in the daemon
// config.
export class SendEmailGate {
  private approved = new Set<string>();

  constructor(private client: AllowmailClient) {}

  async beforeToolCall(
    event: BeforeToolCallEvent,
  ): Promise<BeforeToolCallResult | undefined> {
    if (event.toolName !== TOOL_NAME) {
      return undefined;
    }

    let recipients;
    try {
      recipients = await this.client.recipients();
    } catch {
      // Fail closed: without approval information no send may proceed.
      return {
        block: true,
        blockReason:
          "allowmaild recipient discovery failed; refusing to send without approval information",
      };
    }

    const alias =
      typeof event.params.recipient === "string" ? event.params.recipient : "";
    const entry = recipients.find((r) => r.alias === alias);
    if (!entry?.requires_approval) {
      // Unflagged or unknown alias: no prompt. Unknown aliases fail later in
      // the daemon with unknown_recipient and its valid_recipients list.
      return undefined;
    }

    const subject =
      typeof event.params.subject === "string" ? event.params.subject : "";
    const callId = event.toolCallId;
    const description = truncate(
      `Send an email to "${alias}" with subject: ${subject}`,
      MAX_DESCRIPTION_CHARS,
    );
    return {
      requireApproval: {
        title: `Send email to ${alias}`,
        description,
        allowedDecisions: ["allow-once", "deny"],
        onResolution: (decision) => {
          if (decision === "allow-once" && callId) {
            this.approved.add(callId);
          }
        },
      },
    };
  }

  // consumeApproval reports whether this tool call's prompt resolved
  // allow-once, and forgets the approval so it cannot be replayed.
  consumeApproval(callId: string | undefined): boolean {
    return callId !== undefined && this.approved.delete(callId);
  }
}

export async function executeSendEmail(
  client: AllowmailClient,
  gate: SendEmailGate,
  callId: string | undefined,
  params: SendEmailParams,
): Promise<SendResult> {
  const request: SendParams = {
    recipient: params.recipient,
    subject: params.subject,
    text: params.text,
    idempotency_key: newIdempotencyKey(),
  };
  if (gate.consumeApproval(callId)) {
    request.approved = true;
  }
  return client.send(request);
}

// Each tool call is a fresh request: agent-level retries get new keys and the
// daemon's rate limits bound the damage.
export function newIdempotencyKey(): string {
  return "openclaw-" + randomBytes(16).toString("hex");
}

export function formatDaemonError(err: DaemonError): string {
  let msg = `allowmaild rejected the send (${err.code}): ${err.message}`;
  if (err.validRecipients?.length) {
    msg += ` Valid recipients: ${err.validRecipients.join(", ")}.`;
  }
  return msg;
}

function truncate(s: string, max: number): string {
  return s.length <= max ? s : s.slice(0, max - 1) + "…";
}

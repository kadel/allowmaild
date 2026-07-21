import { Type } from "typebox";
import {
  buildJsonPluginConfigSchema,
  definePluginEntry,
  type OpenClawPluginDefinition,
} from "openclaw/plugin-sdk/plugin-entry";

import { AllowmailClient, DaemonError, DEFAULT_SOCKET_PATH } from "./client.js";
import {
  SendEmailGate,
  TOOL_NAME,
  executeSendEmail,
  formatDaemonError,
  type BeforeToolCallEvent,
  type SendEmailParams,
} from "./plugin.js";

const plugin: OpenClawPluginDefinition = definePluginEntry({
  id: "allowmail",
  name: "AllowMail",
  description:
    "Send email through the allowmaild daemon, with per-recipient approval gating.",
  configSchema: buildJsonPluginConfigSchema({
    type: "object",
    additionalProperties: false,
    properties: {
      socketPath: {
        type: "string",
        description: "Path to the allowmaild Unix socket.",
        default: DEFAULT_SOCKET_PATH,
      },
    },
  }),
  register(api) {
    const cfg = (api.pluginConfig ?? {}) as { socketPath?: string };
    const client = new AllowmailClient(cfg.socketPath ?? DEFAULT_SOCKET_PATH);
    const gate = new SendEmailGate(client);

    api.registerTool({
      name: TOOL_NAME,
      label: "Send email",
      description:
        "Send a plain-text email to a pre-approved recipient alias via the local allowmaild daemon. " +
        "The recipient is an alias name from the daemon config, never a raw email address.",
      parameters: Type.Object({
        recipient: Type.String({
          description: "Configured recipient alias (not an email address).",
        }),
        subject: Type.String({ description: "Email subject line." }),
        text: Type.String({ description: "Plain-text message body." }),
      }),
      async execute(callId: string, params: unknown) {
        try {
          const result = await executeSendEmail(
            client,
            gate,
            callId,
            params as SendEmailParams,
          );
          return {
            content: [{ type: "text" as const, text: JSON.stringify(result) }],
            details: result,
          };
        } catch (err) {
          if (err instanceof DaemonError) {
            throw new Error(formatDaemonError(err));
          }
          throw err;
        }
      },
    });

    api.on("before_tool_call", (event: BeforeToolCallEvent) =>
      gate.beforeToolCall(event),
    );
  },
});

export default plugin;

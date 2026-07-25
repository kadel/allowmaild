// Validates the built plugin entry against openclaw.plugin.json.
//
// `openclaw plugins build/validate` only handle defineToolPlugin metadata;
// this plugin uses definePluginEntry (it needs both a tool and a hook), so the
// manifest is authored by hand and this script keeps the two in sync: it
// loads dist/index.js, runs register() against a stub API, and checks that
// the registered tools and hooks match the manifest contracts.
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const manifest = JSON.parse(
  await readFile(path.join(root, "openclaw.plugin.json"), "utf8"),
);
const entry = (await import(path.join(root, "dist/index.js"))).default;

const failures = [];
const check = (ok, msg) => {
  if (!ok) failures.push(msg);
};

check(entry && typeof entry === "object", "entry has no default export");
check(entry.id === manifest.id, `entry id ${entry.id} != manifest id ${manifest.id}`);
check(entry.name === manifest.name, `entry name ${entry.name} != manifest name ${manifest.name}`);
check(typeof entry.register === "function", "entry has no register()");

const tools = [];
const hooks = [];
entry.register({
  pluginConfig: {},
  registerTool: (tool) => tools.push(tool.name),
  on: (event) => hooks.push(event),
});

const contractTools = manifest.contracts?.tools ?? [];
check(
  JSON.stringify([...tools].sort()) === JSON.stringify([...contractTools].sort()),
  `registered tools [${tools}] != manifest contracts.tools [${contractTools}]`,
);
check(hooks.includes("before_tool_call"), "before_tool_call hook not registered");

const schemaProps = manifest.configSchema?.properties ?? {};
check("socketPath" in schemaProps, "manifest configSchema is missing socketPath");

if (failures.length > 0) {
  console.error("plugin validation failed:");
  for (const f of failures) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`plugin ok: id=${manifest.id} tools=[${tools}] hooks=[${hooks}]`);

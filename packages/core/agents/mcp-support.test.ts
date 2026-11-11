import { describe, expect, it } from "vitest";

import { providerSupportsMcpConfig } from "./mcp-support";

describe("providerSupportsMcpConfig", () => {
  it("accepts a provider whose runtime consumes mcp_config", () => {
    expect(providerSupportsMcpConfig("claude")).toBe(true);
    expect(providerSupportsMcpConfig("codebuddy")).toBe(true);
    expect(providerSupportsMcpConfig("codex")).toBe(true);
    expect(providerSupportsMcpConfig("multica")).toBe(true);
    expect(providerSupportsMcpConfig("cursor")).toBe(true);
    expect(providerSupportsMcpConfig("hermes")).toBe(true);
    expect(providerSupportsMcpConfig("kimi")).toBe(true);
    expect(providerSupportsMcpConfig("kiro")).toBe(true);
    expect(providerSupportsMcpConfig("opencode")).toBe(true);
    expect(providerSupportsMcpConfig("openclaw")).toBe(true);
    expect(providerSupportsMcpConfig("qoder")).toBe(true);
    expect(providerSupportsMcpConfig("qoderclicn")).toBe(true);
    expect(providerSupportsMcpConfig("qwen")).toBe(true);
    expect(providerSupportsMcpConfig("traecli")).toBe(true);
    expect(providerSupportsMcpConfig("grok")).toBe(true);
    expect(providerSupportsMcpConfig("devin")).toBe(true);
    expect(providerSupportsMcpConfig("zcode")).toBe(true);
  });
  it("rejects providers whose runtime ignores mcp_config", () => {
    expect(providerSupportsMcpConfig("antigravity")).toBe(false);
    expect(providerSupportsMcpConfig("copilot")).toBe(false);
    expect(providerSupportsMcpConfig("pi")).toBe(true);
    // ZeroClaw's ACP server never reads `params.mcpServers` — MCP lives in
    // ZeroClaw's own config-dir, so a value saved here could not be honoured.
    expect(providerSupportsMcpConfig("zeroclaw")).toBe(false);
    expect(providerSupportsMcpConfig(undefined)).toBe(false);
    expect(providerSupportsMcpConfig(null)).toBe(false);
  });
});

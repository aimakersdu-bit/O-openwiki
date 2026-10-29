import { describe, it, expect } from "vitest";
import { formatUsageTelemetry, extractTokenUsageFromChunk } from "../../src/agent/index.js";

describe("Token Usage Telemetry", () => {
  it("formatUsageTelemetry should produce standard OPENWIKI_USAGE json line", () => {
    const output = formatUsageTelemetry({
      promptTokens: 1250,
      completionTokens: 320,
      totalTokens: 1570,
    });

    expect(output).toContain("OPENWIKI_USAGE");
    const jsonStr = output.replace("OPENWIKI_USAGE ", "");
    const parsed = JSON.parse(jsonStr);
    expect(parsed.prompt_tokens).toBe(1250);
    expect(parsed.completion_tokens).toBe(320);
    expect(parsed.total_tokens).toBe(1570);
  });

  it("extractTokenUsageFromChunk should extract usage_metadata if present in stream chunk", () => {
    const chunkWithUsage = [
      "messages",
      [
        {
          type: "ai",
          content: "test",
          response_metadata: {
            usage: {
              prompt_tokens: 500,
              completion_tokens: 150,
              total_tokens: 650,
            },
          },
        },
      ],
    ];

    const usage = extractTokenUsageFromChunk(chunkWithUsage);
    expect(usage).not.toBeNull();
    expect(usage?.promptTokens).toBe(500);
    expect(usage?.completionTokens).toBe(150);
    expect(usage?.totalTokens).toBe(650);
  });
});

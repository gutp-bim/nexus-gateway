import { describe, expect, it } from "vitest";

import { formatTelemetryValue } from "./page";

describe("formatTelemetryValue", () => {
  it("formats numbers without changing existing precision", () => {
    expect(formatTelemetryValue(12.5)).toBe("12.5000");
  });

  it("preserves string and boolean telemetry", () => {
    expect(formatTelemetryValue("running")).toBe("running");
    expect(formatTelemetryValue(false)).toBe("false");
  });
});

import { describe, expect, it } from "vitest";
import { isValidTimezone, resolveTimezone } from "./timezone";

describe("timezone helpers", () => {
  it("accepts IANA timezones supported by the runtime", () => {
    expect(isValidTimezone("UTC")).toBe(true);
    expect(isValidTimezone("Asia/Shanghai")).toBe(true);
  });

  it("rejects legacy non-IANA timezone labels", () => {
    expect(isValidTimezone("Local")).toBe(false);
    expect(isValidTimezone("Not/AZone")).toBe(false);
  });

  it("uses a valid fallback for invalid values", () => {
    expect(resolveTimezone("Local", "Asia/Shanghai")).toBe("Asia/Shanghai");
    expect(resolveTimezone("Local", "Not/AZone")).toBe("UTC");
  });
});

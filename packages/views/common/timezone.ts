/** Return whether a value is accepted by this runtime as an IANA timezone. */
export function isValidTimezone(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const timezone = value.trim();
  if (!timezone) return false;

  try {
    new Intl.DateTimeFormat("en-US", { timeZone: timezone }).format();
    return true;
  } catch {
    return false;
  }
}

/** Resolve legacy or otherwise invalid timezone values without throwing. */
export function resolveTimezone(
  value: string | null | undefined,
  fallback = "UTC",
): string {
  const timezone = value?.trim();
  if (isValidTimezone(timezone)) return timezone;

  const fallbackTimezone = fallback.trim();
  if (isValidTimezone(fallbackTimezone)) return fallbackTimezone;

  return "UTC";
}

// Thin typed wrapper over UIStateService (JSON values keyed by string).
import { UIStateService } from "../lib/bindings/github.com/sid/harness/internal/app";

export async function loadUI<T>(key: string, fallback: T): Promise<T> {
  try {
    const s = await UIStateService.Get(key);
    return s ? ({ ...(typeof fallback === "object" && !Array.isArray(fallback) ? fallback : {}), ...JSON.parse(s) } as T) : fallback;
  } catch {
    return fallback;
  }
}
const timers = new Map<string, ReturnType<typeof setTimeout>>();
export function saveUI(key: string, value: unknown, delay = 500): void {
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => void UIStateService.Set(key, JSON.stringify(value)).catch(() => {}), delay));
}

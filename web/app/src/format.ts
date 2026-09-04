/** Formatea milisegundos como "42 s" o "1 m 52 s". */
export function duration(ms?: number): string | null {
  if (!ms) return null;
  const total = Math.round(ms / 1000);
  if (total < 60) return `${total} s`;
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes} m ${String(seconds).padStart(2, "0")} s`;
}

/** "2,3× tiempo real": cuánto más rápido que el audio fue la transcripción. */
export function speed(audioMs?: number, elapsedMs?: number): string | null {
  if (!audioMs || !elapsedMs) return null;
  const factor = audioMs / elapsedMs;
  return `${factor.toFixed(1).replace(".", ",")}× tiempo real`;
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

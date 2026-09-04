import type { Health, Job } from "./types";

async function json<T>(input: string, init?: RequestInit): Promise<T> {
  const res = await fetch(input, init);
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  health: () => json<Health>("/api/health"),

  jobs: () => json<{ jobs: Job[] }>("/api/jobs").then((d) => d.jobs),

  upload(files: FileList | File[], onProgress?: (percent: number) => void) {
    // XMLHttpRequest y no fetch: es la única forma de saber cuánto se ha subido.
    const body = new FormData();
    for (const file of Array.from(files)) body.append("files", file, file.name);

    return new Promise<void>((resolve, reject) => {
      const xhr = new XMLHttpRequest();
      xhr.open("POST", "/api/transcribe");
      xhr.upload.addEventListener("progress", (e) => {
        if (e.lengthComputable) onProgress?.(Math.round((e.loaded / e.total) * 100));
      });
      xhr.addEventListener("load", () => {
        if (xhr.status >= 200 && xhr.status < 300) return resolve();
        let message = `HTTP ${xhr.status}`;
        try {
          message = JSON.parse(xhr.responseText).error ?? message;
        } catch {
          /* respuesta no JSON: se queda el código de estado */
        }
        reject(new Error(message));
      });
      xhr.addEventListener("error", () => reject(new Error("fallo de red al subir")));
      xhr.send(body);
    });
  },

  remove: (id: string) =>
    fetch(`/api/jobs/${id}`, { method: "DELETE" }).then((res) => {
      if (!res.ok && res.status !== 404) throw new Error(`HTTP ${res.status}`);
    }),

  downloadURL: (id: string, format: string) =>
    `/api/jobs/${id}/download?format=${format}`,
};

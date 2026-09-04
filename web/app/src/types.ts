/** Estado de un trabajo, tal como lo devuelve la API de vozgo. */
export type JobStatus = "queued" | "running" | "done" | "failed";

export interface Job {
  id: string;
  filename: string;
  status: JobStatus;
  error?: string;
  language?: string;
  model?: string;
  duration_ms?: number;
  elapsed_ms?: number;
  text?: string;
  segments: number;
  outputs?: Record<string, string>;
  queued_at: string;
  finished_at?: string;
}

export interface Health {
  status: string;
  version: string;
  model: string;
  language: string;
  workers: number;
  threads: number;
  formats: string[];
  jobs: { total: number; done: number; failed: number };
}

export const FORMATS = ["txt", "srt", "vtt", "json", "md"] as const;

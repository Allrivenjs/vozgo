import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { Job } from "./types";

/**
 * Mantiene la lista de trabajos al día. La fuente principal es el stream SSE
 * de /api/events; el polling solo entra si el navegador no soporta EventSource
 * o si el stream se cae, para que la UI nunca quede congelada.
 */
export function useJobs() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [live, setLive] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  const refresh = useCallback(async () => {
    try {
      setJobs(await api.jobs());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void refresh();

    if (!window.EventSource) {
      timer.current = window.setInterval(refresh, 2000);
      return () => window.clearInterval(timer.current);
    }

    const source = new EventSource("/api/events");
    let debounce: number | undefined;

    // Varios trabajos cambian a la vez; se agrupan en una sola recarga.
    const onJob = () => {
      window.clearTimeout(debounce);
      debounce = window.setTimeout(refresh, 150);
    };

    source.addEventListener("job", onJob);
    source.addEventListener("open", () => {
      setLive(true);
      window.clearInterval(timer.current);
      timer.current = undefined;
    });
    source.addEventListener("error", () => {
      setLive(false);
      // EventSource reintenta solo; mientras tanto, polling.
      if (timer.current === undefined) {
        timer.current = window.setInterval(refresh, 3000);
      }
    });

    return () => {
      window.clearTimeout(debounce);
      window.clearInterval(timer.current);
      source.close();
    };
  }, [refresh]);

  const remove = useCallback(
    async (id: string) => {
      setJobs((prev) => prev.filter((j) => j.id !== id)); // respuesta inmediata
      await api.remove(id);
      void refresh();
    },
    [refresh],
  );

  const clearFinished = useCallback(async () => {
    const finished = jobs.filter((j) => j.status === "done" || j.status === "failed");
    await Promise.all(finished.map((j) => api.remove(j.id)));
    void refresh();
  }, [jobs, refresh]);

  return { jobs, error, live, refresh, remove, clearFinished };
}

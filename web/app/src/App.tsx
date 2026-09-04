import { useEffect, useState } from "react";
import { api } from "./api";
import { DropZone } from "./components/DropZone";
import { JobCard } from "./components/JobCard";
import { useJobs } from "./useJobs";
import type { Health } from "./types";

export default function App() {
  const { jobs, error, live, refresh, remove, clearFinished } = useJobs();
  const [health, setHealth] = useState<Health | null>(null);
  const [uploading, setUploading] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const [uploadError, setUploadError] = useState<string | null>(null);

  useEffect(() => {
    api.health().then(setHealth).catch(() => setHealth(null));
  }, [jobs.length]);

  async function upload(files: FileList | File[]) {
    setUploading(true);
    setUploadError(null);
    setProgress(0);
    try {
      await api.upload(files, setProgress);
      await refresh();
    } catch (e) {
      setUploadError(e instanceof Error ? e.message : String(e));
    } finally {
      setUploading(false);
      setProgress(null);
    }
  }

  const working = jobs.filter((j) => j.status === "queued" || j.status === "running").length;
  const finished = jobs.filter((j) => j.status === "done" || j.status === "failed").length;

  return (
    <>
      <header className="top">
        <h1>vozgo</h1>
        <span className="muted">notas de voz a texto, 100 % local</span>
        <span className="status" title={live ? "recibiendo eventos en vivo" : "sin stream, refrescando por intervalos"}>
          <i className={live ? "dot live" : "dot"} />
          {health
            ? `${health.model} · ${health.language} · ${health.workers} workers`
            : "sin conexión"}
        </span>
      </header>

      <main>
        <DropZone onFiles={upload} busy={uploading} progress={progress} />
        {uploadError && <p className="error banner">{uploadError}</p>}

        <div className="bar-row">
          <h2>
            Trabajos
            {working > 0 && <span className="muted"> · {working} en curso</span>}
          </h2>
          <div>
            <button onClick={() => void refresh()}>Refrescar</button>
            <button onClick={() => void clearFinished()} disabled={finished === 0}>
              Limpiar terminados
            </button>
          </div>
        </div>

        {error && <p className="error banner">{error}</p>}

        {jobs.length === 0 ? (
          <p className="empty">Nada todavía. Suelta una nota de voz para empezar.</p>
        ) : (
          <ul className="jobs">
            {jobs.map((job) => (
              <JobCard key={job.id} job={job} onRemove={(id) => void remove(id)} />
            ))}
          </ul>
        )}
      </main>
    </>
  );
}

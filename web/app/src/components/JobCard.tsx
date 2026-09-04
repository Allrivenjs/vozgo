import { useState } from "react";
import { api } from "../api";
import { duration, speed } from "../format";
import { FORMATS, type Job } from "../types";

const LABEL: Record<Job["status"], string> = {
  queued: "en cola",
  running: "transcribiendo",
  done: "listo",
  failed: "falló",
};

export function JobCard({ job, onRemove }: { job: Job; onRemove: (id: string) => void }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    if (!job.text) return;
    try {
      await navigator.clipboard.writeText(job.text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  const meta = [
    job.language && `idioma ${job.language}`,
    duration(job.duration_ms) && `audio ${duration(job.duration_ms)}`,
    duration(job.elapsed_ms) && `proceso ${duration(job.elapsed_ms)}`,
    speed(job.duration_ms, job.elapsed_ms),
    job.segments > 0 && `${job.segments} segmentos`,
  ].filter(Boolean) as string[];

  return (
    <li className={`job ${job.status}`}>
      <header>
        <h3 title={job.filename}>{job.filename}</h3>
        <span className={`tag ${job.status}`}>{LABEL[job.status]}</span>
        <button className="ghost" onClick={() => onRemove(job.id)} title="Olvidar este trabajo">
          ✕
        </button>
      </header>

      {meta.length > 0 && (
        <ul className="meta">
          {meta.map((m) => (
            <li key={m}>{m}</li>
          ))}
        </ul>
      )}

      {job.status === "running" && <div className="bar indeterminate" />}
      {job.error && <p className="error">{job.error}</p>}

      {job.text && (
        <>
          <p className="text">{job.text}</p>
          <div className="actions">
            <button onClick={copy}>{copied ? "Copiado" : "Copiar"}</button>
            {FORMATS.map((f) => (
              <a key={f} href={api.downloadURL(job.id, f)}>
                {f.toUpperCase()}
              </a>
            ))}
          </div>
        </>
      )}
    </li>
  );
}

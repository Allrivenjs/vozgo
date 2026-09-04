import { useRef, useState } from "react";
import { bytes } from "../format";

interface Props {
  onFiles: (files: FileList | File[]) => void;
  busy: boolean;
  progress: number | null;
}

/** Zona de arrastrar y soltar, con selector de archivos como alternativa. */
export function DropZone({ onFiles, busy, progress }: Props) {
  const [over, setOver] = useState(false);
  const input = useRef<HTMLInputElement>(null);

  return (
    <section
      className={`drop${over ? " over" : ""}`}
      onDragEnter={(e) => {
        e.preventDefault();
        setOver(true);
      }}
      onDragOver={(e) => e.preventDefault()}
      onDragLeave={(e) => {
        e.preventDefault();
        setOver(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        if (e.dataTransfer.files.length) onFiles(e.dataTransfer.files);
      }}
    >
      <p className="drop-title">Arrastra tus notas de voz aquí</p>
      <p className="muted">.ogg · .opus · .m4a · .mp3 · .wav · y lo que ffmpeg sepa leer</p>
      <button className="primary" onClick={() => input.current?.click()} disabled={busy}>
        {busy ? "Subiendo…" : "Elegir archivos"}
      </button>
      <input
        ref={input}
        type="file"
        multiple
        hidden
        accept="audio/*,video/*,.ogg,.opus,.oga,.amr"
        onChange={(e) => {
          if (e.target.files?.length) onFiles(e.target.files);
          e.target.value = "";
        }}
      />
      {progress !== null && (
        <div className="progress" role="progressbar" aria-valuenow={progress}>
          <div className="progress-bar" style={{ width: `${progress}%` }} />
        </div>
      )}
    </section>
  );
}

/** Resumen de lo que se acaba de soltar, antes de que el servidor responda. */
export function PendingSummary({ files }: { files: File[] }) {
  if (!files.length) return null;
  const total = files.reduce((sum, f) => sum + f.size, 0);
  return (
    <p className="muted">
      {files.length} archivo{files.length > 1 ? "s" : ""} · {bytes(total)}
    </p>
  );
}

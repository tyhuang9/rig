import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

const buildLabel = import.meta.env.VITE_BUILD_LABEL || "unlabeled";

function App() {
  const [notes, setNotes] = useState([]);
  const [body, setBody] = useState("");
  const [status, setStatus] = useState("Loading notes…");
  const [version, setVersion] = useState("Loading…");

  async function loadNotes() {
    const response = await fetch("/api/notes");
    if (!response.ok) throw new Error("Notes are unavailable");
    const payload = await response.json();
    setNotes(payload.notes);
  }

  useEffect(() => {
    Promise.all([
      loadNotes(),
      fetch("/api/version").then((response) => response.ok ? response.json() : Promise.reject(new Error("Version unavailable")))
    ]).then(([, payload]) => {
      setVersion(`${payload.sourceVersion} · ${payload.runtimeMarker}`);
      setStatus("Ready");
    }).catch(() => setStatus("The API is unavailable."));
  }, []);

  async function addNote(event) {
    event.preventDefault();
    const trimmed = body.trim();
    if (!trimmed) return;
    setStatus("Saving…");
    try {
      const response = await fetch("/api/notes", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ body: trimmed })
      });
      if (!response.ok) throw new Error("Save failed");
      setBody("");
      await loadNotes();
      setStatus("Saved");
    } catch {
      setStatus("The note could not be saved.");
    }
  }

  return <main>
    <p className="eyebrow">Rig F1 fixture</p>
    <h1>Hosting notes</h1>
    <dl>
      <div><dt>Build label</dt><dd>{buildLabel}</dd></div>
      <div><dt>API version</dt><dd>{version}</dd></div>
    </dl>
    <form onSubmit={addNote}>
      <label htmlFor="note">New note</label>
      <div className="add-note">
        <input id="note" value={body} maxLength="500" onChange={(event) => setBody(event.target.value)} />
        <button type="submit">Add note</button>
      </div>
    </form>
    <p role="status">{status}</p>
    <ul aria-label="Notes">{notes.map((note) => <li key={note.id}>{note.body}</li>)}</ul>
  </main>;
}

createRoot(document.getElementById("root")).render(<StrictMode><App /></StrictMode>);

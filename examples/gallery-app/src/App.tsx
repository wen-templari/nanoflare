import { useEffect, useState } from "react";

type GalleryItem = {
  id: string;
  key: string;
  filename: string;
  contentType: string;
  uploadedAt: string;
  size: number;
};

type GalleryResponse = {
  items: GalleryItem[];
};

type UploadResponse = {
  ok: true;
  item: GalleryItem;
};

type UploadErrorResponse = {
  ok?: false;
  error?: string;
};

export function App() {
  const [items, setItems] = useState<GalleryItem[]>([]);
  const [status, setStatus] = useState("Loading gallery...");
  const [isUploading, setIsUploading] = useState(false);

  useEffect(() => {
    let active = true;

    async function loadGallery() {
      try {
        const response = await fetch("/api/gallery");
        if (!response.ok) {
          throw new Error("Gallery unavailable");
        }

        const payload = (await response.json()) as GalleryResponse;
        if (!active) return;

        const nextItems = payload.items ?? [];
        setItems(nextItems);
        setStatus(nextItems.length ? "Gallery ready." : "No uploads yet. Add the first image.");
      } catch {
        if (!active) return;
        setStatus("Gallery unavailable.");
      }
    }

    void loadGallery();
    return () => {
      active = false;
    };
  }, []);

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const input = form.elements.namedItem("image");
    if (!(input instanceof HTMLInputElement)) {
      setStatus("Image input missing.");
      return;
    }

    const file = input.files?.[0];
    if (!file) {
      setStatus("Choose an image first.");
      return;
    }

    setIsUploading(true);
    setStatus(`Uploading ${file.name}...`);

    const body = new FormData();
    body.set("image", file);

    try {
      const response = await fetch("/api/gallery", {
        method: "POST",
        body,
      });
      const payload = (await response.json()) as UploadResponse | UploadErrorResponse;
      if (!response.ok || !("item" in payload)) {
        const message = "error" in payload ? payload.error : undefined;
        throw new Error(message || "Upload failed");
      }

      setItems((current) => [payload.item, ...current].slice(0, 24));
      setStatus(`Uploaded ${payload.item.filename}.`);
      form.reset();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Upload failed");
    } finally {
      setIsUploading(false);
    }
  }

  return (
    <main className="page">
      <section className="hero">
        <p className="eyebrow">React + Vite + KV + object storage</p>
        <h1>Upload images and watch the gallery fill itself.</h1>
        <p className="lede">
          The gallery interface is bundled by Vite, image bytes live in object
          storage, and the gallery index is stored in KV. The Worker keeps
          <code> /api/*</code> dynamic while the rest of the app ships as static assets.
        </p>
      </section>

      <section className="panel">
        <form className="upload-form" onSubmit={handleSubmit}>
          <label className="upload-prompt" htmlFor="image-input">
            <span>Choose an image</span>
            <small>PNG, JPG, GIF, or WebP all work well here.</small>
          </label>
          <input id="image-input" name="image" type="file" accept="image/*" required />
          <button type="submit" disabled={isUploading}>
            {isUploading ? "Uploading..." : "Upload image"}
          </button>
        </form>
        <p className="status">{status}</p>
      </section>

      <section className="gallery-section" aria-live="polite">
        {items.length ? (
          <div className="gallery">
            {items.map((item) => (
              <article className="card" key={item.id}>
                <img src={`/api/gallery/${item.id}`} alt={item.filename} loading="lazy" />
                <div className="meta">
                  <strong>{item.filename}</strong>
                  <span>
                    {formatBytes(item.size)} • {new Date(item.uploadedAt).toLocaleString()}
                  </span>
                </div>
              </article>
            ))}
          </div>
        ) : (
          <div className="empty">No images yet. Upload one above.</div>
        )}
      </section>
    </main>
  );
}

function formatBytes(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

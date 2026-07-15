import { useEffect, useRef, useState } from "react"

type GalleryItem = {
  id: string
  key: string
  filename: string
  contentType: string
  uploadedAt: string
  size: number
  previewCount: number
}

type GalleryResponse = {
  items: GalleryItem[]
}

type UploadResponse = {
  ok: true
  item: GalleryItem
}

type UploadErrorResponse = {
  ok?: false
  error?: string
}

type DeleteResponse = {
  ok: true
  id: string
}

export function App() {
  const [items, setItems] = useState<GalleryItem[]>([])
  const [status, setStatus] = useState("Loading gallery...")
  const [isUploading, setIsUploading] = useState(false)
  const [selectedItem, setSelectedItem] = useState<GalleryItem | null>(null)
  const [previewingId, setPreviewingId] = useState<string | null>(null)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    let active = true

    async function loadGallery() {
      try {
        const response = await fetch("/api/gallery")
        if (!response.ok) {
          throw new Error("Gallery unavailable")
        }

        const payload = (await response.json()) as GalleryResponse
        if (!active) return

        const nextItems = payload.items ?? []
        setItems(nextItems)
        setStatus(nextItems.length ? "" : "No uploads yet. Add the first image.")
      } catch {
        if (!active) return
        setStatus("Gallery unavailable.")
      }
    }

    void loadGallery()
    return () => {
      active = false
    }
  }, [])

  async function uploadFile(file: File) {
    console.log("[gallery ui] selected file", {
      name: file.name,
      browserType: file.type,
      size: file.size,
      lastModified: file.lastModified,
    })

    setIsUploading(true)
    setStatus(`Uploading ${file.name}...`)

    const body = new FormData()
    body.set("image", file)

    try {
      const response = await fetch("/api/gallery", {
        method: "POST",
        body,
      })
      const payload = (await response.json()) as UploadResponse | UploadErrorResponse
      if (!response.ok || !("item" in payload)) {
        const message = "error" in payload ? payload.error : undefined
        throw new Error(message || "Upload failed")
      }

      console.log("[gallery ui] upload response", payload.item)

      setItems((current) => [payload.item, ...current].slice(0, 24))
      setStatus(`Uploaded ${payload.item.filename}.`)
      if (fileInputRef.current) {
        fileInputRef.current.value = ""
      }
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Upload failed")
    } finally {
      setIsUploading(false)
    }
  }

  function handleFileChange(event: React.ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0] ?? null
    if (file) {
      void uploadFile(file)
    }
  }

  async function handlePreview(item: GalleryItem) {
    setPreviewingId(item.id)

    try {
      const response = await fetch(`/api/gallery/${item.id}/preview`, {
        method: "POST",
      })
      const payload = (await response.json()) as UploadResponse | UploadErrorResponse
      if (!response.ok || !("item" in payload)) {
        const message = "error" in payload ? payload.error : undefined
        throw new Error(message || "Preview unavailable")
      }

      setItems((current) =>
        current.map((candidate) => (candidate.id === payload.item.id ? payload.item : candidate)),
      )
      setSelectedItem(payload.item)
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Preview unavailable")
    } finally {
      setPreviewingId(null)
    }
  }

  async function handleDelete(item: GalleryItem) {
    setDeletingId(item.id)

    try {
      const response = await fetch(`/api/gallery/${item.id}`, {
        method: "DELETE",
      })
      const payload = (await response.json()) as DeleteResponse | UploadErrorResponse
      if (!response.ok || !("id" in payload)) {
        const message = "error" in payload ? payload.error : undefined
        throw new Error(message || "Delete failed")
      }

      setItems((current) => current.filter((candidate) => candidate.id !== payload.id))
      setSelectedItem((current) => (current?.id === payload.id ? null : current))
      setStatus(`Deleted ${item.filename}.`)
    } catch (error) {
      setStatus(error instanceof Error ? error.message : "Delete failed")
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <main className="min-h-screen bg-slate-50 text-slate-900">
      <div className="mx-auto max-w-6xl px-4 py-12 sm:px-6 lg:px-8">
        <input
          ref={fileInputRef}
          id="image-input"
          name="image"
          type="file"
          accept="image/*"
          className="hidden"
          onChange={handleFileChange}
        />

        <section>
          <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
            <div className="max-w-3xl">
              <p className="mb-3 text-xs font-semibold uppercase tracking-wide text-sky-700">
                React + Vite + KV + object storage
              </p>
              <h1 className="text-4xl font-bold tracking-tight text-slate-900 sm:text-5xl">
                Gallery
              </h1>
              <p className="mt-4 max-w-2xl text-base text-slate-600">
                The gallery interface is bundled by Vite, image bytes live in object storage, and
                the gallery index is stored in KV. The Worker keeps <code>/api/*</code> dynamic while
                the rest of the app ships as static assets.
              </p>
            </div>

            <button
              type="button"
              disabled={isUploading}
              onClick={() => fileInputRef.current?.click()}
              className="shrink-0 rounded-lg bg-slate-900 px-5 py-3 text-sm font-semibold text-white hover:bg-slate-800 disabled:cursor-wait disabled:opacity-70"
            >
              {isUploading ? "Uploading..." : "Upload image"}
            </button>
          </div>

          <div className="mt-4 min-h-6 text-sm text-slate-500" aria-live="polite">
            {status}
          </div>
        </section>

        <section className="mt-6" aria-live="polite">
          {items.length ? (
            <div className="grid gap-5 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
              {items.map((item) => (
                <article
                  className="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm"
                  key={item.id}
                >
                  <button
                    type="button"
                    onClick={() => void handlePreview(item)}
                    disabled={previewingId === item.id || deletingId === item.id}
                    className="block w-full bg-transparent p-0 disabled:cursor-wait disabled:opacity-70"
                  >
                    <img
                      src={`/api/gallery/${item.id}`}
                      alt={item.filename}
                      loading="lazy"
                      className="block aspect-square w-full bg-slate-100 object-cover"
                    />
                    <span className="sr-only">
                      {previewingId === item.id ? `Opening ${item.filename}` : `Preview ${item.filename}`}
                    </span>
                  </button>
                  <div className="flex justify-between px-4 pb-4 pt-4">
                    <div className="flex flex-col space-y-1">
                      <strong className="truncate text-sm font-semibold text-slate-900">{item.filename}</strong>
                      <span className="text-sm text-slate-500">
                        {formatBytes(item.size)} • {new Date(item.uploadedAt).toLocaleString()}
                      </span>
                    </div>
                    <div>
                      <span className="text-sm text-slate-500">{formatPreviews(item.previewCount)}</span>
                    </div>
                  </div>
                  {/* <div className="flex gap-2 px-4 pb-4">
                    <button
                      type="button"
                      className="rounded-lg bg-slate-100 px-4 py-2 text-sm font-medium text-slate-900 hover:bg-slate-200 disabled:cursor-wait disabled:opacity-70"
                      onClick={() => void handlePreview(item)}
                      disabled={previewingId === item.id || deletingId === item.id}
                    >
                      {previewingId === item.id ? "Opening..." : "Preview"}
                    </button>
                    <button
                      type="button"
                      className="rounded-lg bg-red-50 px-4 py-2 text-sm font-medium text-red-700 hover:bg-red-100 disabled:cursor-wait disabled:opacity-70"
                      onClick={() => void handleDelete(item)}
                      disabled={deletingId === item.id || previewingId === item.id}
                    >
                      {deletingId === item.id ? "Deleting..." : "Delete"}
                    </button>
                  </div> */}
                </article>
              ))}
            </div>
          ) : (
            <div className="rounded-2xl border border-dashed border-slate-300 bg-white p-8 text-center text-slate-500">
              No images yet. Upload one above.
            </div>
          )}
        </section>
      </div>

      {selectedItem ? (
        <div
          className="fixed inset-0 z-20 grid place-items-center bg-slate-950/60 p-4"
          role="dialog"
          aria-modal="true"
          aria-labelledby="preview-title"
        >
          <div className="relative grid w-full max-w-7xl grid-cols-1 gap-6 rounded-2xl bg-white p-4 shadow-2xl md:grid-cols-[minmax(0,1.8fr)_minmax(240px,0.55fr)] md:p-5">
            <button
              type="button"
              className="absolute right-4 top-4 rounded-lg bg-slate-900 px-4 py-2 text-sm font-medium text-white hover:bg-slate-800"
              onClick={() => setSelectedItem(null)}
              aria-label="Close preview"
            >
              Close
            </button>
            <img
              className="h-auto max-h-[82vh] w-full rounded-xl bg-slate-100 object-contain"
              src={`/api/gallery/${selectedItem.id}`}
              alt={selectedItem.filename}
            />
            <div className="min-w-0 flex flex-col justify-end gap-3">
              <h2
                id="preview-title"
                className="max-w-full overflow-hidden text-pretty break-words text-lg font-semibold text-slate-900 sm:text-xl"
              >
                {selectedItem.filename}
              </h2>
              <p className="text-sm text-slate-500">
                {formatBytes(selectedItem.size)} • {new Date(selectedItem.uploadedAt).toLocaleString()}
              </p>
              <p className="text-sm text-slate-500">{formatPreviews(selectedItem.previewCount)}</p>
              <div className="mt-2">
                <button
                  type="button"
                  className="rounded-lg bg-red-50 px-4 py-2 text-sm font-medium text-red-700 hover:bg-red-100 disabled:cursor-wait disabled:opacity-70"
                  onClick={() => void handleDelete(selectedItem)}
                  disabled={deletingId === selectedItem.id}
                >
                  {deletingId === selectedItem.id ? "Deleting..." : "Delete image"}
                </button>
              </div>
            </div>
          </div>
        </div>
      ) : null}
    </main>
  )
}

function formatBytes(size: number): string {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / (1024 * 1024)).toFixed(1)} MB`
}

function formatPreviews(count: number): string {
  return `${count} ${count === 1 ? "preview" : "previews"}`
}

const form = document.getElementById("upload-form");
const input = document.getElementById("image-input");
const status = document.getElementById("status");
const gallery = document.getElementById("gallery");

void loadGallery();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  const file = input.files?.[0];
  if (!file) {
    status.textContent = "Choose an image first.";
    return;
  }

  status.textContent = `Uploading ${file.name}...`;
  const body = new FormData();
  body.set("image", file);

  try {
    const response = await fetch("/api/gallery", {
      method: "POST",
      body,
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.error || "Upload failed");
    }
    form.reset();
    status.textContent = `Uploaded ${payload.item.filename}.`;
    await loadGallery();
  } catch (error) {
    status.textContent = error instanceof Error ? error.message : "Upload failed";
  }
});

async function loadGallery() {
  try {
    const response = await fetch("/api/gallery");
    const payload = await response.json();
    renderGallery(payload.items || []);
    status.textContent = payload.items?.length ? "Gallery ready." : "No uploads yet. Add the first image.";
  } catch {
    status.textContent = "Gallery unavailable.";
  }
}

function renderGallery(items) {
  if (!items.length) {
    gallery.innerHTML = '<div class="empty">No images yet. Upload one above.</div>';
    return;
  }

  gallery.innerHTML = items.map((item) => `
    <article class="card">
      <img src="/api/gallery/${item.id}" alt="${escapeHTML(item.filename)}" loading="lazy" />
      <div class="meta">
        <strong>${escapeHTML(item.filename)}</strong>
        <span>${formatBytes(item.size)} • ${new Date(item.uploadedAt).toLocaleString()}</span>
      </div>
    </article>
  `).join("");
}

function formatBytes(size) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}

function escapeHTML(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}

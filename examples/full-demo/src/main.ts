import "./styles.css"

const visits = document.getElementById("visits")

fetch("/api/visits")
  .then((response) => response.json())
  .then((payload: { visits: number }) => {
    if (visits) {
      visits.textContent = String(payload.visits)
    }
  })
  .catch(() => {
    if (visits) {
      visits.textContent = "unavailable"
    }
  })

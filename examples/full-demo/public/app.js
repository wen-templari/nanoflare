const visits = document.getElementById("visits");

fetch("/api/visits")
  .then((response) => response.json())
  .then((payload) => {
    visits.textContent = String(payload.visits);
  })
  .catch(() => {
    visits.textContent = "unavailable";
  });

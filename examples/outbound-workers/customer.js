export default {
  fetch(request) {
    const path = new URL(request.url).pathname;
    return fetch(`https://api.example.com${path}`);
  },
};

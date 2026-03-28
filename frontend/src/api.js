export async function fetchJSON(url, options) {
  const response = await fetch(url, options);
  if (response.ok) return response.json();

  let message = "Запрос завершился ошибкой";
  try {
    const payload = await response.json();
    message = payload.error || message;
  } catch {
    // ignore malformed response body
  }

  throw new Error(message);
}

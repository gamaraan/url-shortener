<script lang="ts">
  interface ShortenResponse {
    short_url: string;
    shortcode: string;
    destination: string;
  }

  interface ErrorResponse {
    error: string;
  }

  let destination = "";
  let shortURL = "";
  let loading = false;
  let errorMsg = "";
  let copied = false;

  async function shorten() {
    errorMsg = "";
    shortURL = "";
    copied = false;
    const trimmed = destination.trim();
    if (!trimmed) {
      errorMsg = "destination is required";
      return;
    }
    loading = true;
    try {
      const res = await fetch("/api/shorten", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ destination: trimmed }),
      });
      const body = await res.json();
      if (!res.ok) {
        errorMsg = (body as ErrorResponse).error ?? "failed to shorten";
        return;
      }
      const data = body as ShortenResponse;
      // Build the short URL from the host the SPA was loaded from + the
      // returned shortcode (the backend's short_url is informational).
      shortURL = `${window.location.origin}/${data.shortcode}`;
    } catch (err) {
      errorMsg = "network error: " + String(err);
    } finally {
      loading = false;
    }
  }

  async function copy() {
    if (!shortURL) return;
    try {
      await navigator.clipboard.writeText(shortURL);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      errorMsg = "could not copy to clipboard";
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter") shorten();
  }
</script>

<main>
  <h1>URL Shortener</h1>
  <p class="subtitle">Paste a long URL and get a short one.</p>

  <div class="row">
    <input
      type="url"
      placeholder="https://example.com/very/long/url"
      bind:value={destination}
      on:keydown={onKey}
      aria-label="URL to shorten"
    />
    <button on:click={shorten} disabled={loading}>
      {loading ? "Shortening…" : "Shorten"}
    </button>
  </div>

  {#if errorMsg}
    <p class="error" role="alert">{errorMsg}</p>
  {/if}

  {#if shortURL}
    <div class="result">
      <a href={shortURL} class="short-link">{shortURL}</a>
      <button class="copy" on:click={copy}>{copied ? "Copied!" : "Copy"}</button>
    </div>
  {/if}
</main>

<style>
  :global(body) {
    margin: 0;
    background: #0d1117;
    color: #e6edf3;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial,
      sans-serif;
  }

  main {
    max-width: 640px;
    margin: 0 auto;
    padding: 4rem 1.5rem;
  }

  h1 {
    font-size: 2rem;
    margin: 0 0 0.25rem;
    color: #e6edf3;
  }

  .subtitle {
    margin: 0 0 2rem;
    color: #7d8590;
  }

  .row {
    display: flex;
    gap: 0.5rem;
  }

  input {
    flex: 1;
    padding: 0.6rem 0.75rem;
    background: #161b22;
    border: 1px solid #30363d;
    border-radius: 6px;
    color: #e6edf3;
    font-size: 1rem;
  }

  input:focus {
    outline: none;
    border-color: #f0883e;
    box-shadow: 0 0 0 3px rgba(240, 136, 62, 0.2);
  }

  button {
    padding: 0.6rem 1.1rem;
    background: #f0883e;
    color: #0d1117;
    border: none;
    border-radius: 6px;
    font-size: 1rem;
    font-weight: 600;
    cursor: pointer;
  }

  button:hover:not(:disabled) {
    background: #ff9b57;
  }

  button:disabled {
    opacity: 0.6;
    cursor: default;
  }

  .error {
    margin-top: 1rem;
    padding: 0.6rem 0.75rem;
    background: rgba(248, 81, 73, 0.1);
    border: 1px solid rgba(248, 81, 73, 0.4);
    border-radius: 6px;
    color: #ff7b72;
  }

  .result {
    margin-top: 1.5rem;
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  .short-link {
    color: #f0883e;
    font-size: 1.1rem;
    word-break: break-all;
  }

  .short-link:hover {
    color: #ff9b57;
  }

  .copy {
    padding: 0.4rem 0.75rem;
    background: #21262d;
    color: #e6edf3;
    border: 1px solid #30363d;
    font-weight: 500;
  }

  .copy:hover {
    border-color: #f0883e;
  }
</style>
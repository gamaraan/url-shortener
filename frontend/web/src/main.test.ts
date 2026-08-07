import { describe, it, expect, vi } from "vitest";

// Regression test for the blank-page bug: the legacy `new App({ target })`
// constructor does not mount under Svelte 5, so the page stayed empty (only
// the CSS background was visible). This test exercises the actual bootstrap
// (main.ts) in a jsdom DOM and asserts that #app gets populated with the
// rendered component. With the old `new App()` API, #app stays empty and this
// test fails; with the Svelte 5 `mount()` API it passes.
describe("bootstrap (main.ts)", () => {
  it("mounts the app into #app", async () => {
    // Set up the mount target the real index.html provides.
    document.body.innerHTML = '<div id="app"></div>';

    // The bootstrap runs mount() at import time; reset modules so a re-run
    // re-executes it against the fresh #app.
    vi.resetModules();
    await import("./main");

    const app = document.getElementById("app");
    expect(app).not.toBeNull();
    expect(app!.textContent).toContain("URL Shortener");
    expect(app!.querySelector("button")?.textContent).toMatch(/shorten/i);
  });
});
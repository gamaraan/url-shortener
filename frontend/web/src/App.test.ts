import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/svelte";
import App from "./App.svelte";

// Regression test for the blank-page bug: under Svelte 5 the legacy
// `new App({ target })` constructor did not mount the component, leaving the
// page empty. This test renders App into a DOM (jsdom) and asserts the
// heading and the Shorten button are present — i.e. the component actually
// mounted and rendered. With the old mount API this test fails (nothing is
// rendered into the container); with the Svelte 5 `mount()` API it passes.
describe("App", () => {
  it("renders the heading", () => {
    render(App);
    expect(screen.getByText("URL Shortener")).toBeTruthy();
  });

  it("renders the shorten button", () => {
    render(App);
    expect(screen.getByRole("button", { name: /shorten/i })).toBeTruthy();
  });

  it("renders the URL input", () => {
    render(App);
    expect(
      screen.getByPlaceholderText(/example\.com/i),
    ).toBeTruthy();
  });
});
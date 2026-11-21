// @vitest-environment jsdom
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import App from "./App";

describe("App", () => {
  it("renders the phase 1 shell", () => {
    render(<App />);
    expect(screen.getByRole("heading", { name: /phase 1/i })).toBeTruthy();
  });
});

import { configure } from "@testing-library/react";

// The full suite runs several React apps in parallel; keep async UI waits from
// failing on scheduler contention rather than a real missing element.
configure({ asyncUtilTimeout: 5000 });

// jsdom implements no layout, so it has no scrollIntoView at all. Panels call it
// to keep a reordered or recreated row in view; without a stub those calls throw
// inside requestAnimationFrame, outside any test's error boundary.
if (typeof Element !== "undefined" && !Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

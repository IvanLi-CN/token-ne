import { configure } from "@testing-library/react";

// The full suite runs several React apps in parallel; keep async UI waits from
// failing on scheduler contention rather than a real missing element.
configure({ asyncUtilTimeout: 5000 });

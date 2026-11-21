import { type PointerEvent, type ReactNode, useEffect, useRef } from "react";

export function ProximitySurface({
  children,
  className = "",
}: {
  children: ReactNode;
  className?: string;
}) {
  const frame = useRef<number | null>(null);
  const surface = useRef<HTMLElement>(null);
  useEffect(
    () => () => {
      if (frame.current !== null) cancelAnimationFrame(frame.current);
    },
    [],
  );
  function update(event: PointerEvent<HTMLElement>) {
    if (
      window.matchMedia("(prefers-reduced-motion: reduce)").matches ||
      event.pointerType !== "mouse"
    )
      return;
    const element = surface.current;
    if (!element) return;
    const rect = element.getBoundingClientRect();
    const x = event.clientX - rect.left;
    const y = event.clientY - rect.top;
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      element.style.setProperty("--pointer-x", `${x}px`);
      element.style.setProperty("--pointer-y", `${y}px`);
      frame.current = null;
    });
  }
  function clear() {
    if (frame.current !== null) cancelAnimationFrame(frame.current);
    frame.current = null;
    surface.current?.style.removeProperty("--pointer-x");
    surface.current?.style.removeProperty("--pointer-y");
  }
  return (
    <section
      ref={surface}
      className={`proximity-surface ${className}`}
      onPointerMove={update}
      onPointerLeave={clear}
      onPointerCancel={clear}
    >
      {children}
    </section>
  );
}

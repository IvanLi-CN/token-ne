import * as Dialog from "@radix-ui/react-dialog";
import {
  cloneElement,
  type ReactElement,
  type ReactNode,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { useBlocker } from "react-router";
import { ConfirmDialog } from "../../components/confirm-dialog";
import { Button } from "../../components/primitives";
import { ApiError } from "../../lib/api";

export function errorMessage(error: unknown): string | undefined {
  if (!error) return undefined;
  if (error instanceof ApiError)
    return [
      error.message,
      ...error.details.map((item) => `${item.path}: ${item.reason}`),
      error.requestId ? `请求 ID: ${error.requestId}` : "",
    ]
      .filter(Boolean)
      .join("；");
  return error instanceof Error ? error.message : "操作失败，请重试。";
}
export function FormError({ error }: { error: unknown }) {
  return error ? (
    <p role="alert" className="field-error">
      {errorMessage(error)}
    </p>
  ) : null;
}
export function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactElement<{ id?: string; "aria-describedby"?: string }>;
  hint?: string;
}) {
  const id = useId();
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {cloneElement(children, { id, "aria-describedby": hint ? `${id}-hint` : undefined })}
      {hint && (
        <small id={`${id}-hint`} className="muted">
          {hint}
        </small>
      )}
    </div>
  );
}
export function ActionGroup({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-2">{children}</div>;
}
export function Editor({
  title,
  description,
  children,
  onClose,
  dirty = false,
  pending = false,
  drawer = false,
}: {
  title: string;
  description: string;
  children: ReactNode;
  onClose: () => void;
  dirty?: boolean;
  pending?: boolean;
  drawer?: boolean;
}) {
  const [discard, setDiscard] = useState(false);
  const trigger = useRef(document.activeElement);
  const blocker = useBlocker(dirty || pending);
  const id = useId();
  useEffect(() => {
    if (!dirty && !pending) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty, pending]);
  function close() {
    if (pending) return;
    if (dirty) setDiscard(true);
    else onClose();
  }
  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            if (trigger.current instanceof HTMLElement) trigger.current.focus();
          }}
          style={
            drawer
              ? {
                  top: 0,
                  bottom: 0,
                  left: "auto",
                  right: 0,
                  transform: "none",
                  width: "min(900px, 100%)",
                  maxHeight: "100dvh",
                  borderRadius: "14px 0 0 14px",
                }
              : { top: "8%", maxHeight: "84dvh", width: "min(680px, calc(100% - 32px))" }
          }
          onEscapeKeyDown={(event) => {
            event.preventDefault();
            close();
          }}
          onPointerDownOutside={(event) => {
            event.preventDefault();
          }}
        >
          <div className="flex items-start justify-between gap-4">
            <Dialog.Title>{title}</Dialog.Title>
            <Button variant="secondary" disabled={pending} onClick={close}>
              关闭
            </Button>
          </div>
          <Dialog.Description>{description}</Dialog.Description>
          <div className="mt-6" id={id}>
            {children}
          </div>
          <ConfirmDialog
            trigger={
              <button
                type="button"
                tabIndex={-1}
                className="visually-hidden"
                onFocus={() =>
                  document
                    .getElementById(id)
                    ?.querySelector<HTMLElement>("input, select, textarea, button")
                    ?.focus()
                }
              >
                返回表单
              </button>
            }
            title="放弃未保存修改？"
            description="离开后表单中的修改将丢失。"
            open={discard || blocker.state === "blocked"}
            pending={pending}
            onOpenChange={(open) => {
              if (!open) {
                setDiscard(false);
                if (blocker.state === "blocked") blocker.reset();
              }
            }}
            onConfirm={() => {
              if (blocker.state === "blocked") blocker.proceed();
              else onClose();
            }}
            confirmLabel="放弃修改"
          />
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
export function DangerAction({
  label,
  title,
  description,
  action,
  disabled = false,
}: {
  label: string;
  title: string;
  description: string;
  action: () => Promise<unknown>;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<unknown>();
  return (
    <ConfirmDialog
      trigger={
        <Button variant="secondary" disabled={disabled}>
          {label}
        </Button>
      }
      title={title}
      description={description}
      open={open}
      onOpenChange={(value) => {
        setOpen(value);
        setError(undefined);
      }}
      pending={pending}
      error={errorMessage(error)}
      onConfirm={() => {
        if (pending) return;
        setPending(true);
        setError(undefined);
        void action()
          .then(() => setOpen(false))
          .catch(setError)
          .finally(() => setPending(false));
      }}
    />
  );
}

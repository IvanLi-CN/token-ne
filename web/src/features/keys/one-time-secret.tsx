import * as Dialog from "@radix-ui/react-dialog";
import { useEffect, useState } from "react";
import { useBlocker } from "react-router";
import { Button } from "../../components/primitives";

export function SecretNavigationGuard() {
  const blocker = useBlocker(true);
  useEffect(() => {
    if (blocker.state === "blocked") blocker.reset();
  }, [blocker]);
  useEffect(() => {
    const handler = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, []);
  return null;
}
export function OneTimeSecret({ secret, onDismiss }: { secret: string; onDismiss: () => void }) {
  const [acknowledged, setAcknowledged] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  return (
    <Dialog.Root open>
      <SecretNavigationGuard />
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          onEscapeKeyDown={(event) => event.preventDefault()}
          onPointerDownOutside={(event) => event.preventDefault()}
        >
          <Dialog.Title>保存一次性 Key</Dialog.Title>
          <Dialog.Description>
            明文仅展示这一次。请立即复制并安全保存，关闭后无法恢复。
          </Dialog.Description>
          <textarea
            aria-label="一次性明文 Key"
            className="input mt-5 min-h-24 resize-none break-all rounded-xl font-mono"
            readOnly
            value={secret}
            onFocus={(event) => event.currentTarget.select()}
          />
          <div className="mt-4 flex flex-col gap-4">
            <Button
              variant="secondary"
              onClick={() => {
                setCopyError(false);
                void Promise.resolve()
                  .then(() => navigator.clipboard.writeText(secret))
                  .then(() => setCopied(true))
                  .catch(() => setCopyError(true));
              }}
            >
              {copied ? "已复制" : "复制 Key"}
            </Button>
            {copyError && (
              <p role="alert" className="field-error">
                复制失败，请选中文本手动复制。
              </p>
            )}
            <label className="flex items-center gap-2">
              <input
                type="checkbox"
                checked={acknowledged}
                onChange={(event) => setAcknowledged(event.target.checked)}
              />
              我已复制并安全保存此 Key
            </label>
          </div>
          <div className="dialog-actions">
            <Button disabled={!acknowledged} onClick={onDismiss}>
              完成并清除明文
            </Button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

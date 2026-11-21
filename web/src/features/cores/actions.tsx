import * as Dialog from "@radix-ui/react-dialog";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useId, useState } from "react";
import { Button, Input } from "../../components/primitives";
import { ApiError } from "../../lib/api";
import { queryKeys } from "../../lib/query-client";
import { type Command, coresApi, type Deployment, type Instance, type Version } from "./api";

export type Action =
  | { kind: "register" }
  | { kind: "deploy"; version: Version }
  | { kind: "shift" | "rollback"; deployment: Deployment }
  | { kind: "drain" | "force" | "prune"; instance: Instance };

const copy = {
  register: ["登记版本", "登记镜像及其协议和配置兼容范围。"],
  deploy: ["开始预热", "启动指定版本的实例，初始流量权重为 0。"],
  shift: ["切换权重", "修改部署的流量分配。控制面会验证阶段和目标权重。"],
  rollback: ["回滚部署", "流量将恢复到部署前的版本和权重。"],
  drain: [
    "排水实例",
    "将实例移出流量分配，并等待在途请求完成。排水最后一个实例可能导致服务返回 503。",
  ],
  force: ["强制排水", "立即停止实例可能中断在途请求。请输入原因并确认影响。"],
  prune: ["清理实例", "永久清理已停止实例的状态目录。"],
} as const;

export function coreError(error: Error) {
  if (!(error instanceof ApiError)) return error.message;
  return [
    error.message,
    ...error.details.map((detail) => `${detail.path} · ${detail.reason}`),
    error.requestId && `请求 ID ${error.requestId}`,
  ]
    .filter(Boolean)
    .join(" · ");
}

function buildCommand(action: Action, fields: FormData): Command {
  const read = (name: string) => String(fields.get(name) ?? "").trim();
  switch (action.kind) {
    case "register": {
      const body = {
        version: read("version"),
        image_ref: read("image_ref"),
        image_digest: read("image_digest") || null,
        protocol_version: Number(read("protocol_version")),
        config_schema_min: Number(read("config_schema_min")),
        config_schema_max: Number(read("config_schema_max")),
      };
      if (!body.version || !body.image_ref) throw new Error("请输入版本和镜像。");
      if (body.config_schema_min > body.config_schema_max)
        throw new Error("配置 schema 下限不能高于上限。");
      return { kind: "register", body };
    }
    case "deploy":
      return {
        kind: "deploy",
        body: {
          to_version: action.version.version,
          replicas: Number(read("replicas")),
          initial_weight: 0,
        },
      };
    case "shift":
      return {
        kind: "shift",
        id: action.deployment.id,
        body: {
          weights: Object.fromEntries(
            Object.keys(action.deployment.target_weights).map((key) => [
              key,
              Number(read(`weight:${key}`)),
            ]),
          ),
        },
      };
    case "rollback":
      return { kind: "rollback", id: action.deployment.id };
    case "drain":
      return { kind: "drain", id: action.instance.id, body: { force: false } };
    case "force": {
      const reason = read("reason");
      if (!reason) throw new Error("请输入强制排水原因。");
      return { kind: "drain", id: action.instance.id, body: { force: true, reason } };
    }
    case "prune":
      return { kind: "prune", id: action.instance.id };
  }
}

function NumberField({
  name,
  label,
  value,
  min,
  max,
}: {
  name: string;
  label: string;
  value: number;
  min: number;
  max?: number;
}) {
  const id = useId();
  return (
    <label className="grid min-w-0 gap-1 break-all text-sm" htmlFor={id}>
      {label}
      <Input id={id} type="number" name={name} defaultValue={value} min={min} max={max} required />
    </label>
  );
}

export function CoreActionDialog({
  action,
  block,
  onClose,
  onRestoreFocus,
  onReceipt,
}: {
  action: Action;
  block?: string;
  onClose: () => void;
  onRestoreFocus: () => void;
  onReceipt: (message: string) => void;
}) {
  const client = useQueryClient();
  const [validation, setValidation] = useState("");
  const [review, setReview] = useState<Command | null>(null);
  const mutation = useMutation({
    mutationFn: coresApi.command,
    onSuccess: (receipt) => {
      onReceipt(receipt);
      onClose();
    },
    onSettled: () => {
      void client.invalidateQueries({ queryKey: queryKeys.cores });
    },
  });
  const [title, description] = copy[action.kind];
  const target =
    "instance" in action
      ? action.instance.container_name
      : "deployment" in action
        ? action.deployment.id
        : "version" in action
          ? action.version.version
          : "";
  const message = block || validation || (mutation.error ? coreError(mutation.error) : "");
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (block || mutation.isPending) return;
    setValidation("");
    if (review) {
      mutation.mutate(review);
      return;
    }
    let command: Command;
    try {
      command = buildCommand(action, new FormData(event.currentTarget));
    } catch (error) {
      setValidation(error instanceof Error ? error.message : "无法提交表单。");
      return;
    }
    if (action.kind === "force") setReview(command);
    else mutation.mutate(command);
  }

  return (
    <Dialog.Root
      open
      onOpenChange={(open) => {
        if (!open && !mutation.isPending) onClose();
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay className="dialog-overlay" />
        <Dialog.Content
          className="dialog-content"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            onRestoreFocus();
          }}
          onEscapeKeyDown={(event) => {
            if (mutation.isPending) event.preventDefault();
          }}
          onPointerDownOutside={(event) => {
            if (mutation.isPending) event.preventDefault();
          }}
        >
          <Dialog.Title>{title}</Dialog.Title>
          <Dialog.Description>{description}</Dialog.Description>
          {target && <p className="break-all font-mono text-xs">{target}</p>}
          <form onSubmit={submit} className="mt-4">
            <fieldset disabled={mutation.isPending} className={review ? "hidden" : "grid gap-3"}>
              {action.kind === "register" && (
                <>
                  <label className="grid gap-1 text-sm" htmlFor="core-version">
                    版本
                    <Input id="core-version" name="version" required />
                  </label>
                  <label className="grid gap-1 text-sm" htmlFor="core-image_ref">
                    镜像引用
                    <Input id="core-image_ref" name="image_ref" required />
                  </label>
                  <label className="grid gap-1 text-sm" htmlFor="core-image_digest">
                    镜像 digest
                    <Input id="core-image_digest" name="image_digest" placeholder="sha256:…" />
                  </label>
                  <div className="grid gap-3 sm:grid-cols-3">
                    <NumberField label="协议版本" name="protocol_version" value={1} min={1} />
                    <NumberField label="Schema 下限" name="config_schema_min" value={1} min={1} />
                    <NumberField label="Schema 上限" name="config_schema_max" value={1} min={1} />
                  </div>
                </>
              )}
              {action.kind === "deploy" && (
                <NumberField label="副本数" name="replicas" value={1} min={1} max={10} />
              )}
              {action.kind === "shift" &&
                Object.entries(action.deployment.target_weights).map(([key, value]) => (
                  <NumberField
                    key={key}
                    label={key}
                    name={`weight:${key}`}
                    value={value}
                    min={0}
                    max={10_000}
                  />
                ))}
              {action.kind === "force" && (
                <label className="grid gap-1 text-sm" htmlFor="core-reason">
                  强制排水原因
                  <Input id="core-reason" name="reason" required />
                </label>
              )}
            </fieldset>
            {review?.kind === "drain" && (
              <p className="break-words">强制排水原因：{review.body.reason}</p>
            )}
            {message && (
              <p role="alert" className="field-error">
                {message}
              </p>
            )}
            <div className="dialog-actions">
              {review && (
                <Button
                  variant="secondary"
                  disabled={mutation.isPending}
                  onClick={() => {
                    mutation.reset();
                    setReview(null);
                  }}
                >
                  修改原因
                </Button>
              )}
              <Dialog.Close asChild>
                <Button variant="secondary" disabled={mutation.isPending}>
                  取消
                </Button>
              </Dialog.Close>
              <Button type="submit" variant="danger" disabled={mutation.isPending || !!block}>
                {mutation.isPending
                  ? "等待服务端回执…"
                  : action.kind === "force" && !review
                    ? "核对强制排水"
                    : `确认${title}`}
              </Button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

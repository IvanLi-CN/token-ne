import { useState, useSyncExternalStore } from "react";
import { Button, StatusBadge } from "../../components/primitives";
import type { DemoRuntime } from "../runtime";
import { type SceneId, sceneIds, scenes } from "../scenes";
import {
  datasetVariants,
  demoQuery,
  mergeUrlState,
  networks,
  personas,
  type ShareState,
} from "../share-state";
import {
  defaultLayoutFor,
  effectiveLayout,
  type PanelLayout,
  panelLayouts,
  readPanelLayout,
  writePanelLayout,
} from "./layout";

const layoutCopy: Record<PanelLayout, string> = {
  rail: "固定侧栏",
  floating: "浮层",
  drawer: "抽屉",
  bubble: "收起",
};

function shareUrl(runtime: DemoRuntime): string {
  return new URL(
    mergeUrlState(window.location.href, { scene: runtime.state.scene, share: runtime.state.share }),
    window.location.origin,
  ).toString();
}

export function InspectorPanel({ runtime }: { runtime: DemoRuntime }) {
  useSyncExternalStore(
    (listener) => runtime.subscribe(listener),
    () => runtime.snapshot(),
    () => 0,
  );
  const [layout, setLayout] = useState<PanelLayout>(
    () => readPanelLayout() ?? defaultLayoutFor(window.innerWidth),
  );
  const [copied, setCopied] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const effective: PanelLayout = effectiveLayout(layout, window.innerWidth);
  const { state } = runtime;

  function choose(next: PanelLayout) {
    setLayout(next);
    writePanelLayout(next);
  }

  if (effective === "bubble") {
    return (
      <button
        type="button"
        className="demo-bubble"
        aria-label="打开 demo Inspector"
        onClick={() => choose(defaultLayoutFor(window.innerWidth))}
      >
        <span aria-hidden="true">t/n</span>
        <span className="demo-bubble-badge">simulated</span>
      </button>
    );
  }

  return (
    <aside
      className={`demo-inspector demo-inspector-${effective}`}
      aria-label="Demo Inspector"
      data-layout={effective}
    >
      <header className="demo-inspector-head">
        <div className="demo-inspector-title">
          <span className="demo-brand-mark" aria-hidden="true">
            t/n
          </span>
          <div>
            <h2>Demo Inspector</h2>
            <p className="muted">mock 运行时 · 无真实后端</p>
          </div>
        </div>
        <div className="demo-inspector-badges">
          <StatusBadge tone="warning">
            <span className="demo-simulated">simulated</span>
          </StatusBadge>
          <Button variant="secondary" onClick={() => choose("bubble")} aria-label="收起 Inspector">
            收起
          </Button>
        </div>
      </header>

      <fieldset className="demo-group">
        <legend>场景</legend>
        <div className="demo-grid">
          {sceneIds.map((id: SceneId) => (
            <button
              key={id}
              type="button"
              className={state.scene === id ? "demo-chip is-selected" : "demo-chip"}
              aria-pressed={state.scene === id}
              onClick={() => runtime.setScene(id)}
            >
              <strong>{scenes[id].label}</strong>
              <small>{scenes[id].summary}</small>
            </button>
          ))}
        </div>
      </fieldset>

      <fieldset className="demo-group">
        <legend>身份（persona）</legend>
        <div className="demo-row">
          {personas.map((persona) => (
            <button
              key={persona}
              type="button"
              className={state.share.persona === persona ? "demo-pill is-selected" : "demo-pill"}
              aria-pressed={state.share.persona === persona}
              onClick={() => runtime.setShare({ persona })}
            >
              {persona}
            </button>
          ))}
        </div>
        <p className="muted demo-hint">身份只影响界面文案与权限提示，不产生真实会话。</p>
      </fieldset>

      <fieldset className="demo-group">
        <legend>网络（network）</legend>
        <div className="demo-row">
          {networks.map((network) => (
            <button
              key={network}
              type="button"
              className={state.share.network === network ? "demo-pill is-selected" : "demo-pill"}
              aria-pressed={state.share.network === network}
              onClick={() => runtime.setShare({ network })}
            >
              {network === "instant" ? "即时" : "慢速 400ms"}
            </button>
          ))}
        </div>
      </fieldset>

      <fieldset className="demo-group">
        <legend>数据（data）</legend>
        <div className="demo-row">
          {datasetVariants.map((data) => (
            <button
              key={data}
              type="button"
              className={state.share.data === data ? "demo-pill is-selected" : "demo-pill"}
              aria-pressed={state.share.data === data}
              onClick={() => runtime.setShare({ data })}
            >
              {data === "full" ? "完整" : "稀疏"}
            </button>
          ))}
        </div>
        <Button variant="secondary" onClick={() => runtime.resetData()}>
          重置内存数据
        </Button>
      </fieldset>

      <fieldset className="demo-group">
        <legend>面板</legend>
        <div className="demo-row">
          {panelLayouts.map((option) => (
            <button
              key={option}
              type="button"
              className={layout === option ? "demo-pill is-selected" : "demo-pill"}
              aria-pressed={layout === option}
              onClick={() => choose(option)}
            >
              {layoutCopy[option]}
            </button>
          ))}
        </div>
        <p className="muted demo-hint">面板布局只保存在本机 localStorage，不写入分享链接。</p>
      </fieldset>

      <section className="demo-group" aria-labelledby="demo-recent">
        <h3 id="demo-recent">近期模拟变更</h3>
        {state.mutations.length === 0 ? (
          <p className="muted demo-hint">还没有模拟写操作。修改模型、上游或 Key 后会出现在这里。</p>
        ) : (
          <ol className="demo-mutations">
            {state.mutations.map((mutation) => (
              <li key={`${mutation.at}-${mutation.path}-${mutation.label}`}>
                <span className="demo-mutation-meta">
                  {new Date(mutation.at).toLocaleTimeString("zh-CN", { hour12: false })}
                  {" · "}
                  {mutation.method} {mutation.path}
                </span>
                <span>{mutation.label}</span>
              </li>
            ))}
          </ol>
        )}
      </section>

      <section className="demo-group" aria-labelledby="demo-share">
        <h3 id="demo-share">分享</h3>
        <p className="muted demo-hint break-all">
          {demoQuery.scene}={state.scene} · {demoQuery.persona}={state.share.persona} ·{" "}
          {demoQuery.network}={state.share.network} · {demoQuery.data}={state.share.data}
        </p>
        <div className="demo-row">
          <Button
            variant="secondary"
            onClick={() => {
              void navigator.clipboard?.writeText(shareUrl(runtime)).then(() => setCopied(true));
            }}
          >
            复制分享链接
          </Button>
          <Button variant="secondary" onClick={() => setAdvanced((value) => !value)}>
            {advanced ? "隐藏" : "查看"}原始状态
          </Button>
        </div>
        {copied && (
          <p role="status" className="muted demo-hint">
            已复制当前场景与分享状态。
          </p>
        )}
        {advanced && (
          <pre className="demo-raw">
            {JSON.stringify(
              {
                scene: state.scene,
                share: state.share satisfies ShareState,
                authenticated: state.authenticated,
                counts: {
                  models: state.models.length,
                  upstreams: state.upstreams.length,
                  keys: state.keys.length,
                  requests: state.requests.length,
                },
              },
              null,
              2,
            )}
          </pre>
        )}
      </section>
    </aside>
  );
}

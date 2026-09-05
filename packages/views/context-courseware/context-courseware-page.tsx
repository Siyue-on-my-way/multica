"use client";

// This page is a self-contained Chinese technical courseware artifact. Its
// wording is the documented SIY-121 sample rather than ordinary product UI
// copy; keeping it together makes the visual explanation auditable against the
// source document and avoids turning the course into four unrelated copies.
/* eslint-disable i18next/no-literal-string */

import type { LucideIcon } from "lucide-react";
import {
  ArrowDown,
  ArrowRight,
  BookOpen,
  Bot,
  Check,
  CheckCircle2,
  CircleAlert,
  ClipboardCheck,
  Code2,
  Database,
  Eye,
  FileText,
  GitBranch,
  History,
  Layers,
  MessageSquare,
  PanelTop,
  RefreshCw,
  Server,
  ShieldCheck,
  Sparkles,
  Terminal,
  Workflow,
  Zap,
} from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { PageHeader } from "../layout/page-header";

type Tone = "blue" | "violet" | "amber" | "emerald";

const toneStyles: Record<
  Tone,
  { icon: string; border: string; surface: string; label: string }
> = {
  blue: {
    icon: "bg-blue-500/12 text-blue-600 dark:text-blue-300",
    border: "border-blue-500/25",
    surface: "bg-blue-500/[0.04]",
    label: "text-blue-700 dark:text-blue-300",
  },
  violet: {
    icon: "bg-violet-500/12 text-violet-600 dark:text-violet-300",
    border: "border-violet-500/25",
    surface: "bg-violet-500/[0.04]",
    label: "text-violet-700 dark:text-violet-300",
  },
  amber: {
    icon: "bg-amber-500/14 text-amber-700 dark:text-amber-300",
    border: "border-amber-500/30",
    surface: "bg-amber-500/[0.05]",
    label: "text-amber-800 dark:text-amber-300",
  },
  emerald: {
    icon: "bg-emerald-500/12 text-emerald-600 dark:text-emerald-300",
    border: "border-emerald-500/25",
    surface: "bg-emerald-500/[0.04]",
    label: "text-emerald-700 dark:text-emerald-300",
  },
};

interface Layer {
  id: string;
  label: string;
  title: string;
  description: string;
  carrier: string;
  goal: string;
  tone: Tone;
  icon: LucideIcon;
}

const layers: Layer[] = [
  {
    id: "L1",
    label: "运行时 Brief",
    title: "稳定的运行时契约",
    description: "Agent 身份、安全边界、CLI、工作流、技能索引和 Provider 适配。",
    carrier: "AGENTS.md / CLAUDE.md",
    goal: "可发现、可缓存",
    tone: "blue",
    icon: PanelTop,
  },
  {
    id: "L2",
    label: "当前任务 Prompt",
    title: "这一次要做什么",
    description: "每个 run 重新生成模式、触发信息、回复目标和连续性声明。",
    carrier: "当前 user message",
    goal: "精确描述本轮",
    tone: "violet",
    icon: MessageSquare,
  },
  {
    id: "L3",
    label: "祖先 / 业务背景",
    title: "可迁移的背景快照",
    description: "从当前 Issue 的 parent 向根 Issue 读取标题和 description，并记录 refs。",
    carrier: "Task payload / sidecar",
    goal: "切换 Agent 仍可继续",
    tone: "amber",
    icon: GitBranch,
  },
  {
    id: "L4",
    label: "历史 / 运行状态",
    title: "事实源与恢复状态",
    description: "评论、metadata、handoff、Provider session、rollout 和 workdir 分开管理。",
    carrier: "Multica DB / Provider store",
    goal: "恢复、审计、诊断",
    tone: "emerald",
    icon: Database,
  },
];

interface FlowStep {
  label: string;
  description: string;
  icon: LucideIcon;
  tone: Tone;
}

const flowSteps: FlowStep[] = [
  { label: "事件", description: "assignment / comment / chat", icon: Zap, tone: "amber" },
  { label: "Claim", description: "enrich Task + ancestor", icon: ClipboardCheck, tone: "blue" },
  { label: "连续性 gates", description: "session / runtime / workdir", icon: ShieldCheck, tone: "emerald" },
  { label: "execenv", description: "Brief + sidecar", icon: Terminal, tone: "violet" },
  { label: "Prompt", description: "本轮任务边界", icon: MessageSquare, tone: "blue" },
  { label: "Provider", description: "resume 或 fresh", icon: Bot, tone: "emerald" },
  { label: "结果", description: "handoff + Issue state", icon: CheckCircle2, tone: "amber" },
];

const continuityRows = [
  {
    scenario: "同 Agent + 同 Issue + 同 runtime",
    session: "尝试 resume",
    workdir: "尝试 reuse",
    injected: "当前 Task Prompt、模式、触发信息",
    tone: "emerald" as Tone,
  },
  {
    scenario: "runtime 改变",
    session: "通常不能跨 runtime",
    workdir: "可能共享，但仍校验",
    injected: "同上；必要时从平台状态重建",
    tone: "amber" as Tone,
  },
  {
    scenario: "切换 Agent",
    session: "通常 fresh",
    workdir: "新环境或安全复用",
    injected: "Issue、祖先、handoff、Brief、读取协议",
    tone: "violet" as Tone,
  },
  {
    scenario: "resume 失败 / rollout 丢失",
    session: "fresh retry",
    workdir: "重新准备或挂载",
    injected: "Session Continuity Notice + 重新读取要求",
    tone: "blue" as Tone,
  },
];

const metrics = [
  { value: "1,564", label: "prompt bytes", detail: "assignment wrapper + checkpoint" },
  { value: "false", label: "resume_session", detail: "本样本从 fresh session 开始" },
  { value: "false", label: "reuse_workdir", detail: "切换 Agent 的安全边界" },
  { value: "8,192", label: "ancestor tokens", detail: "独立预算上限" },
  { value: "Codex", label: "Provider", detail: "app-server / AGENTS.md" },
];

const options = [
  {
    id: "A",
    title: "增强 handoff 压缩",
    summary: "把祖先链、关键评论、代码状态和待办压成一张工作地图。",
    benefit: "首轮信息密度高，切换体验直接改善。",
    cost: "摘要仍可能出错；需要 source ID 和 omitted list 才能追溯。",
    icon: Sparkles,
    tone: "violet" as Tone,
  },
  {
    id: "B",
    title: "Context manifest + 按需读取",
    summary: "首轮传 refs、digest、必须读取的 thread IDs 和优先级，正文由 Agent 按协议补齐。",
    benefit: "预算可控、可审计、可重放；遗漏可以对账。",
    cost: "首轮多几次 CLI 读取，需要用指标验证召回率。",
    icon: ClipboardCheck,
    tone: "emerald" as Tone,
    recommended: true,
  },
  {
    id: "C",
    title: "完整 fresh snapshot",
    summary: "祖先、评论全文、metadata、最近结果和代码状态一次打包。",
    benefit: "初始信息最多，适合短链路和人工复核。",
    cost: "Prompt 变大、易过时、破坏缓存，也不能保证全量永远最新。",
    icon: Layers,
    tone: "amber" as Tone,
  },
];

function SectionHeading({
  eyebrow,
  title,
  description,
  id,
}: {
  eyebrow: string;
  title: string;
  description: string;
  id: string;
}) {
  return (
    <div id={id} className="scroll-mt-6">
      <p className="text-caption font-semibold uppercase tracking-[0.14em] text-brand">
        {eyebrow}
      </p>
      <h2 className="mt-1 text-title-sm font-semibold tracking-tight text-foreground sm:text-title">
        {title}
      </h2>
      <p className="mt-2 max-w-3xl text-body leading-6 text-muted-foreground">
        {description}
      </p>
    </div>
  );
}

function LayerCard({ layer }: { layer: Layer }) {
  const styles = toneStyles[layer.tone];
  const Icon = layer.icon;
  return (
    <article className={cn("rounded-xl border p-4", styles.border, styles.surface)}>
      <div className="flex items-start justify-between gap-3">
        <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", styles.icon)}>
          <Icon className="size-4" aria-hidden="true" />
        </div>
        <span className={cn("font-mono text-caption font-semibold", styles.label)}>{layer.id}</span>
      </div>
      <p className="mt-4 text-caption font-medium text-muted-foreground">{layer.label}</p>
      <h3 className="mt-1 text-body font-semibold text-foreground">{layer.title}</h3>
      <p className="mt-2 min-h-12 text-caption leading-5 text-muted-foreground">{layer.description}</p>
      <div className="mt-4 grid gap-2 border-t border-current/10 pt-3 text-caption">
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground">载体</span>
          <code className="truncate font-mono text-foreground">{layer.carrier}</code>
        </div>
        <div className="flex items-center justify-between gap-3">
          <span className="text-muted-foreground">目标</span>
          <span className="font-medium text-foreground">{layer.goal}</span>
        </div>
      </div>
    </article>
  );
}

function FlowNode({ step }: { step: FlowStep }) {
  const styles = toneStyles[step.tone];
  const Icon = step.icon;
  return (
    <div className="flex min-w-0 flex-1 items-center gap-2 rounded-xl border border-surface-border bg-surface-raised/70 p-3 shadow-[var(--surface-shadow)]">
      <div className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg", styles.icon)}>
        <Icon className="size-4" aria-hidden="true" />
      </div>
      <div className="min-w-0">
        <p className="truncate text-caption font-semibold text-foreground">{step.label}</p>
        <p className="mt-0.5 truncate text-micro text-muted-foreground">{step.description}</p>
      </div>
    </div>
  );
}

function FlowConnector() {
  return (
    <div className="flex shrink-0 items-center justify-center text-faint-foreground">
      <ArrowRight className="hidden size-4 md:block" aria-hidden="true" />
      <ArrowDown className="size-4 md:hidden" aria-hidden="true" />
    </div>
  );
}

function MetricCard({
  value,
  label,
  detail,
}: {
  value: string;
  label: string;
  detail: string;
}) {
  return (
    <div className="rounded-xl border border-surface-border bg-surface-raised p-4">
      <p className="font-mono text-title-sm font-semibold tracking-tight text-foreground">{value}</p>
      <p className="mt-1 text-caption font-medium text-brand">{label}</p>
      <p className="mt-1 text-micro leading-4 text-muted-foreground">{detail}</p>
    </div>
  );
}

function ComparisonPanel({
  icon: Icon,
  title,
  description,
  items,
  tone,
}: {
  icon: LucideIcon;
  title: string;
  description: string;
  items: string[];
  tone: Tone;
}) {
  const styles = toneStyles[tone];
  return (
    <article className={cn("rounded-xl border p-5", styles.border, styles.surface)}>
      <div className="flex items-start gap-3">
        <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", styles.icon)}>
          <Icon className="size-4" aria-hidden="true" />
        </div>
        <div>
          <h3 className="text-body font-semibold text-foreground">{title}</h3>
          <p className="mt-1 text-caption leading-5 text-muted-foreground">{description}</p>
        </div>
      </div>
      <ul className="mt-4 grid gap-2">
        {items.map((item) => (
          <li key={item} className="flex items-start gap-2 text-caption leading-5 text-foreground">
            <Check className={cn("mt-0.5 size-3.5 shrink-0", styles.label)} aria-hidden="true" />
            <span>{item}</span>
          </li>
        ))}
      </ul>
    </article>
  );
}

function OptionCard({ option }: { option: (typeof options)[number] }) {
  const styles = toneStyles[option.tone];
  const Icon = option.icon;
  return (
    <article
      className={cn(
        "relative rounded-xl border p-5",
        option.recommended ? "border-emerald-500/45 bg-emerald-500/[0.06]" : "border-surface-border bg-surface-raised",
      )}
    >
      {option.recommended ? (
        <span className="absolute right-4 top-4 inline-flex items-center gap-1 rounded-full bg-emerald-500/12 px-2 py-1 text-micro font-semibold text-emerald-700 dark:text-emerald-300">
          <CheckCircle2 className="size-3" aria-hidden="true" />
          推荐先评估
        </span>
      ) : null}
      <div className="flex items-start gap-3 pr-20">
        <div className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", styles.icon)}>
          <Icon className="size-4" aria-hidden="true" />
        </div>
        <div>
          <p className={cn("font-mono text-caption font-semibold", styles.label)}>方案 {option.id}</p>
          <h3 className="mt-1 text-body font-semibold text-foreground">{option.title}</h3>
        </div>
      </div>
      <p className="mt-4 text-caption leading-5 text-foreground">{option.summary}</p>
      <div className="mt-4 grid gap-3 border-t border-surface-border pt-3 text-caption leading-5">
        <p>
          <span className="font-medium text-emerald-700 dark:text-emerald-300">优点：</span>
          <span className="text-muted-foreground">{option.benefit}</span>
        </p>
        <p>
          <span className="font-medium text-amber-700 dark:text-amber-300">代价：</span>
          <span className="text-muted-foreground">{option.cost}</span>
        </p>
      </div>
    </article>
  );
}

export function ContextCoursewarePage() {
  return (
    <div className="flex h-full min-h-0 flex-col bg-page-canvas">
      <PageHeader className="shrink-0 justify-between gap-3 px-5">
        <div className="flex min-w-0 items-center gap-2">
          <BookOpen className="size-4 shrink-0 text-brand" aria-hidden="true" />
          <h1 className="truncate text-body font-medium">上下文课件</h1>
          <span className="hidden rounded-full bg-brand/10 px-2 py-0.5 text-micro font-medium text-brand sm:inline-flex">
            SIY-121
          </span>
        </div>
        <span className="hidden text-caption text-muted-foreground md:inline">一次 run 的生命周期</span>
      </PageHeader>

      <main className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto grid w-full max-w-[1440px] gap-8 px-4 py-6 sm:px-6 lg:grid-cols-[168px_minmax(0,1fr)] lg:px-8 lg:py-8">
          <aside className="hidden lg:block">
            <nav className="sticky top-6" aria-label="课件目录">
              <p className="mb-3 text-micro font-semibold uppercase tracking-[0.16em] text-muted-foreground">目录</p>
              <div className="grid gap-1 border-l border-surface-border pl-3">
                {[
                  ["layers", "四层上下文"],
                  ["lifecycle", "一次 run 的链路"],
                  ["continuity", "连续性 gates"],
                  ["handoff", "切换时传什么"],
                  ["observed", "真实 run"],
                  ["options", "可选改造"],
                ].map(([href, label]) => (
                  <a
                    key={href}
                    href={`#${href}`}
                    className="rounded-md px-2 py-1.5 text-caption text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
                  >
                    {label}
                  </a>
                ))}
              </div>
            </nav>
          </aside>

          <div className="min-w-0 space-y-12">
            <section className="overflow-hidden rounded-2xl border border-brand/20 bg-[radial-gradient(circle_at_top_right,oklch(0.7_0.16_255/0.18),transparent_42%),linear-gradient(135deg,oklch(0.55_0.16_255/0.11),transparent_62%)] p-5 sm:p-8">
              <div className="grid gap-8 lg:grid-cols-[minmax(0,1.25fr)_minmax(260px,0.75fr)] lg:items-end">
                <div>
                  <div className="inline-flex items-center gap-2 rounded-full border border-brand/25 bg-background/50 px-2.5 py-1 text-micro font-semibold text-brand">
                    <Sparkles className="size-3" aria-hidden="true" />
                    真实 run · 脱敏观测
                  </div>
                  <h2 className="mt-5 max-w-3xl text-3xl font-semibold tracking-[-0.03em] text-foreground sm:text-4xl">
                    一次 run 的
                    <span className="text-brand">上下文生命周期</span>
                  </h2>
                  <p className="mt-4 max-w-2xl text-body leading-7 text-muted-foreground sm:text-title-sm sm:leading-7">
                    Multica 不把上下文当成一个无限增长的字符串，而是把稳定契约、当前任务、可迁移背景和运行状态拆开管理。
                  </p>
                  <div className="mt-6 flex items-start gap-3 rounded-xl border border-brand/20 bg-background/55 p-4">
                    <ShieldCheck className="mt-0.5 size-5 shrink-0 text-brand" aria-hidden="true" />
                    <p className="text-body font-medium leading-6 text-foreground">
                      核心结论：Provider session 负责低延迟的原生连续性；Issue、handoff、Brief 和读取协议负责可迁移、可审计的业务连续性。
                    </p>
                  </div>
                </div>
                <div className="rounded-xl border border-surface-border bg-surface/80 p-4 backdrop-blur-sm">
                  <div className="flex items-center justify-between gap-3 border-b border-surface-border pb-3">
                    <span className="text-caption font-medium text-muted-foreground">样本运行</span>
                    <span className="rounded-full bg-amber-500/12 px-2 py-0.5 text-micro font-semibold text-amber-800 dark:text-amber-300">Ownership</span>
                  </div>
                  <dl className="mt-3 grid gap-3 text-caption">
                    <div className="flex items-center justify-between gap-3"><dt className="text-muted-foreground">Issue</dt><dd className="font-mono font-semibold text-foreground">SIY-121</dd></div>
                    <div className="flex items-center justify-between gap-3"><dt className="text-muted-foreground">Task</dt><dd className="max-w-[15rem] truncate font-mono text-foreground">0f3ea789…</dd></div>
                    <div className="flex items-center justify-between gap-3"><dt className="text-muted-foreground">Provider</dt><dd className="font-medium text-foreground">Codex app-server</dd></div>
                    <div className="flex items-center justify-between gap-3"><dt className="text-muted-foreground">入口</dt><dd className="font-medium text-foreground">assignment / direct</dd></div>
                  </dl>
                </div>
              </div>
            </section>

            <section>
              <SectionHeading
                id="layers"
                eyebrow="01 · Context model"
                title="四层上下文：稳定的和变化的分开"
                description="L1 尽量稳定以利于缓存；L2 每轮重建以保证边界精确；L3、L4 则提供 Agent 切换和故障恢复所需的可验证背景。"
              />
              <div className="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
                {layers.map((layer) => <LayerCard key={layer.id} layer={layer} />)}
              </div>
            </section>

            <section>
              <SectionHeading
                id="lifecycle"
                eyebrow="02 · Run lifecycle"
                title="从事件到结果，所有入口收敛到同一条链路"
                description="assignment、comment、chat 和 autopilot 只是触发方式不同；claim 之后都会经过相同的上下文组装和 Provider 调用边界。"
              />
              <div className="mt-6 rounded-2xl border border-surface-border bg-surface/70 p-4 sm:p-5">
                <div className="flex flex-col gap-2 md:flex-row md:items-stretch md:gap-2">
                  {flowSteps.map((step, index) => (
                    <div key={step.label} className="contents">
                      <FlowNode step={step} />
                      {index < flowSteps.length - 1 ? <FlowConnector /> : null}
                    </div>
                  ))}
                </div>
                <div className="mt-5 flex flex-wrap gap-x-5 gap-y-2 border-t border-surface-border pt-4 text-micro text-muted-foreground">
                  <span className="inline-flex items-center gap-1.5"><CheckCircle2 className="size-3 text-emerald-500" aria-hidden="true" />通过 gates 才 resume</span>
                  <span className="inline-flex items-center gap-1.5"><RefreshCw className="size-3 text-brand" aria-hidden="true" />失败会 fresh retry</span>
                  <span className="inline-flex items-center gap-1.5"><Eye className="size-3 text-amber-500" aria-hidden="true" />结果可审计</span>
                </div>
              </div>
            </section>

            <section>
              <SectionHeading
                id="continuity"
                eyebrow="03 · Continuity"
                title="会话连续性不是一个布尔值"
                description="session、runtime、workdir 和 rollout 各自都有约束。workdir 能复用，不等于 Provider transcript 一定存在；resume 失败也不应静默假装记得。"
              />
              <div className="mt-6 overflow-hidden rounded-2xl border border-surface-border bg-surface/70">
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[760px] border-collapse text-left text-caption">
                    <thead className="bg-surface-hover/70 text-muted-foreground">
                      <tr>
                        <th className="px-4 py-3 font-medium">情形</th>
                        <th className="px-4 py-3 font-medium">Provider session</th>
                        <th className="px-4 py-3 font-medium">Workdir</th>
                        <th className="px-4 py-3 font-medium">当前 run 仍注入</th>
                      </tr>
                    </thead>
                    <tbody>
                      {continuityRows.map((row) => {
                        const styles = toneStyles[row.tone];
                        return (
                          <tr key={row.scenario} className="border-t border-surface-border align-top">
                            <td className="px-4 py-4 font-medium text-foreground"><span className={cn("mr-2 inline-block size-1.5 rounded-full align-middle", styles.icon.split(" ")[0])} />{row.scenario}</td>
                            <td className="px-4 py-4 text-muted-foreground">{row.session}</td>
                            <td className="px-4 py-4 text-muted-foreground">{row.workdir}</td>
                            <td className="px-4 py-4 text-foreground">{row.injected}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-2">
                <div className="flex items-start gap-3 rounded-xl border border-emerald-500/25 bg-emerald-500/[0.04] p-4">
                  <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-300" aria-hidden="true" />
                  <p className="text-caption leading-5 text-muted-foreground"><strong className="font-semibold text-foreground">Warm path：</strong>同 Agent、同 Issue、同 runtime 且 rollout 存在时，优先享受 Provider 原生历史的低成本连续性。</p>
                </div>
                <div className="flex items-start gap-3 rounded-xl border border-amber-500/30 bg-amber-500/[0.05] p-4">
                  <CircleAlert className="mt-0.5 size-4 shrink-0 text-amber-700 dark:text-amber-300" aria-hidden="true" />
                  <p className="text-caption leading-5 text-muted-foreground"><strong className="font-semibold text-foreground">Cold path：</strong>旧 transcript 不可恢复时，明确发出 Session Continuity Notice，再按 CLI 读取协议重建事实。</p>
                </div>
              </div>
            </section>

            <section>
              <SectionHeading
                id="handoff"
                eyebrow="04 · Agent switch"
                title="切换 Agent 时，压缩器看到什么？新 Agent 收到什么？"
                description="切换不是把旧 Provider 窗口原样复制给新 Agent。迁移的是有来源、有边界、可以重新读取的业务表示。"
              />
              <div className="mt-6 grid gap-3 lg:grid-cols-2">
                <ComparisonPanel
                  icon={History}
                  title="压缩器 / 上一个 Agent 留下什么"
                  description="handoff_summary 是主动提炼的 checkpoint，适合说明如何接续，但不是全部事实。"
                  tone="violet"
                  items={[
                    "current_progress、next_steps、unresolved_issues",
                    "working_branch、agent_status 和最近结果",
                    "祖先 refs、当前 Issue 版本和必须关注的线程",
                    "明确哪些信息被省略，避免把摘要当成 transcript",
                  ]}
                />
                <ComparisonPanel
                  icon={Server}
                  title="新 Agent 的首轮输入"
                  description="新 Agent 拿到自己的 Brief，再结合 Task payload 和读取协议补齐上下文。"
                  tone="blue"
                  items={[
                    "当前 Issue title / description 和祖先标题描述快照",
                    "当前模式、触发 comment、回复 parent 和连续性声明",
                    "新 Agent 的身份、skills、project context 和 task-scoped token",
                    "按 roots summary → thread tail → per-id 校验读取评论和 metadata",
                  ]}
                />
              </div>
              <div className="mt-4 rounded-xl border border-brand/25 bg-brand/[0.05] p-4">
                <div className="flex items-start gap-3">
                  <Workflow className="mt-0.5 size-4 shrink-0 text-brand" aria-hidden="true" />
                  <p className="text-caption leading-5 text-foreground"><strong className="font-semibold">阅读优先级：</strong>全局安全与身份 → 当前 run 的模式和 comment → 当前 Issue → ancestor 背景 → 旧 transcript / workdir 中的不确定状态。发生冲突时回到 Issue、comment 和 metadata 查证。</p>
                </div>
              </div>
            </section>

            <section>
              <SectionHeading
                id="observed"
                eyebrow="05 · Observed run"
                title="真实 run：这次实际经过了哪些边界？"
                description="以下数据来自一次真实的 assignment / Ownership run，保留结构和标记，已移除 token、邮箱、绝对路径等敏感信息。"
              />
              <div className="mt-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
                {metrics.map((metric) => <MetricCard key={metric.label} {...metric} />)}
              </div>
              <div className="mt-4 grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(280px,0.8fr)]">
                <div className="rounded-xl border border-surface-border bg-surface-raised p-5">
                  <div className="flex items-center gap-2"><FileText className="size-4 text-brand" aria-hidden="true" /><h3 className="text-body font-semibold">本次 capture 的关键事实</h3></div>
                  <ul className="mt-4 grid gap-3">
                    {[
                      "task kind 是 direct / issue assignment，本次没有触发 comment，因此 Prompt 中没有 [NEW COMMENT]。",
                      "Codex 使用 AGENTS.md 发现 Brief；sidecar 包含 issue_context.md、daemon_task_context.json 和 resources.json。",
                      "daemon 从 fresh session / workdir 开始，上一 run 的 checkpoint 通过 handoff / issue_context 进入。",
                      "ancestor brief 以 SIY-59 为 parent，独立预算为 8,192 tokens，并保留 ancestor refs。",
                    ].map((fact) => <li key={fact} className="flex items-start gap-2 text-caption leading-5 text-muted-foreground"><Check className="mt-0.5 size-3.5 shrink-0 text-emerald-500" aria-hidden="true" />{fact}</li>)}
                  </ul>
                </div>
                <div className="rounded-xl border border-surface-border bg-surface-raised p-5">
                  <div className="flex items-center gap-2"><Code2 className="size-4 text-brand" aria-hidden="true" /><h3 className="text-body font-semibold">最短源码路线</h3></div>
                  <div className="mt-4 grid gap-2 font-mono text-micro leading-5 text-muted-foreground">
                    <code>daemon/types.go → Task wire shape</code>
                    <code>handler/daemon.go → claim + enrich</code>
                    <code>service/ancestor_brief.go → budget</code>
                    <code>daemon/prompt.go → per-turn prompt</code>
                    <code>daemon/execenv → Brief + sidecar</code>
                    <code>daemon.go → resume / fresh retry</code>
                  </div>
                </div>
              </div>
            </section>

            <section>
              <SectionHeading
                id="options"
                eyebrow="06 · Next design"
                title="压缩上下文的三种改造方案"
                description="当前实现已经有 ancestor budget、handoff 和带预算的读取协议。下一步重点是让迁移覆盖率可观测，再决定是否增加首轮 payload。"
              />
              <div className="mt-6 grid gap-3 lg:grid-cols-3">
                {options.map((option) => <OptionCard key={option.id} option={option} />)}
              </div>
              <div className="mt-4 rounded-xl border border-emerald-500/30 bg-emerald-500/[0.05] p-4">
                <div className="flex items-start gap-3">
                  <ClipboardCheck className="mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-300" aria-hidden="true" />
                  <p className="text-caption leading-5 text-foreground"><strong className="font-semibold">建议的验证顺序：</strong>保留同 Agent 的 warm resume；先把方案 B 做成 context manifest，记录 ancestor refs、comment IDs、handoff 版本和 truncated IDs，再用覆盖率、首轮 token、resume 成功率和切换后返工率做 A/B 对照。</p>
                </div>
              </div>
            </section>

            <footer className="border-t border-surface-border pt-6 pb-2 text-micro leading-5 text-muted-foreground">
              <p className="flex items-center gap-2"><BookOpen className="size-3.5" aria-hidden="true" />课件来源：<code className="font-mono">docs/context-management-courseware.md</code></p>
              <p className="mt-2 flex items-center gap-2"><Database className="size-3.5" aria-hidden="true" />真实数据 dump：<code className="font-mono">docs/context-run-dump/</code></p>
              <p className="mt-3">页面展示的是当前方案与真实样本，不把“切换时压缩”尚未实现的能力伪装成已存在的功能。</p>
            </footer>
          </div>
        </div>
      </main>
    </div>
  );
}

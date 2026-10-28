"use client";

/* CCTV poster hosts vary by result, so native images are used intentionally. */
/* eslint-disable @next/next/no-img-element */

import {
  BellRing,
  Check,
  CheckCircle2,
  ChevronRight,
  CirclePause,
  Clock3,
  Download,
  FolderOpen,
  FolderTree,
  Gauge,
  HardDriveDownload,
  LoaderCircle,
  MoreHorizontal,
  Pause,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  Sparkles,
  Trash2,
  Tv2,
  WifiOff,
  X,
} from "lucide-react";
import { FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

const API_BASE = process.env.NEXT_PUBLIC_API_URL
  ?? (process.env.NODE_ENV === "production" ? "" : "http://127.0.0.1:8080");

type Tab = "search" | "subscriptions" | "downloads" | "settings";
type SearchResult = {
  title: string;
  sourceUrl: string;
  poster: string;
  description: string;
  year?: string;
  category?: string;
  region?: string;
  episodeCount?: number;
  isSingle?: boolean;
};
type Episode = {
  number: number;
  title: string;
  pageUrl: string;
  image?: string;
  publishedAt?: string;
};
type SeriesPreview = SearchResult & { episodes: Episode[] };
type Subscription = SearchResult & {
  id: string;
  storageCategoryId: string;
  status: "active" | "paused";
  autoDownload: boolean;
  episodeCount: number;
  downloadedCount: number;
  lastCheckedAt?: string;
  lastError?: string;
};
type StorageCategory = {
  id: string;
  name: string;
  parentId?: string;
  downloadPath: string;
  createdAt: string;
};
type DownloadTask = {
  id: string;
  title: string;
  seriesTitle: string;
  poster: string;
  status: "queued" | "downloading" | "paused" | "completed" | "failed" | "cancelled";
  progress: number;
  speed?: string;
  filePath?: string;
  videoQuality?: string;
  pageUrl?: string;
  error?: string;
  createdAt: string;
  completedAt?: string;
};
type AppSettings = {
  pollIntervalMinutes: number;
  concurrentDownloads: number;
  segmentConcurrency: number;
  verifyVideoAfterDownload: boolean;
  downloadsPaused: boolean;
};
type Stats = { subscriptions: number; queued: number; downloading: number; paused: number; completed: number; failed: number };
type AppState = { settings: AppSettings; categories: StorageCategory[]; subscriptions: Subscription[]; downloads: DownloadTask[]; stats: Stats };

const initialSettings: AppSettings = { pollIntervalMinutes: 30, concurrentDownloads: 2, segmentConcurrency: 4, verifyVideoAfterDownload: false, downloadsPaused: false };
const initialStats: Stats = { subscriptions: 0, queued: 0, downloading: 0, paused: 0, completed: 0, failed: 0 };

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
  });
  const payload = response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok) throw new Error(payload?.error ?? `请求失败（${response.status}）`);
  return payload as T;
}

function timeAgo(value?: string) {
  if (!value) return "尚未检查";
  const diff = Date.now() - new Date(value).getTime();
  if (diff < 60_000) return "刚刚";
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return new Intl.DateTimeFormat("zh-CN", { month: "short", day: "numeric" }).format(new Date(value));
}

function statusText(status: DownloadTask["status"]) {
  return { queued: "等待中", downloading: "下载中", paused: "已暂停", completed: "已完成", failed: "失败", cancelled: "已取消" }[status];
}

function flattenCategories(categories: StorageCategory[]) {
  const children = new Map<string, StorageCategory[]>();
  for (const category of categories) {
    const key = category.parentId || "";
    children.set(key, [...(children.get(key) ?? []), category]);
  }
  const result: Array<StorageCategory & { depth: number; label: string }> = [];
  const visited = new Set<string>();
  const visit = (category: StorageCategory, depth: number, trail: string[]) => {
    if (visited.has(category.id)) return;
    visited.add(category.id);
    const nextTrail = [...trail, category.name];
    result.push({ ...category, depth, label: nextTrail.join(" / ") });
    for (const child of children.get(category.id) ?? []) visit(child, depth + 1, nextTrail);
  };
  for (const root of children.get("") ?? []) visit(root, 0, []);
  for (const category of categories) if (!visited.has(category.id)) visit(category, 0, []);
  return result;
}

export default function Home() {
  const [tab, setTab] = useState<Tab>("search");
  const [state, setState] = useState<AppState>({ settings: initialSettings, categories: [], subscriptions: [], downloads: [], stats: initialStats });
  const [connected, setConnected] = useState<boolean | null>(null);
  const [ffmpeg, setFfmpeg] = useState(false);
  const [query, setQuery] = useState("汪汪队立大功");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);
  const [preview, setPreview] = useState<SeriesPreview | null>(null);
  const [selectedEpisodeURLs, setSelectedEpisodeURLs] = useState<string[]>([]);
  const [resolvingURL, setResolvingURL] = useState<string | null>(null);
  const [queueBusy, setQueueBusy] = useState(false);
  const [adding, setAdding] = useState<SearchResult | null>(null);
  const [addBusy, setAddBusy] = useState(false);
  const [downloadExisting, setDownloadExisting] = useState(false);
  const [selectedCategoryID, setSelectedCategoryID] = useState("");
  const [taskFilter, setTaskFilter] = useState("all");
  const [settingsDraft, setSettingsDraft] = useState<AppSettings>(initialSettings);
  const [savingSettings, setSavingSettings] = useState(false);
  const [categoryDraft, setCategoryDraft] = useState({ name: "", parentId: "", downloadPath: "/downloads" });
  const [editingCategoryID, setEditingCategoryID] = useState<string | null>(null);
  const [savingCategory, setSavingCategory] = useState(false);
  const [bulkBusy, setBulkBusy] = useState(false);
  const [toast, setToast] = useState<{ text: string; type: "ok" | "error" } | null>(null);
  const [menuOpen, setMenuOpen] = useState<string | null>(null);
  const [playingTask, setPlayingTask] = useState<DownloadTask | null>(null);
  const [openingDirectoryID, setOpeningDirectoryID] = useState<string | null>(null);
  const settingsInitialized = useRef(false);

  const notify = useCallback((text: string, type: "ok" | "error" = "ok") => {
    setToast({ text, type });
    window.setTimeout(() => setToast(null), 3200);
  }, []);

  const loadState = useCallback(async (quiet = false) => {
    try {
      const [next, health] = await Promise.all([
        api<AppState>("/api/state"),
        api<{ ok: boolean; ffmpeg: boolean }>("/api/health"),
      ]);
	  next.subscriptions = next.subscriptions ?? [];
	  next.downloads = next.downloads ?? [];
	  next.categories = next.categories ?? [];
	  setState(next);
      if (!settingsInitialized.current) {
        setSettingsDraft(next.settings);
        settingsInitialized.current = true;
      }
      setSelectedCategoryID((current) => next.categories.some((category) => category.id === current) ? current : (next.categories[0]?.id ?? ""));
      setFfmpeg(health.ffmpeg);
      setConnected(true);
    } catch (error) {
      setConnected(false);
      if (!quiet) notify(error instanceof Error ? error.message : "无法连接后台服务", "error");
    }
  }, [notify]);

  useEffect(() => {
	const initial = window.setTimeout(() => loadState(true), 0);
    const timer = window.setInterval(() => loadState(true), 2500);
	return () => { window.clearTimeout(initial); window.clearInterval(timer); };
  }, [loadState]);

  function showEpisodePicker(series: SeriesPreview) {
    setPreview(series);
    const existing = new Set(state.downloads.filter((task) => task.status !== "failed" && task.status !== "cancelled").map((task) => task.pageUrl));
    const available = series.episodes.filter((episode) => !existing.has(episode.pageUrl));
    setSelectedEpisodeURLs(series.isSingle ? available.slice(0, 1).map((episode) => episode.pageUrl) : []);
    setSelectedCategoryID((current) => state.categories.some((category) => category.id === current) ? current : (state.categories[0]?.id ?? ""));
  }

  async function openEpisodePicker(sourceURL: string) {
    setResolvingURL(sourceURL);
    try {
      const series = await api<SeriesPreview>(`/api/resolve?url=${encodeURIComponent(sourceURL)}`);
      showEpisodePicker(series);
    } catch (error) {
      notify(error instanceof Error ? error.message : "无法读取选集", "error");
    } finally {
      setResolvingURL(null);
    }
  }

  async function runSearch(event?: FormEvent) {
    event?.preventDefault();
    if (!query.trim()) return;
    const value = query.trim();
    setSearching(true);
    setSearched(true);
    setResults([]);
    try {
      if (/^https?:\/\//i.test(value)) {
        const series = await api<SeriesPreview>(`/api/resolve?url=${encodeURIComponent(value)}`);
        setResults([series]);
        showEpisodePicker(series);
      } else {
        const data = await api<{ results: SearchResult[] }>(`/api/search?q=${encodeURIComponent(value)}`);
        setResults(data.results);
      }
      setConnected(true);
    } catch (error) {
      notify(error instanceof Error ? error.message : "解析或搜索失败", "error");
    } finally {
      setSearching(false);
    }
  }

  function toggleEpisode(pageURL: string) {
    setSelectedEpisodeURLs((current) => current.includes(pageURL) ? current.filter((url) => url !== pageURL) : current.length < 200 ? [...current, pageURL] : current);
  }

  async function queueSelectedEpisodes() {
    if (!preview || selectedEpisodeURLs.length === 0 || !selectedCategoryID) return;
    setQueueBusy(true);
    try {
      const response = await api<{ added: number; skipped: number }>("/api/downloads", {
        method: "POST",
        body: JSON.stringify({
          sourceUrl: preview.sourceUrl,
          episodeUrls: selectedEpisodeURLs,
          storageCategoryId: selectedCategoryID,
        }),
      });
      if (response.added > 0) {
        notify(`已加入 ${response.added} 集下载队列${response.skipped ? `，跳过 ${response.skipped} 集重复任务` : ""}`);
        setPreview(null);
        setTab("downloads");
        await loadState(true);
      } else {
        notify("所选集数已在下载队列或已完成，无需重复添加");
        await loadState(true);
      }
    } catch (error) {
      notify(error instanceof Error ? error.message : "加入下载任务失败", "error");
    } finally {
      setQueueBusy(false);
    }
  }

  async function confirmAdd() {
    if (!adding) return;
    setAddBusy(true);
    try {
      await api("/api/subscriptions", { method: "POST", body: JSON.stringify({ sourceUrl: adding.sourceUrl, storageCategoryId: selectedCategoryID, downloadExisting }) });
      notify(downloadExisting ? "已订阅，已有剧集已加入下载队列" : "订阅成功，新剧集会自动下载");
      setAdding(null);
      await loadState(true);
    } catch (error) {
      notify(error instanceof Error ? error.message : "订阅失败", "error");
    } finally {
      setAddBusy(false);
    }
  }

  async function updateSubscription(sub: Subscription, patch: { status?: string; autoDownload?: boolean; storageCategoryId?: string }) {
    try {
      await api(`/api/subscriptions/${sub.id}`, { method: "PATCH", body: JSON.stringify(patch) });
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "更新失败", "error"); }
  }

  async function checkSubscription(sub: Subscription) {
    try {
      await api(`/api/subscriptions/${sub.id}/check`, { method: "POST" });
      notify(`正在检查《${sub.title.replace(/[《》]/g, "")}》`);
      window.setTimeout(() => loadState(true), 1800);
    } catch (error) { notify(error instanceof Error ? error.message : "检查失败", "error"); }
  }

  async function removeSubscription(sub: Subscription) {
    if (!window.confirm(`确定删除“${sub.title}”的订阅吗？已下载的视频文件不会删除。`)) return;
    try {
      await api(`/api/subscriptions/${sub.id}`, { method: "DELETE" });
      notify("订阅已删除");
      setMenuOpen(null);
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "删除失败", "error"); }
  }

  async function taskAction(task: DownloadTask, action: "retry" | "cancel") {
    if (task.status === "completed" && action === "retry" && !window.confirm("重新下载该集吗？原视频会在新视频验证成功后才替换。")) return;
    try {
      await api(`/api/downloads/${task.id}/${action}`, { method: "POST" });
      notify(action === "retry" ? "任务已重新加入队列" : "任务已取消");
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "操作失败", "error"); }
  }

  async function toggleAllDownloads() {
    setBulkBusy(true);
    const resume = state.settings.downloadsPaused;
    try {
      await api(`/api/downloads/${resume ? "resume-all" : "pause-all"}`, { method: "POST" });
      notify(resume ? "全部下载已恢复" : "全部下载已暂停");
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "操作失败", "error"); }
    finally { setBulkBusy(false); }
  }

  async function openDownloadDirectory(task: DownloadTask) {
    setOpeningDirectoryID(task.id);
    try {
      await api(`/api/downloads/${encodeURIComponent(task.id)}/open-directory`, { method: "POST", body: "{}" });
      notify("已请求在运行 CCTV 程序的电脑上打开视频所在目录");
    } catch (error) {
      notify(error instanceof Error ? error.message : "打开目录失败", "error");
    } finally {
      setOpeningDirectoryID(null);
    }
  }

  async function saveSettings(event: FormEvent) {
    event.preventDefault();
    setSavingSettings(true);
    try {
      const saved = await api<AppSettings>("/api/settings", { method: "PUT", body: JSON.stringify(settingsDraft) });
      setSettingsDraft(saved);
      notify("设置已保存并立即生效");
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "保存失败", "error"); }
    finally { setSavingSettings(false); }
  }

  function beginCategoryEdit(category: StorageCategory) {
    setEditingCategoryID(category.id);
    setCategoryDraft({ name: category.name, parentId: category.parentId ?? "", downloadPath: category.downloadPath });
  }

  function resetCategoryEditor() {
    setEditingCategoryID(null);
    setCategoryDraft({ name: "", parentId: "", downloadPath: "/downloads" });
  }

  async function saveCategory(event: FormEvent) {
    event.preventDefault();
    setSavingCategory(true);
    try {
      await api(`/api/categories${editingCategoryID ? `/${editingCategoryID}` : ""}`, {
        method: editingCategoryID ? "PUT" : "POST",
        body: JSON.stringify(categoryDraft),
      });
      notify(editingCategoryID ? "分类已更新" : "分类已创建");
      resetCategoryEditor();
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "保存分类失败", "error"); }
    finally { setSavingCategory(false); }
  }

  async function deleteCategory(category: StorageCategory) {
    if (!window.confirm(`确定删除分类“${category.name}”吗？分类中的文件不会删除。`)) return;
    try {
      await api(`/api/categories/${category.id}`, { method: "DELETE" });
      notify("分类已删除");
      if (editingCategoryID === category.id) resetCategoryEditor();
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "删除分类失败", "error"); }
  }

  const subscribedURLs = useMemo(() => new Set(state.subscriptions.map((item) => item.sourceUrl)), [state.subscriptions]);
  const existingEpisodeURLs = useMemo(() => new Set(state.downloads.filter((task) => task.status !== "cancelled" && task.status !== "failed").map((task) => task.pageUrl)), [state.downloads]);
  const availableEpisodes = useMemo(() => preview?.episodes.filter((episode) => !existingEpisodeURLs.has(episode.pageUrl)) ?? [], [preview, existingEpisodeURLs]);
  const visibleTasks = useMemo(() => taskFilter === "all" ? state.downloads : state.downloads.filter((task) => task.status === taskFilter), [state.downloads, taskFilter]);
  const orderedCategories = useMemo(() => flattenCategories(state.categories), [state.categories]);
  const categoryNames = useMemo(() => new Map(orderedCategories.map((category) => [category.id, category.label])), [orderedCategories]);
  const pageMeta = {
    search: ["链接下载 / 发现节目", "粘贴央视单集或节目链接，选择要下载的集数"],
    subscriptions: ["我的订阅", `${state.subscriptions.length} 个剧集正在持续追踪`],
    downloads: ["下载任务", state.settings.downloadsPaused ? `${state.stats.paused} 个任务已暂停` : `${state.stats.downloading} 个进行中 · ${state.stats.queued} 个等待中`],
    settings: ["系统设置", "管理分类路径、更新频率和并发任务"],
  }[tab];

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark"><Tv2 size={22} /></span><span><b>剧栈</b><small>CCTV 自动追剧</small></span></div>
        <nav className="nav-list" aria-label="主导航">
          <button className={tab === "search" ? "active" : ""} onClick={() => setTab("search")}><Search size={18} /><span className="nav-label">选集下载</span></button>
          <button className={tab === "subscriptions" ? "active" : ""} onClick={() => setTab("subscriptions")}><BellRing size={18} /><span className="nav-label">我的订阅</span><span className="nav-count">{state.subscriptions.length}</span></button>
          <button className={tab === "downloads" ? "active" : ""} onClick={() => setTab("downloads")}><HardDriveDownload size={18} /><span className="nav-label">下载任务</span>{state.stats.downloading > 0 && <span className="nav-live">{state.stats.downloading}</span>}</button>
          <button className={tab === "settings" ? "active" : ""} onClick={() => { setTab("settings"); setSettingsDraft(state.settings); }}><Settings size={18} /><span className="nav-label">系统设置</span></button>
        </nav>
        <div className="sidebar-card">
          <span className={`status-dot ${connected ? "online" : ""}`} />
          <div><b>{connected ? "服务运行中" : connected === null ? "正在连接" : "后台未连接"}</b><small>{connected ? `每 ${state.settings.pollIntervalMinutes} 分钟检查更新` : "请启动 Go 后台服务"}</small></div>
        </div>
        <div className="sidebar-foot"><Sparkles size={15} /><span>官方源 · 自动归档</span></div>
      </aside>

      <main className="main-area">
        <header className="topbar">
          <div><h1>{pageMeta[0]}</h1><p>{pageMeta[1]}</p></div>
          <div className="top-actions">
            <span className={`connection-pill ${connected ? "ready" : ""}`}>{connected ? <Check size={14} /> : <WifiOff size={14} />}{connected ? "已连接" : "离线"}</span>
            <button className="icon-button" title="刷新数据" aria-label="刷新数据" onClick={() => loadState()}><RefreshCw size={18} /></button>
          </div>
        </header>

        {!connected && connected !== null && <div className="offline-banner"><WifiOff size={18} /><span>界面已就绪，但还没有连接到 Go 后台。请先运行后端服务后再搜索。</span><button onClick={() => loadState()}>重新连接</button></div>}

        {tab === "search" && (
          <section className="page-content search-page">
            <div className="search-hero">
              <div className="eyebrow"><span /><b>央视网 · 链接下载</b></div>
              <h2>粘贴视频链接，<br /><em>选好集数就下载。</em></h2>
              <p>支持央视单集播放页和剧集主页，无需先订阅。也能搜索剧名，选择几集下载，想追更时再订阅。</p>
              <form className="search-box" onSubmit={runSearch}>
                <Search size={21} />
                <input aria-label="搜索剧集" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="粘贴 https://tv.cctv.com/... 单集或节目链接，或输入剧名" />
                {query && <button type="button" className="clear-search" onClick={() => setQuery("")} aria-label="清空搜索"><X size={16} /></button>}
                <button className="search-submit" disabled={searching || !connected}>{searching ? <LoaderCircle className="spin" size={18} /> : <Search size={18} />}{searching ? "正在解析" : /^https?:\/\//i.test(query.trim()) ? "解析链接" : "搜索"}</button>
              </form>
              <div className="search-hints"><span>按剧名搜索：</span>{["汪汪队立大功", "航拍中国", "典籍里的中国"].map((hint) => <button key={hint} onClick={() => { setQuery(hint); }}>{hint}</button>)}</div>
            </div>

            {(searched || results.length > 0) && <div className="results-section">
              <div className="section-heading"><div><h3>搜索结果</h3><p>{searching ? "正在从央视网获取节目…" : `找到 ${results.length} 个节目`}</p></div><span className="source-badge"><span />数据来自 CCTV.com</span></div>
              {searching ? <div className="result-grid">{[1,2,3].map((item) => <div className="result-card skeleton-card" key={item}><div className="poster-skeleton" /><div className="skeleton-lines"><i /><i /><i /></div></div>)}</div>
              : results.length > 0 ? <div className="result-grid">{results.map((result) => {
                const subscribed = subscribedURLs.has(result.sourceUrl);
                return <article className="result-card" key={result.sourceUrl}>
                  <div className="poster-wrap"><img src={result.poster} alt={`${result.title}海报`} /><span className="quality-tag">CCTV</span></div>
                  <div className="result-body"><div className="result-meta"><span>{result.year || "央视节目"}</span>{result.category && <><i /> <span>{result.category}</span></>}{result.episodeCount ? <><i /><span>共 {result.episodeCount} 集</span></> : null}</div>
                    <h4>{result.title}</h4><p>{result.description || "暂无节目简介"}</p>
                    <div className="result-actions"><button className="subscribe-button direct-download" disabled={resolvingURL === result.sourceUrl} onClick={() => openEpisodePicker(result.sourceUrl)}>{resolvingURL === result.sourceUrl ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}{resolvingURL === result.sourceUrl ? "正在解析" : result.isSingle ? "下载本集" : "选集下载"}</button>{!result.isSingle && <button className={`follow-button ${subscribed ? "subscribed" : ""}`} disabled={subscribed} onClick={() => { setAdding(result); setDownloadExisting(false); setSelectedCategoryID(orderedCategories[0]?.id ?? ""); }}>{subscribed ? <Check size={14} /> : <BellRing size={14} />}{subscribed ? "已订阅" : "订阅追更"}</button>}<a href={result.sourceUrl} target="_blank" rel="noreferrer" title="在央视网查看"><ChevronRight size={19} /></a></div>
                  </div>
                </article>;
              })}</div> : <div className="empty-state"><Search size={30} /><h3>没有找到匹配的剧集</h3><p>换个关键词，或直接粘贴 tv.cctv.com 的单集、剧集链接。</p></div>}
            </div>}
          </section>
        )}

        {tab === "subscriptions" && <section className="page-content">
          <div className="stats-row">
            <Stat icon={<BellRing size={20} />} label="活跃订阅" value={state.subscriptions.filter((s) => s.status === "active").length} tone="orange" />
            <Stat icon={<Download size={20} />} label="已下载剧集" value={state.subscriptions.reduce((sum, s) => sum + s.downloadedCount, 0)} tone="green" />
            <Stat icon={<Clock3 size={20} />} label="检查间隔" value={`${state.settings.pollIntervalMinutes}m`} tone="blue" />
          </div>
          <div className="section-heading compact"><div><h3>全部订阅</h3><p>暂停订阅不会删除已有下载</p></div><button className="primary-small" onClick={() => setTab("search")}><Plus size={16} />添加订阅</button></div>
          {state.subscriptions.length ? <div className="subscription-list">{state.subscriptions.map((sub) => <article className="subscription-card" key={sub.id}>
            <img src={sub.poster} alt="" />
            <div className="subscription-info"><div className="subscription-title"><h3>{sub.title}</h3><span className={`sub-state ${sub.status}`}>{sub.status === "active" ? "订阅中" : "已暂停"}</span></div><p>{sub.description}</p><div className="subscription-meta"><span>{sub.year || "年份未知"}</span><i /><span>{sub.episodeCount} 集</span><i /><span>{categoryNames.get(sub.storageCategoryId) || "未分类"}</span><i /><span>上次检查 {timeAgo(sub.lastCheckedAt)}</span></div>{sub.lastError && <div className="inline-error">{sub.lastError}</div>}</div>
            <div className="subscription-progress"><div><span>下载进度</span><b>{sub.downloadedCount} / {sub.episodeCount}</b></div><div className="progress-track"><span style={{ width: `${sub.episodeCount ? sub.downloadedCount / sub.episodeCount * 100 : 0}%` }} /></div></div>
            <div className="subscription-actions"><button title="立即检查" onClick={() => checkSubscription(sub)}><RefreshCw size={17} /></button><button title={sub.status === "active" ? "暂停订阅" : "恢复订阅"} onClick={() => updateSubscription(sub, { status: sub.status === "active" ? "paused" : "active" })}>{sub.status === "active" ? <Pause size={17} /> : <Play size={17} />}</button><button title="更多操作" onClick={() => setMenuOpen(menuOpen === sub.id ? null : sub.id)}><MoreHorizontal size={18} /></button>{menuOpen === sub.id && <div className="context-menu"><label className="context-category">保存分类<select value={sub.storageCategoryId} onChange={async (event) => { await updateSubscription(sub, { storageCategoryId: event.target.value }); setMenuOpen(null); }}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select></label><button onClick={() => updateSubscription(sub, { autoDownload: !sub.autoDownload })}>{sub.autoDownload ? <CirclePause size={15} /> : <Download size={15} />}{sub.autoDownload ? "关闭自动下载" : "开启自动下载"}</button><button className="danger" onClick={() => removeSubscription(sub)}><Trash2 size={15} />删除订阅</button></div>}</div>
          </article>)}</div> : <div className="empty-state large"><BellRing size={32} /><h3>还没有订阅</h3><p>先去搜索喜欢的节目，更新后会自动出现在下载队列。</p><button className="primary-small" onClick={() => setTab("search")}><Search size={16} />发现剧集</button></div>}
        </section>}

        {tab === "downloads" && <section className="page-content">
          <div className={`download-summary ${state.settings.downloadsPaused ? "paused" : ""}`}><div className="summary-orb">{state.settings.downloadsPaused ? <CirclePause size={28} /> : <Gauge size={28} />}</div><div><span>{state.settings.downloadsPaused ? "全局状态" : "当前下载"}</span><strong>{state.settings.downloadsPaused ? state.stats.paused : state.stats.downloading}</strong><small>{state.settings.downloadsPaused ? "任务已安全暂停" : `并发上限 ${state.settings.concurrentDownloads}`}</small></div><div className="summary-divider" /><div><span>排队等待</span><strong>{state.stats.queued}</strong><small>{state.settings.downloadsPaused ? "恢复后自动继续" : "自动按加入顺序处理"}</small></div><div className="summary-divider" /><div><span>累计完成</span><strong>{state.stats.completed}</strong><small>{state.stats.failed ? `${state.stats.failed} 个任务失败` : "任务状态正常"}</small></div></div>
          <div className="task-toolbar"><div className="filter-tabs">{[["all","全部"],["downloading","下载中"],["queued","等待中"],["paused","已暂停"],["completed","已完成"],["failed","失败"]].map(([key,label]) => <button className={taskFilter === key ? "active" : ""} key={key} onClick={() => setTaskFilter(key)}>{label}</button>)}</div><div className="toolbar-actions"><button className={`bulk-control ${state.settings.downloadsPaused ? "resume" : ""}`} disabled={bulkBusy} onClick={toggleAllDownloads}>{bulkBusy ? <LoaderCircle className="spin" size={15} /> : state.settings.downloadsPaused ? <Play size={15} /> : <Pause size={15} />}{state.settings.downloadsPaused ? "全部恢复" : "全部暂停"}</button><button className="ghost-small" onClick={() => loadState()}><RefreshCw size={15} />刷新</button></div></div>
          {visibleTasks.length ? <div className="task-list">{visibleTasks.map((task) => <article className="task-row" key={task.id}><div className="task-thumb"><img src={task.poster} alt="" />{task.status === "completed" && <span><Check size={14} /></span>}</div><div className="task-main"><div className="task-heading"><div><h3>{task.title}</h3><p>{task.seriesTitle}</p></div><span className={`task-status ${task.status}`}>{task.status === "downloading" && <LoaderCircle className="spin" size={13} />}{statusText(task.status)}</span></div>
            {(task.status === "downloading" || task.status === "queued" || task.status === "paused") && <div className="task-progress"><div className="progress-track"><span style={{ width: `${task.progress}%` }} /></div><div><b>{task.status === "queued" ? "等待可用下载槽" : task.status === "paused" ? "已暂停，恢复后重新开始" : `${Math.round(task.progress)}%`}</b><span>{task.speed || (task.status === "queued" || task.status === "paused" ? "" : "正在连接资源…")}</span></div></div>}
            {task.status === "failed" && <div className="task-error">{task.error}</div>}{task.status === "completed" && <div className="task-file"><FolderOpen size={14} /><span title={task.filePath}>{task.filePath}</span>{task.videoQuality && <b title="实际保存的视频画质"> · {task.videoQuality}</b>}</div>}
          </div><div className="task-actions">{task.status === "completed" && task.filePath && <><button className="retry" title="重新下载并修复之前损坏的视频" onClick={() => taskAction(task, "retry")}><RotateCcw size={15} />重新下载</button><button className="open-folder" title="在运行程序的电脑上打开保存目录" disabled={openingDirectoryID === task.id} onClick={() => openDownloadDirectory(task)}>{openingDirectoryID === task.id ? <LoaderCircle className="spin" size={15} /> : <FolderOpen size={15} />}打开目录</button><button className="play-video" title="在线播放已下载视频" onClick={() => setPlayingTask(task)}><Play size={15} />播放</button></>}{(task.status === "queued" || task.status === "downloading" || task.status === "paused") && <button title="取消任务" onClick={() => taskAction(task, "cancel")}><X size={17} /></button>}{(task.status === "failed" || task.status === "cancelled") && <button className="retry" onClick={() => taskAction(task, "retry")}><RotateCcw size={15} />重试</button>}</div></article>)}</div> : <div className="empty-state large"><HardDriveDownload size={32} /><h3>这里暂时没有任务</h3><p>去“选集下载”粘贴链接并勾选集数，或订阅节目自动下载。</p></div>}
        </section>}

        {tab === "settings" && <section className="page-content settings-grid">
          <div className="settings-stack">
            <form className="settings-card" onSubmit={saveSettings}>
              <div className="settings-card-head"><span><SlidersHorizontal size={20} /></span><div><h3>订阅与任务</h3><p>无需重启，新下载任务自动使用并发设置</p></div></div>
              <div className="field-grid"><label className="field-label">轮询间隔（分钟）<input type="number" min={1} max={10080} value={settingsDraft.pollIntervalMinutes} onChange={(event) => setSettingsDraft({ ...settingsDraft, pollIntervalMinutes: Number(event.target.value) })} /><small>建议 15 分钟以上</small></label><label className="field-label">同时下载剧集数<input type="number" min={1} max={10} value={settingsDraft.concurrentDownloads} onChange={(event) => setSettingsDraft({ ...settingsDraft, concurrentDownloads: Number(event.target.value) })} /><small>建议 1–2 集，多个剧集同时处理会占用更多 CPU</small></label><label className="field-label">单集分片并发数<input type="number" min={1} max={16} value={settingsDraft.segmentConcurrency} onChange={(event) => setSettingsDraft({ ...settingsDraft, segmentConcurrency: Number(event.target.value) })} /><small>建议 4–8 路，同时下载一集的多个 TS 分片</small></label></div>
              <label className="toggle-setting"><span className="toggle-icon"><ShieldCheck size={19} /></span><span className="toggle-copy"><b>下载完成后检测花屏</b><small>开启后会严格解码整片，能发现坏帧但会增加 CPU 占用</small></span><input type="checkbox" checked={settingsDraft.verifyVideoAfterDownload} onChange={(event) => setSettingsDraft({ ...settingsDraft, verifyVideoAfterDownload: event.target.checked })} /><span className="toggle-switch" aria-hidden="true" /></label>
              <button className="save-button" disabled={savingSettings || !connected}>{savingSettings ? <LoaderCircle className="spin" size={17} /> : <Check size={17} />}{savingSettings ? "正在保存" : "保存设置"}</button>
            </form>
            <section className="settings-card category-card">
              <div className="settings-card-head"><span><FolderTree size={20} /></span><div><h3>分类与保存路径</h3><p>支持多级分类，每个分类使用独立保存位置</p></div></div>
              <div className="category-list">{orderedCategories.map((category) => <div className="category-row" key={category.id} style={{ paddingLeft: `${12 + category.depth * 22}px` }}><span className="category-branch">{category.depth ? "└" : <FolderOpen size={15} />}</span><div><b>{category.name}</b><small title={category.downloadPath}>{category.downloadPath}</small></div><span className="category-sub-count">{state.subscriptions.filter((sub) => sub.storageCategoryId === category.id).length} 个订阅</span><button type="button" title="编辑分类" onClick={() => beginCategoryEdit(category)}><Pencil size={14} /></button><button type="button" title="删除分类" className="danger" onClick={() => deleteCategory(category)}><Trash2 size={14} /></button></div>)}</div>
              <form className="category-editor" onSubmit={saveCategory}>
                <div className="category-editor-title"><b>{editingCategoryID ? "编辑分类" : "新建分类"}</b>{editingCategoryID && <button type="button" onClick={resetCategoryEditor}>取消编辑</button>}</div>
                <div className="category-editor-grid"><label className="field-label">分类名称<input value={categoryDraft.name} onChange={(event) => setCategoryDraft({ ...categoryDraft, name: event.target.value })} placeholder="例如：儿童动画" required /></label><label className="field-label">父分类<select value={categoryDraft.parentId} onChange={(event) => setCategoryDraft({ ...categoryDraft, parentId: event.target.value })}><option value="">无（顶级分类）</option>{orderedCategories.filter((category) => category.id !== editingCategoryID).map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select></label></div>
                <label className="field-label">分类保存路径<div className="path-input"><input value={categoryDraft.downloadPath} onChange={(event) => setCategoryDraft({ ...categoryDraft, downloadPath: event.target.value })} placeholder="/downloads/儿童动画" required /><FolderOpen size={17} /></div><small>视频最终保存为：分类路径 / 剧集名称 / 单集.mp4</small></label>
                <button className="save-button" disabled={savingCategory || !connected}>{savingCategory ? <LoaderCircle className="spin" size={17} /> : <Plus size={17} />}{savingCategory ? "正在保存" : editingCategoryID ? "保存分类" : "添加分类"}</button>
              </form>
            </section>
          </div>
          <aside className="settings-aside"><div className="runtime-card"><div className="runtime-head"><span className={`status-dot ${connected ? "online" : ""}`} /><div><b>运行状态</b><small>Go 下载服务</small></div><span className={`healthy ${connected ? "" : "offline"}`}>{connected ? "正常" : "离线"}</span></div><dl><div><dt>视频处理</dt><dd>{ffmpeg ? "最高画质 · 官方解密" : "组件未就绪"}</dd></div><div><dt>花屏检测</dt><dd>{state.settings.verifyVideoAfterDownload ? "已开启" : "已关闭"}</dd></div><div><dt>数据来源</dt><dd>tv.cctv.com</dd></div><div><dt>状态存储</dt><dd>SQLite</dd></div></dl></div><div className="tip-card"><Sparkles size={18} /><div><b>小提示</b><p>{ffmpeg ? "下载时会选择最高分辨率，并通过 CCTV 官方播放器链路解密后无损封装为 MP4。" : "视频处理组件尚未就绪，请检查后台运行状态。"}</p></div></div></aside>
        </section>}
      </main>

      {preview && <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !queueBusy) setPreview(null); }}>
        <div className="episode-modal" role="dialog" aria-modal="true" aria-labelledby="episode-picker-title">
          <header className="episode-modal-header">
            {preview.poster ? <img src={preview.poster} alt="" /> : <span className="episode-poster-placeholder"><Tv2 size={26} /></span>}
            <div className="episode-modal-title">
              <span className="modal-kicker"><Download size={14} />{preview.isSingle ? "单集直接下载" : "选择要下载的剧集"}</span>
              <h2 id="episode-picker-title">{preview.title}</h2>
              <p>{preview.episodes.length} 集可选 · 下载后可在任务列表直接播放</p>
            </div>
            <button className="episode-modal-close" aria-label="关闭选集" title="关闭" onClick={() => setPreview(null)} disabled={queueBusy}><X size={19} /></button>
          </header>
          <div className="episode-picker-toolbar">
            <strong>选集列表 <span>已选 {selectedEpisodeURLs.length} / {preview.episodes.length}</span></strong>
            {!preview.isSingle && <div>
              <button onClick={() => setSelectedEpisodeURLs(availableEpisodes.slice(0, 200).map((ep) => ep.pageUrl))}>全选{availableEpisodes.length > 200 ? "前 200 集" : ""}</button>
              <button onClick={() => setSelectedEpisodeURLs(availableEpisodes.length ? [availableEpisodes[availableEpisodes.length - 1].pageUrl] : [])}>只选最新</button>
              <button onClick={() => setSelectedEpisodeURLs([])}>清空</button>
            </div>}
          </div>
          <div className="episode-picker-list">
            {preview.episodes.map((episode) => {
              const exists = existingEpisodeURLs.has(episode.pageUrl);
              const selected = selectedEpisodeURLs.includes(episode.pageUrl);
              return <label key={episode.pageUrl} className={`episode-picker-item ${selected ? "selected" : ""} ${exists ? "already-queued" : ""}`}>
                <input type="checkbox" checked={selected} disabled={exists || queueBusy} onChange={() => toggleEpisode(episode.pageUrl)} />
                <span className="episode-index">{episode.number}</span>
                <span className="episode-title">{episode.title}{episode.publishedAt && <small>{episode.publishedAt}</small>}</span>
                {exists ? <span className="episode-exists"><Check size={13} />已加入</span> : selected ? <Check size={17} className="episode-checked" /> : null}
              </label>;
            })}
          </div>
          <footer className="episode-picker-footer">
            <label>保存到 <select value={selectedCategoryID} onChange={(event) => setSelectedCategoryID(event.target.value)} disabled={queueBusy}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select></label>
            <span className="episode-picker-path">{state.categories.find((category) => category.id === selectedCategoryID)?.downloadPath || "尚未设置保存目录"}</span>
            <div className="episode-picker-buttons"><button className="cancel-button" onClick={() => setPreview(null)} disabled={queueBusy}>取消</button><button className="confirm-button" disabled={queueBusy || !selectedCategoryID || selectedEpisodeURLs.length === 0} onClick={queueSelectedEpisodes}>{queueBusy ? <LoaderCircle className="spin" size={17} /> : <Download size={17} />}{queueBusy ? "正在加入队列" : `下载选中 ${selectedEpisodeURLs.length} 集`}</button></div>
          </footer>
        </div>
      </div>}
      {adding && <div className="modal-backdrop"><div className="add-modal" role="dialog" aria-modal="true" aria-labelledby="add-title"><button className="modal-close" onClick={() => setAdding(null)} disabled={addBusy}><X size={18} /></button><img src={adding.poster} alt="" /><div className="modal-copy"><span className="modal-kicker"><BellRing size={14} />新订阅</span><h2 id="add-title">{adding.title}</h2><p>订阅后系统会每 {state.settings.pollIntervalMinutes} 分钟检查一次，新剧集将自动进入下载队列。</p><label className="modal-category">保存分类<select value={selectedCategoryID} onChange={(event) => setSelectedCategoryID(event.target.value)}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select><small>{state.categories.find((category) => category.id === selectedCategoryID)?.downloadPath || "请先在设置中创建分类"}</small></label><div className="check-row"><input id="download-existing" type="checkbox" checked={downloadExisting} onChange={(event) => setDownloadExisting(event.target.checked)} /><label htmlFor="download-existing"><b>同时下载已有剧集</b><small>{adding.episodeCount ? `将加入 ${adding.episodeCount} 个下载任务` : "已有选集会加入下载队列"}</small></label></div><div className="modal-actions"><button className="cancel-button" onClick={() => setAdding(null)} disabled={addBusy}>取消</button><button className="confirm-button" onClick={confirmAdd} disabled={addBusy || !selectedCategoryID}>{addBusy ? <LoaderCircle className="spin" size={17} /> : <BellRing size={17} />}{addBusy ? "正在添加" : "确认订阅"}</button></div></div></div></div>}
      {playingTask && <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) setPlayingTask(null); }}><div className="video-modal" role="dialog" aria-modal="true" aria-label={`播放 ${playingTask.title}`}><div className="video-modal-header"><div><strong>{playingTask.title}</strong><small>{playingTask.seriesTitle}{playingTask.videoQuality ? ` · ${playingTask.videoQuality}` : ""}</small></div><button type="button" title="关闭播放器" aria-label="关闭播放器" onClick={() => setPlayingTask(null)}><X size={20} /></button></div><video key={playingTask.id} src={`${API_BASE}/api/downloads/${encodeURIComponent(playingTask.id)}/video`} controls autoPlay playsInline preload="metadata" onError={() => notify("播放失败：请检查文件是否存在以及浏览器是否支持该视频编码", "error")} /><p>直接播放本机保存的 MP4，不会重新下载。若没有画面，可尝试在下载页面重新下载该集。</p></div></div>}
      {toast && <div className={`toast ${toast.type}`}><span>{toast.type === "ok" ? <CheckCircle2 size={18} /> : <X size={18} />}</span>{toast.text}</div>}
    </div>
  );
}

function Stat({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: string | number; tone: string }) {
  return <div className="stat-card"><span className={`stat-icon ${tone}`}>{icon}</span><div><small>{label}</small><strong>{value}</strong></div></div>;
}

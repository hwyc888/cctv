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
  ListChecks,
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

type Tab = "library" | "search" | "subscriptions" | "downloads" | "settings";
type LibraryKind = "animation" | "drama" | "documentary" | "special" | "columns";
type LibraryProgram = { id: string; kind: LibraryKind; title: string; poster: string; description: string; sourceUrl?: string };
type LibraryResult = { programs: LibraryProgram[]; page: number; hasMore: boolean };
type LibraryEpisodesResult = { episodes: Episode[]; page: number; hasMore: boolean; total?: number };
const LIBRARY_CATEGORIES: { id: LibraryKind; name: string; description: string }[] = [
  { id: "animation", name: "动画片", description: "少儿与动画" },
  { id: "drama", name: "电视剧", description: "热门电视剧" },
  { id: "documentary", name: "纪录片", description: "人文与自然" },
  { id: "special", name: "特别节目", description: "专题与晚会" },
  { id: "columns", name: "栏目大全", description: "按 CCTV 频道" },
];
const CCTV_CHANNELS = [
  ["", "全部频道"], ["EPGC1386744804340101", "CCTV-1 综合"], ["EPGC1386744804340102", "CCTV-2 财经"],
  ["EPGC1386744804340103", "CCTV-3 综艺"], ["EPGC1386744804340104", "CCTV-4 中文国际"],
  ["EPGC1386744804340107", "CCTV-5 体育"], ["EPGC1386744804340108", "CCTV-6 电影"],
  ["EPGC1386744804340109", "CCTV-7 国防军事"], ["EPGC1386744804340110", "CCTV-8 电视剧"],
  ["EPGC1386744804340112", "CCTV-9 纪录"], ["EPGC1386744804340113", "CCTV-10 科教"],
  ["EPGC1386744804340114", "CCTV-11 戏曲"], ["EPGC1386744804340115", "CCTV-12 社会与法"],
  ["EPGC1386744804340116", "CCTV-13 新闻"], ["EPGC1386744804340117", "CCTV-14 少儿"],
  ["EPGC1386744804340118", "CCTV-15 音乐"], ["EPGC1634630207058998", "CCTV-16 奥林匹克"],
  ["EPGC1563932742616872", "CCTV-17 农业农村"], ["EPGC1468294755566101", "CCTV-5+ 体育赛事"],
] as const;
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
  videoGuid?: string;
  image?: string;
  publishedAt?: string;
};
type SeriesPreview = SearchResult & {
  episodes: Episode[];
  libraryKind?: LibraryKind;
  libraryId?: string;
  libraryPage?: number;
  libraryHasMore?: boolean;
  libraryTotal?: number;
};
type SubscriptionEpisodeRow = {
  id: string; number: number; title: string; pageUrl: string; videoGuid?: string; publishedAt?: string;
  status: DownloadTask["status"] | "not_downloaded"; taskId?: string; filePath?: string; progress?: number;
  speed?: string; error?: string; createdAt?: string; completedAt?: string;
};
type SubscriptionEpisodeResult = { episodes: SubscriptionEpisodeRow[]; total: number; checkedAt: string };
type Subscription = SearchResult & {
  id: string;
  storageCategoryId: string;
  status: "active" | "paused";
  autoDownload: boolean;
  episodeCount: number;
  downloadedCount: number;
  lastCheckedAt?: string;
  lastError?: string;
  archiveScanned?: boolean;
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
  videoGuid?: string;
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

function subscriptionSourceKey(value?: string) {
  if (!value) return "";
  try {
    const url = new URL(value);
    return `${url.hostname.toLowerCase()}${url.pathname.replace(/\/+$/, "") || "/"}`;
  } catch {
    return value.trim().replace(/[?#].*$/, "").replace(/\/+$/, "");
  }
}

function episodeKey(episode: Episode) {
  return episode.videoGuid || episode.pageUrl;
}

function taskKeys(task: DownloadTask) {
  return [task.videoGuid, task.pageUrl].filter((value): value is string => !!value);
}

function isEpisodeQueued(episode: Episode, existing: Set<string>) {
  return !!((episode.videoGuid && existing.has(episode.videoGuid)) || (episode.pageUrl && existing.has(episode.pageUrl)));
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
  const [tab, setTab] = useState<Tab>("library");
  const [state, setState] = useState<AppState>({ settings: initialSettings, categories: [], subscriptions: [], downloads: [], stats: initialStats });
  const [connected, setConnected] = useState<boolean | null>(null);
  const [ffmpeg, setFfmpeg] = useState(false);
  const [backendRevision, setBackendRevision] = useState("旧版/未知");
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [searching, setSearching] = useState(false);
  const [libraryKind, setLibraryKind] = useState<LibraryKind>("animation");
  const [libraryChannel, setLibraryChannel] = useState("");
  const [libraryPage, setLibraryPage] = useState(1);
  const [libraryReload, setLibraryReload] = useState(0);
  const [libraryResult, setLibraryResult] = useState<LibraryResult>({ programs: [], page: 1, hasMore: false });
  const [libraryLoading, setLibraryLoading] = useState(false);
  const [libraryError, setLibraryError] = useState("");
  const [resolvingLibraryID, setResolvingLibraryID] = useState<string | null>(null);
  const [episodePageLoading, setEpisodePageLoading] = useState(false);
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
  const [selectedTaskIDs, setSelectedTaskIDs] = useState<string[]>([]);
  const [deletingTaskIDs, setDeletingTaskIDs] = useState<string[]>([]);
  const [toast, setToast] = useState<{ text: string; type: "ok" | "error" } | null>(null);
  const [menuOpen, setMenuOpen] = useState<string | null>(null);
  const [managedSubscription, setManagedSubscription] = useState<Subscription | null>(null);
  const [managedEpisodes, setManagedEpisodes] = useState<SubscriptionEpisodeRow[]>([]);
  const [managedEpisodesLoading, setManagedEpisodesLoading] = useState(false);
  const [managedEpisodesBusy, setManagedEpisodesBusy] = useState(false);
  const [managedEpisodeFilter, setManagedEpisodeFilter] = useState("all");
  const [selectedManagedEpisodeIDs, setSelectedManagedEpisodeIDs] = useState<string[]>([]);
  const [openingDirectoryID, setOpeningDirectoryID] = useState<string | null>(null);
  const settingsInitialized = useRef(false);
  const managedEpisodePollBusy = useRef(false);
  const managedSubscriptionID = managedSubscription?.id;

  const notify = useCallback((text: string, type: "ok" | "error" = "ok") => {
    setToast({ text, type });
    window.setTimeout(() => setToast(null), 3200);
  }, []);

  const loadState = useCallback(async (quiet = false) => {
    try {
      const [next, health] = await Promise.all([
        api<AppState>("/api/state"),
        api<{ ok: boolean; ffmpeg: boolean; revision?: string }>("/api/health"),
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
      setBackendRevision(health.revision ? health.revision.slice(0, 8) : "旧版/未知");
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

  useEffect(() => {
    if (tab !== "library") return;
    let active = true;
    const params = new URLSearchParams({ kind: libraryKind, page: String(libraryPage) });
    if (libraryKind === "columns" && libraryChannel) params.set("channel", libraryChannel);
    setLibraryLoading(true);
    setLibraryError("");
    api<LibraryResult>(`/api/library/programs?${params}`).then((response) => {
      if (active) setLibraryResult(response);
    }).catch((error: unknown) => {
      if (active) {
        setLibraryResult({ programs: [], page: libraryPage, hasMore: false });
        setLibraryError(error instanceof Error ? error.message : "无法加载央视节目目录");
      }
    }).finally(() => { if (active) setLibraryLoading(false); });
    return () => { active = false; };
  }, [tab, libraryKind, libraryChannel, libraryPage, libraryReload]);

  useEffect(() => {
    if (!managedSubscriptionID) return;
    let active = true;
    const refresh = async () => {
      if (managedEpisodePollBusy.current || managedEpisodesBusy || managedEpisodesLoading) return;
      managedEpisodePollBusy.current = true;
      try {
        const result = await api<SubscriptionEpisodeResult>(`/api/subscriptions/${encodeURIComponent(managedSubscriptionID)}/episodes`);
        if (active) setManagedEpisodes(result.episodes);
      } catch {
        // A temporary polling error should not interrupt the management dialog.
      } finally {
        managedEpisodePollBusy.current = false;
      }
    };
    const timer = window.setInterval(() => { void refresh(); }, 2500);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [managedSubscriptionID, managedEpisodesBusy, managedEpisodesLoading]);

  function showEpisodePicker(series: SeriesPreview) {
    setPreview(series);
    const existing = new Set(state.downloads.filter((task) => task.status !== "failed" && task.status !== "cancelled").flatMap(taskKeys));
    const available = series.episodes.filter((episode) => !isEpisodeQueued(episode, existing));
    setSelectedEpisodeURLs(series.isSingle ? available.slice(0, 1).map(episodeKey) : []);
    setSelectedCategoryID((current) => state.categories.some((category) => category.id === current) ? current : (state.categories[0]?.id ?? ""));
  }

  function startLibrarySubscription(program: LibraryProgram) {
    const sourceUrl = program.sourceUrl?.trim();
    if (!sourceUrl) {
      notify("央视目录没有提供可用于自动追更的节目主页链接，暂时无法直接订阅。请先打开选集，或通过“链接与搜索”粘贴节目主页链接。", "error");
      return;
    }
    const sourceKey = subscriptionSourceKey(sourceUrl);
    const existing = state.subscriptions.find((item) => subscriptionSourceKey(item.sourceUrl) === sourceKey);
    if (existing) {
      setTab("subscriptions");
      void openSubscriptionManager(existing);
      return;
    }
    setAdding({
      title: program.title,
      sourceUrl,
      poster: program.poster,
      description: program.description,
      isSingle: false,
    });
    setDownloadExisting(false);
    setSelectedCategoryID(orderedCategories[0]?.id ?? "");
  }

  async function openLibraryProgram(program: LibraryProgram, page = 1) {
    setResolvingLibraryID(program.id);
    try {
      const params = new URLSearchParams({ kind: program.kind, id: program.id, page: String(page) });
      const data = await api<LibraryEpisodesResult>(`/api/library/episodes?${params}`);
      showEpisodePicker({
        title: program.title, sourceUrl: program.sourceUrl || "", poster: program.poster,
        description: program.description, episodeCount: data.episodes.length,
        episodes: data.episodes, libraryKind: program.kind, libraryId: program.id,
        libraryPage: data.page, libraryHasMore: data.hasMore, libraryTotal: data.total,
      });
    } catch (error) {
      notify(error instanceof Error ? error.message : "无法加载央视选集", "error");
    } finally {
      setResolvingLibraryID(null);
    }
  }

  async function changeLibraryEpisodePage(page: number) {
    if (!preview?.libraryKind || !preview.libraryId || page < 1 || episodePageLoading) return;
    setEpisodePageLoading(true);
    try {
      const params = new URLSearchParams({ kind: preview.libraryKind, id: preview.libraryId, page: String(page) });
      const result = await api<LibraryEpisodesResult>(`/api/library/episodes?${params}`);
      setPreview({ ...preview, episodes: result.episodes, libraryPage: result.page, libraryHasMore: result.hasMore, libraryTotal: result.total });
      setSelectedEpisodeURLs([]);
    } catch (error) {
      notify(error instanceof Error ? error.message : "加载下一页选集失败", "error");
    } finally {
      setEpisodePageLoading(false);
    }
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
      const inLibrary = !!preview.libraryKind && !!preview.libraryId;
      const response = await api<{ added: number; skipped: number }>(inLibrary ? "/api/library/downloads" : "/api/downloads", {
        method: "POST",
        body: JSON.stringify(inLibrary ? {
          kind: preview.libraryKind, id: preview.libraryId, page: preview.libraryPage,
          programTitle: preview.title, videoGuids: selectedEpisodeURLs,
          storageCategoryId: selectedCategoryID,
        } : {
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

  async function openSubscriptionManager(sub: Subscription) {
    setManagedSubscription(sub);
    setManagedEpisodes([]);
    setSelectedManagedEpisodeIDs([]);
    setManagedEpisodeFilter("all");
    setManagedEpisodesLoading(true);
    try {
      const result = await api<SubscriptionEpisodeResult>(`/api/subscriptions/${encodeURIComponent(sub.id)}/episodes`);
      setManagedEpisodes(result.episodes);
      if (result.episodes.length === 0 || !sub.archiveScanned) {
        // Existing subscriptions created before archive-aware parsing are
        // automatically backfilled once when the management dialog is opened.
        try {
          await api(`/api/subscriptions/${encodeURIComponent(sub.id)}/check`, { method: "POST" });
          notify(!sub.archiveScanned
            ? "正在补齐历史集数；历史集只加入管理列表，不会自动排队下载。扫描完成后列表会自动刷新"
            : "尚未读取到集数，已自动检查央视目录；列表会自动刷新");
        } catch {
          notify("暂时没有集数记录，请点击“检查新集”重试", "error");
        }
      }
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "读取订阅集数失败", "error"); }
    finally { setManagedEpisodesLoading(false); }
  }

  async function refreshManagedEpisodes() {
    if (!managedSubscription) return;
    setManagedEpisodesLoading(true);
    try {
      const result = await api<SubscriptionEpisodeResult>(`/api/subscriptions/${encodeURIComponent(managedSubscription.id)}/episodes`);
      setManagedEpisodes(result.episodes);
      await loadState(true);
    } catch (error) { notify(error instanceof Error ? error.message : "刷新订阅集数失败", "error"); }
    finally { setManagedEpisodesLoading(false); }
  }

  function toggleManagedEpisode(id: string) {
    setSelectedManagedEpisodeIDs((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]);
  }

  async function queueManagedEpisodes(ids: string[]) {
    if (!managedSubscription || !ids.length || managedEpisodesBusy) return;
    const selected = managedEpisodes.filter((episode) => ids.includes(episode.id));
    const completedCount = selected.filter((episode) => episode.status === "completed").length;
    if (completedCount && !window.confirm(`选中的集数中有 ${completedCount} 集已经下载完成。重新下载后，原视频会在新文件验证成功后才替换。确定继续吗？`)) return;
    setManagedEpisodesBusy(true);
    try {
      const result = await api<{ added: number; skipped: number }>(`/api/subscriptions/${encodeURIComponent(managedSubscription.id)}/episodes/queue`, {
        method: "POST", body: JSON.stringify({ episodeIds: ids }),
      });
      notify(`已加入/重新下载 ${result.added} 集，跳过 ${result.skipped} 集`);
      setSelectedManagedEpisodeIDs([]);
      await refreshManagedEpisodes();
    } catch (error) { notify(error instanceof Error ? error.message : "加入下载队列失败", "error"); }
    finally { setManagedEpisodesBusy(false); }
  }

  async function deleteManagedEpisodes(episodeIDs: string[]) {
    if (!managedSubscription || managedEpisodesBusy || !episodeIDs.length) return;
    const selected = managedEpisodes.filter((episode) => episodeIDs.includes(episode.id));
    const eligible = selected.filter((episode) => episode.taskId && ["completed", "failed", "cancelled"].includes(episode.status));
    if (!eligible.length) {
      notify("只能删除已完成、失败或已取消的集数；请先取消等待中/下载中的任务", "error");
      return;
    }
    if (eligible.length !== episodeIDs.length) {
      notify("已跳过没有任务记录或仍在下载的集数，只删除可安全删除的任务", "error");
    }
    const ids = eligible.map((episode) => episode.taskId as string);
    if (!window.confirm(`确定删除选中的 ${eligible.length} 集吗？这会删除对应下载记录和本地 MP4 文件；订阅本身会保留，空的剧集目录会自动清理。此操作不可恢复。`)) return;
    setManagedEpisodesBusy(true);
    try {
      const result = await api<{ deleted: number; filesDeleted: number; directoriesDeleted: number }>("/api/downloads/delete-batch", {
        method: "POST", body: JSON.stringify({ ids }),
      });
      notify(`已删除 ${result.deleted} 条任务、${result.filesDeleted} 个视频文件及 ${result.directoriesDeleted ?? 0} 个空目录`);
      setSelectedManagedEpisodeIDs([]);
      await refreshManagedEpisodes();
    } catch (error) {
      notify(error instanceof Error ? error.message : "删除所选集数失败", "error");
      await refreshManagedEpisodes();
    } finally {
      setManagedEpisodesBusy(false);
    }
  }

  async function cancelManagedEpisodes(episodeIDs: string[]) {
    if (!managedSubscription || managedEpisodesBusy || !episodeIDs.length) return;
    const eligible = managedEpisodes.filter((episode) =>
      episodeIDs.includes(episode.id) && !!episode.taskId && ["queued", "downloading", "paused"].includes(episode.status)
    );
    if (!eligible.length) {
      notify("所选集数中没有可以取消的下载任务", "error");
      return;
    }
    if (!window.confirm(`确定取消选中的 ${eligible.length} 个下载任务吗？`)) return;
    setManagedEpisodesBusy(true);
    let cancelled = 0;
    let failed = 0;
    try {
      for (const episode of eligible) {
        try {
          await api(`/api/downloads/${encodeURIComponent(episode.taskId as string)}/cancel`, { method: "POST" });
          cancelled++;
        } catch {
          failed++;
        }
      }
      notify(failed ? `已请求取消 ${cancelled} 集，另有 ${failed} 集操作失败` : `已请求取消 ${cancelled} 集`, failed ? "error" : "ok");
      setSelectedManagedEpisodeIDs([]);
      await refreshManagedEpisodes();
    } finally {
      setManagedEpisodesBusy(false);
    }
  }

  async function manageEpisodeAction(episode: SubscriptionEpisodeRow, action: "retry" | "cancel" | "open" | "play") {
    if (!episode.taskId) return;
    const task = state.downloads.find((item) => item.id === episode.taskId);
    if (action === "play" && task) { playDownloadedVideo(task); return; }
    if (action === "open" && task) { await openDownloadDirectory(task); return; }
    if (action === "retry" && episode.status === "completed" && !window.confirm(`重新下载《${episode.title}》吗？原视频会在新视频验证成功后才替换。`)) return;
    setManagedEpisodesBusy(true);
    try {
      await api(`/api/downloads/${encodeURIComponent(episode.taskId)}/${action}`, { method: "POST" });
      notify(action === "retry" ? "已加入重新下载队列" : "已请求取消该集");
      await refreshManagedEpisodes();
    } catch (error) { notify(error instanceof Error ? error.message : "操作失败", "error"); }
    finally { setManagedEpisodesBusy(false); }
  }

  async function checkManagedSubscription() {
    if (!managedSubscription || managedEpisodesBusy) return;
    setManagedEpisodesBusy(true);
    try {
      await api(`/api/subscriptions/${encodeURIComponent(managedSubscription.id)}/check`, { method: "POST" });
      notify(`正在检查《${managedSubscription.title.replace(/[《》]/g, "")}》；集数列表会自动刷新`);
    } catch (error) {
      notify(error instanceof Error ? error.message : "检查订阅失败", "error");
    } finally {
      setManagedEpisodesBusy(false);
    }
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

  function toggleTaskSelection(id: string) {
    setSelectedTaskIDs((current) => current.includes(id) ? current.filter((value) => value !== id) : [...current, id]);
  }

  async function deleteDownloads(ids: string[]) {
    if (deletingTaskIDs.length || !ids.length) return;
    const selected = state.downloads.filter((task) => ids.includes(task.id));
    if (selected.length !== ids.length) {
      notify("部分任务已经不存在，请刷新后重新选择", "error");
      return;
    }
    if (selected.some((task) => task.status === "downloading")) {
      notify("请先取消正在下载的任务，等待停止后再删除", "error");
      return;
    }
    const withFiles = selected.filter((task) => !!task.filePath).length;
    const prompt = ids.length === 1
      ? `确定永久删除“${selected[0].title}”吗？${withFiles ? "\n这将同时删除电脑上保存的 MP4 文件。" : "\n这将删除下载任务记录。"}\n空的剧集目录也会自动清理；其他文件和分类根目录会保留。\n操作不可恢复。`
      : `确定批量永久删除选中的 ${ids.length} 个任务吗？\n其中 ${withFiles} 个任务记录了本地视频文件，程序会同时将这些 MP4 从硬盘删除。\n空的剧集目录也会自动清理；其他文件和分类根目录会保留。\n此操作不可恢复。`;
    if (!window.confirm(prompt)) return;
    setDeletingTaskIDs(ids);
    try {
      const result = await api<{ deleted: number; filesDeleted: number; directoriesDeleted: number }>(
        ids.length === 1 ? `/api/downloads/${encodeURIComponent(ids[0])}` : "/api/downloads/delete-batch",
        ids.length === 1 ? { method: "DELETE" } : { method: "POST", body: JSON.stringify({ ids }) },
      );
      notify(`已删除 ${result.deleted} 条任务、${result.filesDeleted} 个 MP4 文件，清理 ${result.directoriesDeleted ?? 0} 个空目录`);
      setSelectedTaskIDs((current) => current.filter((id) => !ids.includes(id)));
      await loadState(true);
    } catch (error) {
      notify(error instanceof Error ? error.message : "删除未完成，请检查报错并刷新任务列表", "error");
      await loadState(true);
    } finally {
      setDeletingTaskIDs([]);
    }
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

  function playDownloadedVideo(task: DownloadTask) {
    // Navigate the browser to the MP4 streaming endpoint. The server responds
    // with video/mp4 and Content-Disposition: inline, so the browser's built-in
    // media player handles playback; no OS player or shell command is invoked.
    const videoURL = `${API_BASE}/api/downloads/${encodeURIComponent(task.id)}/video`;
    const playerWindow = window.open(videoURL, "_blank");
    if (!playerWindow) {
      notify("浏览器阻止了播放新标签页，请允许此网站打开新标签页后重试。", "error");
      return;
    }
    try { playerWindow.opener = null; } catch { /* Cross-origin browsers may restrict opener access. */ }
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
  const existingEpisodeURLs = useMemo(() => new Set(state.downloads.filter((task) => task.status !== "cancelled" && task.status !== "failed").flatMap(taskKeys)), [state.downloads]);
  const availableEpisodes = useMemo(() => preview?.episodes.filter((episode) => !isEpisodeQueued(episode, existingEpisodeURLs)) ?? [], [preview, existingEpisodeURLs]);
  const visibleTasks = useMemo(() => taskFilter === "all" ? state.downloads : state.downloads.filter((task) => task.status === taskFilter), [state.downloads, taskFilter]);
  const selectableVisibleTasks = useMemo(() => visibleTasks.filter((task) => task.status !== "downloading"), [visibleTasks]);
  const selectedVisibleTasks = useMemo(() => selectableVisibleTasks.filter((task) => selectedTaskIDs.includes(task.id)), [selectableVisibleTasks, selectedTaskIDs]);
  const orderedCategories = useMemo(() => flattenCategories(state.categories), [state.categories]);
  const categoryNames = useMemo(() => new Map(orderedCategories.map((category) => [category.id, category.label])), [orderedCategories]);
  const pageMeta = {
    library: ["央视节目库", "直接从央视片库和栏目目录选择视频，无需复制链接"],
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
          <button className={tab === "library" ? "active" : ""} onClick={() => setTab("library")}><Tv2 size={18} /><span className="nav-label">央视片库</span></button>
          <button className={tab === "search" ? "active" : ""} onClick={() => setTab("search")}><Search size={18} /><span className="nav-label">链接与搜索</span></button>
          <button className={tab === "subscriptions" ? "active" : ""} onClick={() => setTab("subscriptions")}><BellRing size={18} /><span className="nav-label">我的订阅</span><span className="nav-count">{state.subscriptions.length}</span></button>
          <button className={tab === "downloads" ? "active" : ""} onClick={() => setTab("downloads")}><HardDriveDownload size={18} /><span className="nav-label">下载任务</span>{state.stats.downloading > 0 && <span className="nav-live">{state.stats.downloading}</span>}</button>
          <button className={tab === "settings" ? "active" : ""} onClick={() => { setTab("settings"); setSettingsDraft(state.settings); }}><Settings size={18} /><span className="nav-label">系统设置</span></button>
        </nav>
        <div className="sidebar-card">
          <span className={`status-dot ${connected ? "online" : ""}`} />
          <div><b>{connected ? "服务运行中" : connected === null ? "正在连接" : "后台未连接"}</b><small>{connected ? `每 ${state.settings.pollIntervalMinutes} 分钟检查更新` : "请启动 Go 后台服务"}</small></div>
        </div>
        <div className="sidebar-foot" title={`当前后台版本：${backendRevision}`}><Sparkles size={15} /><span>服务版本 {backendRevision}</span></div>
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

        {tab === "library" && (
          <section className="page-content library-page">
            <div className="library-intro">
              <div><span className="library-kicker"><Tv2 size={15} />央视网节目库</span>
                <h2>打开就能逛 CCTV，<em>选中即可下载。</em></h2>
                <p>直接读取央视节目目录和栏目视频列表。先选分类，再选节目，勾选需要的剧集或期数。无需复制链接，也无需先订阅。</p>
              </div>
              <button className="library-link-button" onClick={() => setTab("search")}><Search size={16} />链接下载 / 搜索</button>
            </div>
            <div className="library-category-tabs" role="group" aria-label="央视节目分类">
              {LIBRARY_CATEGORIES.map((category) => <button type="button" key={category.id} className={libraryKind === category.id ? "active" : ""} aria-pressed={libraryKind === category.id}
                onClick={() => { setLibraryKind(category.id); setLibraryPage(1); }}>
                <strong>{category.name}</strong><small>{category.description}</small>
              </button>)}
            </div>
            {libraryKind === "columns" && <div className="library-filter-bar">
              <label htmlFor="cctv-library-channel">按电视频道查看栏目</label>
              <select id="cctv-library-channel" value={libraryChannel} onChange={(event) => { setLibraryChannel(event.target.value); setLibraryPage(1); }}>
                {CCTV_CHANNELS.map(([id, name]) => <option key={id} value={id}>{name}</option>)}
              </select>
              <span>以下是栏目点播视频，不是电视直播。</span>
            </div>}
            <div className="section-heading compact">
              <div><h3>{LIBRARY_CATEGORIES.find((category) => category.id === libraryKind)?.name} · 央视节目</h3>
                <p>{libraryLoading ? "正在读取央视目录…" : libraryError ? "目录暂时不可用" : `第 ${libraryPage} 页 · ${libraryResult.programs.length} 个节目`}</p></div>
              <button className="ghost-small" onClick={() => setLibraryReload((value) => value + 1)} disabled={libraryLoading}>
                {libraryLoading ? <LoaderCircle size={16} className="spin" /> : <RefreshCw size={16} />}刷新列表
              </button>
            </div>
            {libraryLoading ? <div className="library-program-grid">{Array.from({ length: 8 }, (_, index) => <div key={index} className="library-program-card library-loading"><div className="library-poster-skeleton" /><div className="library-card-skeleton" /></div>)}</div>
              : libraryError ? <div className="empty-state large"><WifiOff size={28} /><h3>暂时无法读取央视节目库</h3><p>{libraryError}</p><button className="primary-small" onClick={() => setLibraryReload((value) => value + 1)}><RefreshCw size={16} />重新加载</button></div>
              : libraryResult.programs.length ? <div className="library-program-grid">{libraryResult.programs.map((program) =>
                <article className="library-program-card" key={`${program.kind}-${program.id}`}>
                  <div className="library-program-poster">{program.poster ? <img src={program.poster} alt="" loading="lazy" /> : <Tv2 size={30} />}<span>CCTV</span></div>
                  <div className="library-program-info"><h4 title={program.title}>{program.title}</h4><p title={program.description}>{program.description || "来自央视网节目库"}</p>
                    <div className="library-program-actions">
                      <button type="button" className="library-browse-button" onClick={() => openLibraryProgram(program)} disabled={resolvingLibraryID !== null}>
                        {resolvingLibraryID === program.id ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}
                        {resolvingLibraryID === program.id ? "读取选集…" : "查看选集"}
                      </button>
                      {(() => {
                        const subscribed = !!program.sourceUrl && state.subscriptions.some((item) => subscriptionSourceKey(item.sourceUrl) === subscriptionSourceKey(program.sourceUrl));
                        return <button
                          type="button"
                          className={`library-subscribe-button ${subscribed ? "subscribed" : ""}`}
                          title={subscribed ? "已订阅，点击管理每集下载" : program.sourceUrl ? "订阅该节目，自动追更并加入订阅管理" : "央视目录没有提供节目主页链接，暂时无法直接订阅"}
                          onClick={() => startLibrarySubscription(program)}
                          disabled={!program.sourceUrl}
                        >
                          {subscribed ? <Check size={15} /> : <BellRing size={15} />}
                          {subscribed ? "已订阅" : program.sourceUrl ? "订阅追更" : "暂不可订阅"}
                        </button>;
                      })()}
                    </div>
                  </div>
                </article>)}</div>
              : <div className="empty-state large"><Tv2 size={30} /><h3>这一页暂时没有节目</h3><p>可以切换分类、频道，或返回上一页。</p></div>}
            {!libraryLoading && !libraryError && <div className="library-pagination">
              <button disabled={libraryPage <= 1} onClick={() => setLibraryPage((page) => Math.max(1, page - 1))}>上一页</button>
              <span>第 {libraryPage} 页</span>
              <button disabled={!libraryResult.hasMore || libraryPage >= 200} onClick={() => setLibraryPage((page) => page + 1)}>下一页</button>
            </div>}
          </section>
        )}

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
                    <div className="result-actions"><button className="subscribe-button direct-download" disabled={resolvingURL === result.sourceUrl} onClick={() => openEpisodePicker(result.sourceUrl)}>{resolvingURL === result.sourceUrl ? <LoaderCircle className="spin" size={16} /> : <Download size={16} />}{resolvingURL === result.sourceUrl ? "正在解析" : result.isSingle ? "下载本期" : "选集下载"}</button>{!result.isSingle && <button className={`follow-button ${subscribed ? "subscribed" : ""}`} disabled={subscribed} onClick={() => { setAdding(result); setDownloadExisting(false); setSelectedCategoryID(orderedCategories[0]?.id ?? ""); }}>{subscribed ? <Check size={14} /> : <BellRing size={14} />}{subscribed ? "已订阅" : "订阅追更"}</button>}<a href={result.sourceUrl} target="_blank" rel="noreferrer" title="在央视网查看"><ChevronRight size={19} /></a></div>
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
            <div className="subscription-info" role="button" tabIndex={0} title="点击查看每集下载状态并管理" style={{ cursor: "pointer" }} onClick={() => openSubscriptionManager(sub)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); openSubscriptionManager(sub); } }}><div className="subscription-title"><h3>{sub.title}</h3><span className={`sub-state ${sub.status}`}>{sub.status === "active" ? "订阅中" : "已暂停"}</span></div><p>{sub.description}</p><div className="subscription-meta"><span>{sub.year || "年份未知"}</span><i /><span>{sub.episodeCount} 集</span><i /><span>{categoryNames.get(sub.storageCategoryId) || "未分类"}</span><i /><span>上次检查 {timeAgo(sub.lastCheckedAt)}</span></div>{sub.lastError && <div className="inline-error">{sub.lastError}</div>}</div>
            <div className="subscription-progress"><div><span>下载进度</span><b>{sub.downloadedCount} / {sub.episodeCount}</b></div><div className="progress-track"><span style={{ width: `${sub.episodeCount ? sub.downloadedCount / sub.episodeCount * 100 : 0}%` }} /></div></div>
            <div className="subscription-actions"><button className="manage-episodes-button" title="查看待下载、已完成状态并管理每一集" onClick={() => openSubscriptionManager(sub)}><ListChecks size={16} /><span>管理下载</span></button><button title="选集下载" onClick={() => openEpisodePicker(sub.sourceUrl)} disabled={resolvingURL === sub.sourceUrl}>{resolvingURL === sub.sourceUrl ? <LoaderCircle className="spin" size={17} /> : <Download size={17} />}</button><button title="立即检查" onClick={() => checkSubscription(sub)}><RefreshCw size={17} /></button><button title={sub.status === "active" ? "暂停订阅" : "恢复订阅"} onClick={() => updateSubscription(sub, { status: sub.status === "active" ? "paused" : "active" })}>{sub.status === "active" ? <Pause size={17} /> : <Play size={17} />}</button><button title="更多操作" onClick={() => setMenuOpen(menuOpen === sub.id ? null : sub.id)}><MoreHorizontal size={18} /></button>{menuOpen === sub.id && <div className="context-menu"><label className="context-category">保存分类<select value={sub.storageCategoryId} onChange={async (event) => { await updateSubscription(sub, { storageCategoryId: event.target.value }); setMenuOpen(null); }}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select></label><button onClick={() => updateSubscription(sub, { autoDownload: !sub.autoDownload })}>{sub.autoDownload ? <CirclePause size={15} /> : <Download size={15} />}{sub.autoDownload ? "关闭自动下载" : "开启自动下载"}</button><button className="danger" onClick={() => removeSubscription(sub)}><Trash2 size={15} />删除订阅</button></div>}</div>
          </article>)}</div> : <div className="empty-state large"><BellRing size={32} /><h3>还没有订阅</h3><p>先去搜索喜欢的节目，更新后会自动出现在下载队列。</p><button className="primary-small" onClick={() => setTab("search")}><Search size={16} />发现剧集</button></div>}
        </section>}

        {tab === "downloads" && <section className="page-content">
          <div className={`download-summary ${state.settings.downloadsPaused ? "paused" : ""}`}><div className="summary-orb">{state.settings.downloadsPaused ? <CirclePause size={28} /> : <Gauge size={28} />}</div><div><span>{state.settings.downloadsPaused ? "全局状态" : "当前下载"}</span><strong>{state.settings.downloadsPaused ? state.stats.paused : state.stats.downloading}</strong><small>{state.settings.downloadsPaused ? "任务已安全暂停" : `并发上限 ${state.settings.concurrentDownloads}`}</small></div><div className="summary-divider" /><div><span>排队等待</span><strong>{state.stats.queued}</strong><small>{state.settings.downloadsPaused ? "恢复后自动继续" : "自动按加入顺序处理"}</small></div><div className="summary-divider" /><div><span>累计完成</span><strong>{state.stats.completed}</strong><small>{state.stats.failed ? `${state.stats.failed} 个任务失败` : "任务状态正常"}</small></div></div>
          <div className="task-toolbar"><div className="filter-tabs">{[["all","全部"],["downloading","下载中"],["queued","等待中"],["paused","已暂停"],["completed","已完成"],["failed","失败"]].map(([key,label]) => <button className={taskFilter === key ? "active" : ""} key={key} onClick={() => { setTaskFilter(key); setSelectedTaskIDs([]); }}>{label}</button>)}</div><div className="toolbar-actions"><button className="ghost-small task-select-all" disabled={!selectableVisibleTasks.length || deletingTaskIDs.length > 0} onClick={() => setSelectedTaskIDs(selectedVisibleTasks.length === selectableVisibleTasks.length ? [] : selectableVisibleTasks.map((task) => task.id))}><Check size={15} />{selectedVisibleTasks.length === selectableVisibleTasks.length && selectableVisibleTasks.length ? "取消全选" : "全选本页"}</button>{selectedVisibleTasks.length > 0 && <button className="task-bulk-delete" disabled={deletingTaskIDs.length > 0} onClick={() => deleteDownloads(selectedVisibleTasks.map((task) => task.id))}>{deletingTaskIDs.length > 0 ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}删除选中（{selectedVisibleTasks.length}）</button>}<button className={`bulk-control ${state.settings.downloadsPaused ? "resume" : ""}`} disabled={bulkBusy} onClick={toggleAllDownloads}>{bulkBusy ? <LoaderCircle className="spin" size={15} /> : state.settings.downloadsPaused ? <Play size={15} /> : <Pause size={15} />}{state.settings.downloadsPaused ? "全部恢复" : "全部暂停"}</button><button className="ghost-small" onClick={() => loadState()}><RefreshCw size={15} />刷新</button></div></div>
          {visibleTasks.length ? <div className="task-list">{visibleTasks.map((task) => <article className={`task-row ${selectedTaskIDs.includes(task.id) ? "selected-for-delete" : ""}`} key={task.id}><label className="task-select-box" title={task.status === "downloading" ? "请先取消下载，等待停止后再选择删除" : "选择此任务以批量删除"}><input type="checkbox" aria-label={`选择删除 ${task.title}`} checked={selectedTaskIDs.includes(task.id)} disabled={task.status === "downloading" || deletingTaskIDs.length > 0} onChange={() => toggleTaskSelection(task.id)} /></label><div className="task-thumb"><img src={task.poster} alt="" />{task.status === "completed" && <span><Check size={14} /></span>}</div><div className="task-main"><div className="task-heading"><div><h3>{task.title}</h3><p>{task.seriesTitle}</p></div><span className={`task-status ${task.status}`}>{task.status === "downloading" && <LoaderCircle className="spin" size={13} />}{statusText(task.status)}</span></div>
            {(task.status === "downloading" || task.status === "queued" || task.status === "paused") && <div className="task-progress"><div className="progress-track"><span style={{ width: `${task.progress}%` }} /></div><div><b>{task.status === "queued" ? "等待可用下载槽" : task.status === "paused" ? "已暂停，恢复后重新开始" : `${Math.round(task.progress)}%`}</b><span>{task.speed || (task.status === "queued" || task.status === "paused" ? "" : "正在连接资源…")}</span></div></div>}
            {task.status === "failed" && <div className="task-error">{task.error}</div>}{task.status === "completed" && <div className="task-file"><FolderOpen size={14} /><span title={task.filePath}>{task.filePath}</span>{task.videoQuality && <b title="实际保存的视频画质"> · {task.videoQuality}</b>}</div>}
          </div><div className="task-actions">{task.status === "completed" && task.filePath && <><button className="retry" title="重新下载并修复之前损坏的视频" onClick={() => taskAction(task, "retry")}><RotateCcw size={15} />重新下载</button><button className="open-folder" title="在运行程序的电脑上打开保存目录" disabled={openingDirectoryID === task.id} onClick={() => openDownloadDirectory(task)}>{openingDirectoryID === task.id ? <LoaderCircle className="spin" size={15} /> : <FolderOpen size={15} />}打开目录</button><button className="play-video" title="在浏览器新标签页播放，不调用系统播放器" onClick={() => playDownloadedVideo(task)}><Play size={15} />网页播放</button></>}{(task.status === "queued" || task.status === "downloading" || task.status === "paused") && <button title="取消任务" onClick={() => taskAction(task, "cancel")}><X size={17} /></button>}{(task.status === "failed" || task.status === "cancelled") && <button className="retry" onClick={() => taskAction(task, "retry")}><RotateCcw size={15} />重试</button>}<button className="task-delete" title={task.status === "downloading" ? "请先取消并停止下载" : "永久删除下载记录及本地视频文件"} disabled={task.status === "downloading" || deletingTaskIDs.length > 0} onClick={() => deleteDownloads([task.id])}>{deletingTaskIDs.includes(task.id) ? <LoaderCircle size={15} className="spin" /> : <Trash2 size={15} />}删除</button></div></article>)}</div> : <div className="empty-state large"><HardDriveDownload size={32} /><h3>这里暂时没有任务</h3><p>去“选集下载”粘贴链接并勾选集数，或订阅节目自动下载。</p></div>}
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

      {managedSubscription && <div className="modal-backdrop subscription-episodes-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !managedEpisodesBusy) setManagedSubscription(null); }}>
        <section className="subscription-episodes-modal" role="dialog" aria-modal="true" aria-labelledby="subscription-episodes-title">
          <header className="subscription-episodes-header"><img src={managedSubscription.poster} alt="" /><div><span className="library-kicker"><ListChecks size={15} />订阅集数管理</span><h2 id="subscription-episodes-title">{managedSubscription.title}</h2><div className="subscription-episodes-summary"><span><b>{managedEpisodes.length}</b> 已发现</span><span className="pending"><b>{managedEpisodes.filter((e) => e.status === "not_downloaded").length}</b> 待下载</span><span className="active"><b>{managedEpisodes.filter((e) => ["queued", "downloading", "paused"].includes(e.status)).length}</b> 处理中</span><span className="done"><b>{managedEpisodes.filter((e) => e.status === "completed").length}</b> 已完成</span><span className="failed"><b>{managedEpisodes.filter((e) => e.status === "failed").length}</b> 失败</span></div></div><button className="episode-modal-close" title="关闭" onClick={() => setManagedSubscription(null)} disabled={managedEpisodesBusy}><X size={19} /></button></header>
          <div className="subscription-episodes-toolbar"><div className="filter-tabs">{[["all","全部"],["not_downloaded","待下载"],["queued","等待中"],["downloading","下载中"],["paused","已暂停"],["completed","已完成"],["failed","失败"],["cancelled","已取消"]].map(([key,label]) => <button key={key} className={managedEpisodeFilter === key ? "active" : ""} onClick={() => { setManagedEpisodeFilter(key); setSelectedManagedEpisodeIDs([]); }}>{label}</button>)}</div>
            <div className="subscription-episodes-bulk"><span className="managed-selection-count">已选 {selectedManagedEpisodeIDs.length} 集</span><button className="ghost-small" disabled={managedEpisodesLoading || managedEpisodesBusy} onClick={() => { const rows = managedEpisodes.filter((e) => managedEpisodeFilter === "all" || e.status === managedEpisodeFilter); setSelectedManagedEpisodeIDs(selectedManagedEpisodeIDs.length === rows.length ? [] : rows.map((e) => e.id)); }}><Check size={14} />全选当前筛选</button><button className="ghost-small" disabled={managedEpisodesLoading || managedEpisodesBusy} onClick={checkManagedSubscription}><RefreshCw size={14} />检查新集</button><button className="ghost-small" disabled={managedEpisodesLoading || managedEpisodesBusy} onClick={refreshManagedEpisodes}><RefreshCw size={14} />刷新状态</button><button className="primary-small" disabled={!selectedManagedEpisodeIDs.length || managedEpisodesBusy} onClick={() => queueManagedEpisodes(selectedManagedEpisodeIDs)}>{managedEpisodesBusy ? <LoaderCircle className="spin" size={14} /> : <Download size={14} />}下载/重试所选</button><button className="managed-cancel-selected" disabled={!selectedManagedEpisodeIDs.some((id) => { const e = managedEpisodes.find((row) => row.id === id); return !!e?.taskId && ["queued","downloading","paused"].includes(e.status); }) || managedEpisodesBusy} onClick={() => cancelManagedEpisodes(selectedManagedEpisodeIDs)}><X size={14}/>取消所选</button><button className="managed-delete-selected" disabled={!selectedManagedEpisodeIDs.some((id) => { const e = managedEpisodes.find((row) => row.id === id); return !!e?.taskId && ["completed","failed","cancelled"].includes(e.status); }) || managedEpisodesBusy} onClick={() => deleteManagedEpisodes(selectedManagedEpisodeIDs)}><Trash2 size={14}/>删除所选任务</button></div>
          </div>
          {managedEpisodesLoading ? <div className="subscription-episodes-loading"><LoaderCircle className="spin" size={22} />正在读取每集状态…</div> :
          <div className="subscription-episodes-table-wrap"><table className="subscription-episodes-table"><thead><tr><th>选择</th><th>集数</th><th>视频标题</th><th>状态</th><th>进度 / 结果</th><th>操作</th></tr></thead><tbody>
            {managedEpisodes.filter((e) => managedEpisodeFilter === "all" || e.status === managedEpisodeFilter).map((episode) => <tr key={episode.id}>
              <td><input type="checkbox" checked={selectedManagedEpisodeIDs.includes(episode.id)} disabled={managedEpisodesBusy} onChange={() => toggleManagedEpisode(episode.id)} aria-label={`选择第 ${episode.number} 集`} /></td>
              <td>{episode.number || "—"}</td><td><div className="managed-episode-title">{episode.title}</div>{episode.publishedAt && <small>{episode.publishedAt}</small>}</td>
              <td><span className={`managed-episode-status ${episode.status}`}>{episode.status === "not_downloaded" ? "待下载" : statusText(episode.status as DownloadTask["status"])}</span></td>
              <td>{["downloading","queued","paused"].includes(episode.status) ? <><div className="progress-track"><span style={{width:`${episode.progress || 0}%`}} /></div><small>{episode.status === "queued" ? "等待下载槽" : episode.status === "paused" ? "已暂停" : `${Math.round(episode.progress || 0)}% ${episode.speed || ""}`}</small></> : episode.status === "completed" ? <small className="managed-episode-path" title={episode.filePath}>{episode.filePath || "已完成"}</small> : episode.status === "failed" ? <small className="managed-episode-error" title={episode.error}>{episode.error || "下载失败"}</small> : <small>—</small>}</td>
              <td><div className="managed-episode-actions">{episode.status === "not_downloaded" ? <button disabled={managedEpisodesBusy} onClick={() => queueManagedEpisodes([episode.id])}><Download size={14}/>下载</button> : ["completed","failed","cancelled"].includes(episode.status) ? <button disabled={managedEpisodesBusy} onClick={() => episode.taskId ? manageEpisodeAction(episode,"retry") : queueManagedEpisodes([episode.id])}><RotateCcw size={14}/>{episode.status === "completed" ? "重新下载" : "重试"}</button> : episode.taskId ? <button disabled={managedEpisodesBusy} onClick={() => manageEpisodeAction(episode,"cancel")}><X size={14}/>取消</button> : null}
              {episode.status === "completed" && episode.taskId && <><button disabled={managedEpisodesBusy} onClick={() => manageEpisodeAction(episode,"play")}><Play size={14}/>播放</button><button disabled={managedEpisodesBusy} onClick={() => manageEpisodeAction(episode,"open")}><FolderOpen size={14}/>目录</button></>}{episode.taskId && ["completed","failed","cancelled"].includes(episode.status) && <button className="managed-row-delete" disabled={managedEpisodesBusy} onClick={() => deleteManagedEpisodes([episode.id])}><Trash2 size={14}/>删除</button>}</div></td>
            </tr>)}
            {!managedEpisodes.filter((e) => managedEpisodeFilter === "all" || e.status === managedEpisodeFilter).length && <tr><td colSpan={6} className="managed-episodes-empty">没有符合条件的集数。可以点击上方“检查新集”重新读取央视目录。</td></tr>}
          </tbody></table></div>}
          <footer className="subscription-episodes-footer"><span>已完成集重新下载时，原视频会在新文件验证成功后替换。</span><button className="cancel-button" onClick={() => setManagedSubscription(null)} disabled={managedEpisodesBusy}>关闭</button></footer>
        </section>
      </div>}
      {preview && <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !queueBusy && !episodePageLoading) setPreview(null); }}>
        <div className="episode-modal" role="dialog" aria-modal="true" aria-labelledby="episode-picker-title">
          <header className="episode-modal-header">
            {preview.poster ? <img src={preview.poster} alt="" /> : <span className="episode-poster-placeholder"><Tv2 size={26} /></span>}
            <div className="episode-modal-title">
              <span className="modal-kicker"><Download size={14} />{preview.isSingle ? "单集直接下载" : "选择要下载的剧集"}</span>
              <h2 id="episode-picker-title">{preview.title}</h2>
              <p>{preview.libraryKind ? `第 ${preview.libraryPage} 页：本页 ${preview.episodes.length} 条${preview.libraryTotal ? `，央视返回共 ${preview.libraryTotal} 条` : ""}` : `${preview.episodes.length} 集可选`} · 下载后可以直接播放</p>
            </div>
            <button className="episode-modal-close" aria-label="关闭选集" title="关闭" onClick={() => setPreview(null)} disabled={queueBusy}><X size={19} /></button>
          </header>
          <div className="episode-picker-toolbar">
            <strong>选集列表 <span>已选 {selectedEpisodeURLs.length} / {preview.episodes.length}</span></strong>
            {!preview.isSingle && <div>
              <button onClick={() => setSelectedEpisodeURLs(availableEpisodes.slice(0, 200).map(episodeKey))}>全选{availableEpisodes.length > 200 ? "前 200 集" : ""}</button>
              <button onClick={() => setSelectedEpisodeURLs(availableEpisodes.length ? [episodeKey(preview.libraryKind === "columns" ? availableEpisodes[0] : availableEpisodes[availableEpisodes.length - 1])] : [])}>只选最新</button>
              <button onClick={() => setSelectedEpisodeURLs([])}>清空</button>
            </div>}
          </div>
          {preview.libraryKind && <div className="episode-page-controls">
            <span>第 {preview.libraryPage} 页 · 每页最多 100 条</span>
            <button disabled={episodePageLoading || queueBusy || (preview.libraryPage ?? 1) <= 1} onClick={() => changeLibraryEpisodePage((preview.libraryPage ?? 1) - 1)}>上一页</button>
            <button disabled={episodePageLoading || queueBusy || !preview.libraryHasMore || (preview.libraryPage ?? 1) >= 200} onClick={() => changeLibraryEpisodePage((preview.libraryPage ?? 1) + 1)}>下一页</button>
            {episodePageLoading && <LoaderCircle className="spin" size={16} />}
          </div>}
          <div className="episode-picker-list">
            {preview.episodes.length === 0 && <div className="episode-empty-state">
              <strong>央视暂时未返回可下载的视频</strong>
              <p>已尝试不同视频列表模式与后续页码。如果央视网页能播放，请通过“链接与搜索”粘贴该节目链接再试；这不代表视频已经下架。</p>
              {preview.sourceUrl && <a href={preview.sourceUrl} target="_blank" rel="noreferrer">在央视网页检查节目</a>}
            </div>}
            {preview.episodes.map((episode) => {
              const exists = isEpisodeQueued(episode, existingEpisodeURLs);
              const selected = selectedEpisodeURLs.includes(episodeKey(episode));
              return <label key={episodeKey(episode)} className={`episode-picker-item ${selected ? "selected" : ""} ${exists ? "already-queued" : ""}`}>
                <input type="checkbox" checked={selected} disabled={exists || queueBusy || episodePageLoading} onChange={() => toggleEpisode(episodeKey(episode))} />
                <span className="episode-index">{episode.number}</span>
                <span className="episode-title">{episode.title}{episode.publishedAt && <small>{episode.publishedAt}</small>}</span>
                {exists ? <span className="episode-exists"><Check size={13} />已加入</span> : selected ? <Check size={17} className="episode-checked" /> : null}
              </label>;
            })}
          </div>
          <footer className="episode-picker-footer">
            <label>保存到 <select value={selectedCategoryID} onChange={(event) => setSelectedCategoryID(event.target.value)} disabled={queueBusy}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select></label>
            <span className="episode-picker-path">{state.categories.find((category) => category.id === selectedCategoryID)?.downloadPath || "尚未设置保存目录"}</span>
            <div className="episode-picker-buttons"><button className="cancel-button" onClick={() => setPreview(null)} disabled={queueBusy}>取消</button><button className="confirm-button" disabled={queueBusy || episodePageLoading || !selectedCategoryID || selectedEpisodeURLs.length === 0} onClick={queueSelectedEpisodes}>{queueBusy ? <LoaderCircle className="spin" size={17} /> : <Download size={17} />}{queueBusy ? "正在加入队列" : `下载选中 ${selectedEpisodeURLs.length} 集`}</button></div>
          </footer>
        </div>
      </div>}
      {adding && <div className="modal-backdrop"><div className="add-modal" role="dialog" aria-modal="true" aria-labelledby="add-title"><button className="modal-close" onClick={() => setAdding(null)} disabled={addBusy}><X size={18} /></button><img src={adding.poster} alt="" /><div className="modal-copy"><span className="modal-kicker"><BellRing size={14} />新订阅</span><h2 id="add-title">{adding.title}</h2><p>订阅后系统会每 {state.settings.pollIntervalMinutes} 分钟检查一次，新剧集将自动进入下载队列。</p><label className="modal-category">保存分类<select value={selectedCategoryID} onChange={(event) => setSelectedCategoryID(event.target.value)}>{orderedCategories.map((category) => <option key={category.id} value={category.id}>{category.label}</option>)}</select><small>{state.categories.find((category) => category.id === selectedCategoryID)?.downloadPath || "请先在设置中创建分类"}</small></label><div className="check-row"><input id="download-existing" type="checkbox" checked={downloadExisting} onChange={(event) => setDownloadExisting(event.target.checked)} /><label htmlFor="download-existing"><b>同时下载已有剧集</b><small>{adding.episodeCount ? `将加入 ${adding.episodeCount} 个下载任务` : "已有选集会加入下载队列"}</small></label></div><div className="modal-actions"><button className="cancel-button" onClick={() => setAdding(null)} disabled={addBusy}>取消</button><button className="confirm-button" onClick={confirmAdd} disabled={addBusy || !selectedCategoryID}>{addBusy ? <LoaderCircle className="spin" size={17} /> : <BellRing size={17} />}{addBusy ? "正在添加" : "确认订阅"}</button></div></div></div></div>}

      {toast && <div className={`toast ${toast.type}`}><span>{toast.type === "ok" ? <CheckCircle2 size={18} /> : <X size={18} />}</span>{toast.text}</div>}
    </div>
  );
}

function Stat({ icon, label, value, tone }: { icon: React.ReactNode; label: string; value: string | number; tone: string }) {
  return <div className="stat-card"><span className={`stat-icon ${tone}`}>{icon}</span><div><small>{label}</small><strong>{value}</strong></div></div>;
}

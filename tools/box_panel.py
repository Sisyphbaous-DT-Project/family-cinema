from __future__ import annotations

"""机顶盒媒体服务 · 控制面板

把日常运维的操作做成一键按钮：启动/停止/重启服务、查看状态与日志、
重新扫描媒体库、清理下载缓存、重新挂载硬盘。
只依赖标准库 tkinter，双击 start_box_panel.bat 即可运行。
"""

import json
import os
import queue
import re
import shutil
import subprocess
import threading
import time
import tkinter as tk
import urllib.error
import urllib.request
from pathlib import Path
from tkinter import ttk

# 默认使用文档占位地址，可在界面或环境变量中改成自己的盒子地址。
DEFAULT_ADB_TARGET = os.environ.get("BOX_ADB", "box.local:30016")
DEFAULT_HTTP_HOST = os.environ.get("BOX_HOST", "box.local")
HTTP_PORT = 8088
RESCUE_PORT = 8089
MEDIACTL = "/data/server/bin/mediactl"
MEDIA_MOUNT = "/mnt/sda/sda1"
CACHE_DIR = f"{MEDIA_MOUNT}/.torrent-cache"

# ADB 命令统一超时（秒）。挂载/扫描类操作偏慢，单独给更长的值
TIMEOUT_QUICK = 20
TIMEOUT_SLOW = 90


class BoxPanel:
    def __init__(self, root: tk.Tk) -> None:
        self.root = root
        self.root.title("机顶盒媒体服务 · 控制面板")
        self.root.geometry("880x620")
        self.root.minsize(720, 520)

        self.adb_path = shutil.which("adb")
        self.target_var = tk.StringVar(value=DEFAULT_ADB_TARGET)
        self.host_var = tk.StringVar(value=DEFAULT_HTTP_HOST)
        self.status_var = tk.StringVar(value="就绪")
        self.svc_var = tk.StringVar(value="服务状态：未检查")
        self.lib_var = tk.StringVar(value="媒体库：未检查")

        # 后台线程把输出塞进队列，主线程定时取出来写进日志框
        self.log_q: queue.Queue[str] = queue.Queue()
        self.busy = False

        self._build_ui()
        self.root.after(120, self._drain_log)

        if not self.adb_path:
            self._log("找不到 adb，请确认已安装并加入 PATH。")
            self.status_var.set("缺少 adb")
        else:
            self._log(f"adb: {self.adb_path}")
            self.refresh_status()

    # ---------- 界面 ----------
    def _build_ui(self) -> None:
        pad = {"padx": 6, "pady": 4}

        top = ttk.Frame(self.root)
        top.pack(fill="x", **pad)
        ttk.Label(top, text="ADB 地址").pack(side="left")
        ttk.Entry(top, textvariable=self.target_var, width=22).pack(side="left", padx=(4, 12))
        ttk.Label(top, text="网页地址").pack(side="left")
        ttk.Entry(top, textvariable=self.host_var, width=16).pack(side="left", padx=(4, 12))
        ttk.Button(top, text="刷新状态", command=self.refresh_status).pack(side="left")
        ttk.Button(top, text="打开网页", command=self.open_web).pack(side="left", padx=4)

        info = ttk.LabelFrame(self.root, text="当前状态")
        info.pack(fill="x", **pad)
        ttk.Label(info, textvariable=self.svc_var).pack(anchor="w", padx=8, pady=(6, 2))
        ttk.Label(info, textvariable=self.lib_var).pack(anchor="w", padx=8, pady=(0, 2))
        diag_row = ttk.Frame(info)
        diag_row.pack(fill="x", padx=6, pady=(0, 6))
        self._btn(diag_row, "一键体检", self.diagnose)
        ttk.Label(diag_row, text="出问题点它：会告诉你哪里坏了、该按哪个按钮",
                  foreground="#666").pack(side="left", padx=8)

        # 服务相关
        svc = ttk.LabelFrame(self.root, text="服务")
        svc.pack(fill="x", **pad)
        row = ttk.Frame(svc)
        row.pack(fill="x", padx=6, pady=6)
        self._btn(row, "启动服务", self.svc_start)
        self._btn(row, "重启服务", self.svc_restart)
        self._btn(row, "停止服务", self.svc_stop)
        self._btn(row, "查看日志", self.svc_logs)

        # IP 漂移自救：找到盒子 + 挂回老地址
        ipfix = ttk.Frame(svc)
        ipfix.pack(fill="x", padx=6, pady=(0, 6))
        self._btn(ipfix, "找到盒子（IP漂了用）", self.ip_find)
        self._btn(ipfix, f"恢复别名（{DEFAULT_HTTP_HOST}）", self.ip_fix)

        # 媒体库与硬盘
        media = ttk.LabelFrame(self.root, text="媒体库与硬盘")
        media.pack(fill="x", **pad)
        row2 = ttk.Frame(media)
        row2.pack(fill="x", padx=6, pady=6)
        self._btn(row2, "重新扫描媒体库", self.lib_scan)
        self._btn(row2, "检查硬盘挂载", self.disk_check)
        self._btn(row2, "拔插硬盘后修复", self.disk_recover)
        self._btn(row2, "查看磁盘占用", self.disk_usage)

        # 下载与缓存
        dl = ttk.LabelFrame(self.root, text="下载与缓存")
        dl.pack(fill="x", **pad)
        row3 = ttk.Frame(dl)
        row3.pack(fill="x", padx=6, pady=6)
        self._btn(row3, "查看下载任务", self.tasks_list)
        self._btn(row3, "查看缓存占用", self.cache_list)
        self._btn(row3, "清空下载缓存", self.cache_clear)

        # 救援服务（8089，手机工具箱）
        rescue = ttk.LabelFrame(self.root, text="救援服务（手机工具箱 8089）")
        rescue.pack(fill="x", **pad)
        row4 = ttk.Frame(rescue)
        row4.pack(fill="x", padx=6, pady=6)
        self._btn(row4, "启动救援服务", self.rescue_start)
        self._btn(row4, "重启救援服务", self.rescue_restart)
        self._btn(row4, "停止救援服务", self.rescue_stop)
        self._btn(row4, "打开手机工具箱", self.open_rescue)

        # 日志区
        logf = ttk.LabelFrame(self.root, text="输出")
        logf.pack(fill="both", expand=True, **pad)
        wrap = ttk.Frame(logf)
        wrap.pack(fill="both", expand=True, padx=6, pady=6)
        self.log = tk.Text(wrap, height=14, wrap="none", state="disabled",
                           bg="#1e1e1e", fg="#d8d8d8", insertbackground="#d8d8d8")
        ysb = ttk.Scrollbar(wrap, orient="vertical", command=self.log.yview)
        xsb = ttk.Scrollbar(wrap, orient="horizontal", command=self.log.xview)
        self.log.configure(yscrollcommand=ysb.set, xscrollcommand=xsb.set)
        self.log.grid(row=0, column=0, sticky="nsew")
        ysb.grid(row=0, column=1, sticky="ns")
        xsb.grid(row=1, column=0, sticky="ew")
        wrap.rowconfigure(0, weight=1)
        wrap.columnconfigure(0, weight=1)

        bottom = ttk.Frame(self.root)
        bottom.pack(fill="x", **pad)
        ttk.Label(bottom, textvariable=self.status_var).pack(side="left")
        ttk.Button(bottom, text="清空输出", command=self._clear_log).pack(side="right")

    def _btn(self, parent: ttk.Frame, text: str, cmd) -> None:
        ttk.Button(parent, text=text, command=cmd, width=16).pack(side="left", padx=3)

    # ---------- 日志 ----------
    def _log(self, msg: str) -> None:
        self.log_q.put(msg)

    def _drain_log(self) -> None:
        wrote = False
        try:
            while True:
                msg = self.log_q.get_nowait()
                if not wrote:
                    self.log.configure(state="normal")
                    wrote = True
                self.log.insert("end", msg.rstrip() + "\n")
        except queue.Empty:
            pass
        if wrote:
            self.log.see("end")
            self.log.configure(state="disabled")
        self.root.after(120, self._drain_log)

    def _clear_log(self) -> None:
        self.log.configure(state="normal")
        self.log.delete("1.0", "end")
        self.log.configure(state="disabled")

    # ---------- 执行封装 ----------
    def _adb(self, shell_cmd: str, timeout: int = TIMEOUT_QUICK) -> tuple[bool, str]:
        """在盒子上执行一条 shell 命令，返回 (是否成功, 输出)。"""
        if not self.adb_path:
            return False, "未找到 adb"
        target = self.target_var.get().strip()
        cmd = [self.adb_path, "-s", target, "shell", shell_cmd]
        try:
            p = subprocess.run(cmd, capture_output=True, timeout=timeout,
                               creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        except subprocess.TimeoutExpired:
            return False, f"命令超时（{timeout}s）"
        except OSError as e:
            return False, f"执行失败：{e}"
        out = (p.stdout or b"").decode("utf-8", "replace")
        err = (p.stderr or b"").decode("utf-8", "replace").strip()
        if err:
            out = (out + "\n" + err) if out.strip() else err
        return p.returncode == 0, out.strip()

    def _http(self, path: str, timeout: int = 15, post: bool = False, port: int = HTTP_PORT):
        """请求盒子上的 HTTP 接口，返回解析后的 JSON（失败抛异常）。"""
        url = f"http://{self.host_var.get().strip()}:{port}{path}"
        req = urllib.request.Request(url, method="POST" if post else "GET")
        if post:
            req.data = b"{}"
            req.add_header("Content-Type", "application/json")
        # 不走系统代理，避免内网地址被代理拦掉
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(req, timeout=timeout) as r:
            body = r.read().decode("utf-8", "replace")
        return json.loads(body) if body.strip() else {}

    def _run_async(self, title: str, fn) -> None:
        """把耗时操作放到后台线程，避免界面卡住；同一时间只允许一个。"""
        if self.busy:
            self._log("上一个操作还在进行，请稍候。")
            return
        self.busy = True
        self.status_var.set(f"正在{title}…")
        self._log(f"—— {title} ——")

        def worker() -> None:
            try:
                fn()
            except Exception as e:  # 后台线程不能让异常静默
                self._log(f"出错：{e}")
            finally:
                self.busy = False
                self.root.after(0, lambda: self.status_var.set("就绪"))

        threading.Thread(target=worker, daemon=True).start()

    # ---------- 状态 ----------
    def refresh_status(self) -> None:
        def job() -> None:
            ok, out = self._adb(f"{MEDIACTL} status")
            self.svc_var.set("服务状态：" + (out.splitlines()[0] if out else "无响应"))
            self._log(out or "（无输出）")
            try:
                d = self._http("/api/library?cat=all")
                cats = {c["id"]: c["count"] for c in d.get("categories", [])}
                self.lib_var.set(
                    f"媒体库：共 {cats.get('all', 0)} 部"
                    f"（电影电视剧 {cats.get('movie', 0)} · 番剧 {cats.get('series', 0)}）"
                )
            except Exception as e:
                self.lib_var.set(f"媒体库：网页无响应（{e}）")

        self._run_async("检查状态", job)

    def open_web(self) -> None:
        import webbrowser
        webbrowser.open(f"http://{self.host_var.get().strip()}:{HTTP_PORT}/")

    # ---------- 一键体检 ----------
    # 诊断逻辑跑在盒子上（rescue-server 的 /api/diagnose），这里只负责：
    # 1. 调接口拿结果；2. 8089 挂了就先拉起来再调；3. 把结论翻译成人话。
    def diagnose(self) -> None:
        def job() -> None:
            d = self._diagnose_call()
            if d is None:
                self._log("体检失败：救援服务（8089）连不上，也拉不起来。")
                self._log("可点「启动救援服务」重试，或确认盒子在线。")
                return
            for c in d.get("checks", []):
                flag = "OK  " if c.get("ok") else "FAIL"
                self._log(f"[{flag}] {c.get('name')}：{c.get('detail')}")
                if not c.get("ok") and c.get("fixLabel"):
                    self._log(f"       └→ 建议按：{c['fixLabel']}")
            self._log("")
            self._log(d.get("verdict", ""))

        self._run_async("一键体检", job)

    def _diagnose_call(self):
        """请求 8089 的诊断接口；不通就先用 adb 把救援服务拉起来，再试一次。"""
        host = self.host_var.get().strip()
        try:
            return self._http("/api/diagnose", timeout=30, port=RESCUE_PORT)
        except Exception as e:
            self._log(f"救援服务没响应（{e}），尝试用 adb 拉起它…")
        ok, out = self._adb(f"{self.RESCUECTL} start", timeout=TIMEOUT_QUICK)
        if not ok:
            self._log(f"拉起救援服务失败：{out}")
            return None
        self._log(out or "救援服务已启动。")
        for _ in range(8):
            time.sleep(1)
            try:
                return self._http("/api/diagnose", timeout=30, port=RESCUE_PORT)
            except Exception:
                continue
        return None

    # ---------- 服务 ----------
    def svc_start(self) -> None:
        self._run_async("启动服务", lambda: self._svc_action("start"))

    def svc_restart(self) -> None:
        self._run_async("重启服务", lambda: self._svc_action("restart"))

    def svc_stop(self) -> None:
        self._run_async("停止服务", lambda: self._svc_action("stop"))

    def _svc_action(self, action: str) -> None:
        # 缓存目录可能因为拔过硬盘而消失，启动前补一次
        if action in ("start", "restart"):
            self._adb(f"mkdir -p {CACHE_DIR}")
        ok, out = self._adb(f"{MEDIACTL} {action}", timeout=TIMEOUT_SLOW)
        self._log(out or "（无输出）")
        if action == "stop":
            self.svc_var.set("服务状态：已停止")
            return
        # 启动后等它扫库，再确认网页真的通了
        self._log("等待服务就绪…")
        for i in range(10):
            time.sleep(3)
            try:
                d = self._http("/api/library?cat=all", timeout=8)
                cats = {c["id"]: c["count"] for c in d.get("categories", [])}
                self.lib_var.set(
                    f"媒体库：共 {cats.get('all', 0)} 部"
                    f"（电影电视剧 {cats.get('movie', 0)} · 番剧 {cats.get('series', 0)}）"
                )
                self.svc_var.set("服务状态：运行中，网页可访问")
                self._log(f"服务已就绪（等待 {(i + 1) * 3}s），共 {cats.get('all', 0)} 部资源。")
                return
            except Exception:
                continue
        self.svc_var.set("服务状态：进程在跑，但网页暂时无响应")
        self._log("30 秒内网页仍无响应，可点「查看日志」看看卡在哪。")

    def svc_logs(self) -> None:
        def job() -> None:
            ok, out = self._adb(f"{MEDIACTL} logs", timeout=TIMEOUT_SLOW)
            self._log(out or "（日志为空）")

        self._run_async("读取日志", job)

    # ---------- 媒体库与硬盘 ----------
    def lib_scan(self) -> None:
        def job() -> None:
            self._http("/api/scan", post=True)
            self._log("已触发扫描，等待完成…")
            time.sleep(6)
            d = self._http("/api/library?cat=all")
            cats = {c["id"]: c["count"] for c in d.get("categories", [])}
            self.lib_var.set(
                f"媒体库：共 {cats.get('all', 0)} 部"
                f"（电影电视剧 {cats.get('movie', 0)} · 番剧 {cats.get('series', 0)}）"
            )
            self._log(f"扫描完成，共 {cats.get('all', 0)} 部资源。")

        self._run_async("重新扫描媒体库", job)

    def disk_check(self) -> None:
        def job() -> None:
            ok, out = self._adb("mount | grep '/mnt/sd'; echo '--- 期望路径:'; ls " + MEDIA_MOUNT)
            self._log(out or "（硬盘未挂载）")
            if MEDIA_MOUNT not in out:
                self._log("老路径不可用，盘可能又改名了，可试试「拔插硬盘后修复」。")

        self._run_async("检查硬盘挂载", job)

    def _has_media_dirs(self) -> bool:
        """判断媒体目录在不在。

        必须用 test -d，绝不能用 `ls` 的输出做中文匹配——盒子 locale 不支持
        UTF-8，busybox ls 会把中文目录名输出成一串问号（"??????"），
        拿中文去匹配必然失败，会把好盘误判成坏盘。
        test -d 走内核路径比对，不经输出转码，结果可靠。
        """
        ok, _ = self._adb(
            f"test -d {MEDIA_MOUNT}/电影电视剧 || test -d {MEDIA_MOUNT}/番剧")
        return ok

    def disk_recover(self) -> None:
        """拔插硬盘后的一键修复。

        盒子拔插硬盘后系统可能给它换名字（如 sda -> sdb），挂载点会变，
        而服务配置写死 /mnt/sda/sda1。这里自动找到盘的实际挂载点：
          - 还是 sda1：直接补建缓存目录、重启服务（老流程）
          - 变成别的名字：把新挂载点嫁接（bind mount）回老路径再重启
        顺便检测掉盘瞬间写坏的 BT 进度数据库（.torrent.bolt.db），
        损坏则重建（前提是没有进行中的下载任务）。
        """
        def job() -> None:
            # 1. 找到盘的实际挂载点（形如 /dev/block/vold/8:17 /mnt/sdb/sdb1 tntfs）
            ok, out = self._adb("mount | grep '/mnt/sd'")
            mounts = re.findall(r"(/dev/block/\S+) (/mnt/sd[a-z]+/sd[a-z]+1) ", out)
            usb_mount = mounts[0][1] if mounts else None
            if not usb_mount:
                self._log("没找到已挂载的 USB 硬盘。请重新插一次，等十几秒再点。")
                self._log(out or "（mount 里没有任何 vold 挂载记录）")
                return
            self._log(f"硬盘挂载点：{usb_mount}")
            if usb_mount != MEDIA_MOUNT:
                # 老路径已经能用（本轮或之前嫁接过）就不再叠一层嫁接
                if self._has_media_dirs():
                    self._log("老路径已可用，跳过嫁接。")
                else:
                    self._log(f"系统给盘换了名字（期望 {MEDIA_MOUNT}），正在嫁接回老路径…")
                    self._adb(f"mkdir -p {MEDIA_MOUNT}")
                    ok, out2 = self._adb(f"mount -o bind {usb_mount} {MEDIA_MOUNT}")
                    if not ok:
                        self._log(f"嫁接失败：{out2}")
                        return
                    self._log("嫁接完成，正在校验媒体目录…")
                    if not self._has_media_dirs():
                        self._log("警告：老路径里没看到媒体目录，请先检查硬盘内容和挂载位置。")
                        return

            # 2. 检测 BT 进度数据库是否损坏：坏库是持续性的（每次启动都会报），
            #    所以只看「最近一次服务启动之后」的日志段，旧记录不算数。
            ok, mark = self._adb(
                "grep -n 'starting media-server on port' /data/server/logs/media-server.log "
                "| /system/bin/busybox tail -n 1", timeout=TIMEOUT_QUICK)
            start_line = 0
            if ok and mark.strip():
                try:
                    start_line = int(mark.strip().split(":", 1)[0])
                except ValueError:
                    start_line = 0
            damaged = False
            if start_line:
                ok, log = self._adb(
                    f"/system/bin/busybox tail -n +{start_line} "
                    "/data/server/logs/media-server.log | grep -c 'invalid database'",
                    timeout=TIMEOUT_QUICK)
                damaged = ok and log.strip().isdigit() and int(log.strip()) > 0
            if damaged:
                # 有坏库记录：确认没有下载任务再重建，否则跳过提醒
                busy = False
                try:
                    tasks = self._http("/api/tasks", timeout=8)
                    busy = any(t.get("status") == "downloading" for t in tasks)
                except Exception:
                    pass
                if busy:
                    self._log("检测到 BT 数据库曾损坏，但当前有下载任务，本次跳过重建，下载完再修。")
                else:
                    self._log("检测到 BT 进度数据库损坏，正在重建…")
                    self._adb(f"rm -f {CACHE_DIR}/.torrent.bolt.db")
                    self._log("数据库已删除，重启服务后会自动重建。")

            # 3. 补建缓存目录并重启服务
            self._log("正在补建缓存目录并重启服务…")
            self._adb(f"mkdir -p {CACHE_DIR}")
            self._svc_action("restart")

        self._run_async("修复硬盘与服务", job)

    def disk_usage(self) -> None:
        def job() -> None:
            ok, out = self._adb(f"df {MEDIA_MOUNT} /data")
            self._log(out or "（无输出）")

        self._run_async("查看磁盘占用", job)

    # ---------- 下载与缓存 ----------
    def tasks_list(self) -> None:
        def job() -> None:
            tasks = self._http("/api/tasks")
            if not tasks:
                self._log("当前没有下载任务。")
                return
            for t in tasks:
                speed = t.get("speedKBs") or 0
                sp = f"{speed / 1024:.1f} MB/s" if speed > 1024 else f"{speed:.0f} KB/s"
                line = (f"[{t.get('status')}] {t.get('name')} "
                        f"{t.get('progress', 0):.1f}% · {sp} · {t.get('peers', 0)} 节点")
                if t.get("error"):
                    line += f" · 错误：{t['error']}"
                self._log(line)

        self._run_async("查看下载任务", job)

    def cache_list(self) -> None:
        def job() -> None:
            d = self._http("/api/manage/cache")
            entries = d.get("entries") or []
            self._log(f"缓存目录：{d.get('dir')}")
            self._log(f"共 {len(entries)} 项，占用 {d.get('totalHuman', '0')}")
            for e in entries:
                tag = "（正在下载）" if e.get("inUse") else ("（未下完）" if e.get("partial") else "")
                self._log(f"  · {e.get('title') or e.get('name')} — {e.get('sizeHuman')}{tag}")

        self._run_async("查看缓存占用", job)

    def cache_clear(self) -> None:
        d = None
        try:
            d = self._http("/api/manage/cache")
        except Exception as e:
            self._log(f"读取缓存失败：{e}")
            return
        entries = d.get("entries") or []
        if not entries:
            self._log("缓存目录是干净的，无需清理。")
            return
        busy = sum(1 for e in entries if e.get("inUse"))
        msg = f"清空全部下载缓存？\n\n共 {len(entries)} 项，占用 {d.get('totalHuman', '0')}"
        if busy:
            msg += f"\n其中 {busy} 项正在下载，会自动跳过。"
        msg += "\n\n未完成的下载进度会丢失。"
        from tkinter import messagebox
        if not messagebox.askyesno("确认清理", msg):
            return

        def job() -> None:
            url = f"http://{self.host_var.get().strip()}:{HTTP_PORT}/api/manage/cache/delete"
            req = urllib.request.Request(url, data=b'{"all":true}', method="POST")
            req.add_header("Content-Type", "application/json")
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(req, timeout=60) as r:
                res = json.loads(r.read().decode("utf-8", "replace") or "{}")
            self._log(f"已清理 {res.get('removed', '')}，释放 {res.get('freedHuman', '0')}")

        self._run_async("清空下载缓存", job)

    # ---------- IP 漂移自救 ----------
    def _probe_box(self, ip: str, port: int) -> bool:
        """探测某 IP 的指定端口是不是我们的盒子。
        8088 影院的特征是 /api/web-manifest，8089 救援的特征是 /api/status。"""
        feat = "/api/status" if port == RESCUE_PORT else "/api/web-manifest"
        try:
            req = urllib.request.Request(f"http://{ip}:{port}{feat}")
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(req, timeout=1.5) as r:
                return r.status == 200
        except Exception:
            return False

    def ip_find(self) -> None:
        """扫本机所在 192.168.1.0/24 网段找盒子真实 IP，找到就直接填进地址栏。"""
        def job() -> None:
            self._log("正在扫描 192.168.1.0/24 …（约半分钟）")
            hits = []
            import concurrent.futures
            def probe(i):
                ip = f"192.168.1.{i}"
                return ip if self._probe_box(ip, HTTP_PORT) else None
            with concurrent.futures.ThreadPoolExecutor(max_workers=64) as ex:
                for r in ex.map(probe, range(1, 255)):
                    if r:
                        hits.append(r)
            if not hits:
                self._log("没扫到盒子。确认盒子开着、和电脑连同一个路由器，再试一次。")
                return
            self._log("找到盒子：" + "、".join(hits))
            first = hits[0]
            self.host_var.set(first)
            self.target_var.set(f"{first}:30016")
            self._log(f"地址已填成 {first}，后续按钮都会操作它。")
        self._run_async("查找盒子", job)

    def ip_fix(self) -> None:
        """IP 漂移后把老地址挂回盒子（走 8089 救援服务）。"""
        def job() -> None:
            host = self.host_var.get().strip()
            # 当前地址不是 .13 就先用救援服务（它自己也在 .13 上，所以要先探它在哪）
            candidates = [host, DEFAULT_HTTP_HOST]
            base = None
            for c in candidates:
                if self._probe_box(c, RESCUE_PORT):
                    base = c
                    break
            if not base:
                self._log("先点「找到盒子」确认盒子地址（救援服务 8089 也没找到）。")
                return
            try:
                req = urllib.request.Request(f"http://{base}:{RESCUE_PORT}/api/fix-ip", data=b"{}", method="POST")
                req.add_header("Content-Type", "application/json")
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open(req, timeout=20) as r:
                    res = json.loads(r.read().decode("utf-8", "replace") or "{}")
                if res.get("ok"):
                    self._log(res.get("out") or "已完成。")
                    self.host_var.set(DEFAULT_HTTP_HOST)
                    self.target_var.set(DEFAULT_ADB_TARGET)
                    self._log("地址栏已恢复为 " + DEFAULT_HTTP_HOST + "。")
                else:
                    self._log("修复失败：" + (res.get("error") or "未知错误"))
            except Exception as e:
                self._log(f"请求救援服务失败：{e}")
        self._run_async("挂回老地址", job)

    # ---------- 救援服务（8089，手机工具箱） ----------
    RESCUECTL = "/data/server/bin/rescuectl"
    RESCUE_PORT = 8089

    def rescue_start(self) -> None:
        self._run_async("启动救援服务", lambda: self._rescue_action("start"))

    def rescue_restart(self) -> None:
        self._run_async("重启救援服务", lambda: self._rescue_action("restart"))

    def rescue_stop(self) -> None:
        self._run_async("停止救援服务", lambda: self._rescue_action("stop"))

    def _rescue_action(self, action: str) -> None:
        ok, out = self._adb(f"{self.RESCUECTL} {action}", timeout=TIMEOUT_QUICK)
        self._log(out or "（无输出）")
        if action == "stop":
            return
        # 启动后确认 8089 真的通了
        for _ in range(5):
            time.sleep(1)
            try:
                urllib.request.urlopen(
                    f"http://{self.host_var.get().strip()}:{self.RESCUE_PORT}/api/status",
                    timeout=5)
                self._log("救援服务已就绪：手机浏览器打开 "
                          f"http://{self.host_var.get().strip()}:{self.RESCUE_PORT} 即可使用。")
                return
            except Exception:
                continue
        self._log("救援服务进程在跑，但 8089 无响应，可查看 /data/server/logs/rescue-server.log。")

    def open_rescue(self) -> None:
        import webbrowser
        webbrowser.open(f"http://{self.host_var.get().strip()}:{self.RESCUE_PORT}/")


def main() -> None:
    root = tk.Tk()
    try:
        root.call("tk", "scaling", 1.25)
    except tk.TclError:
        pass
    BoxPanel(root)
    root.mainloop()


if __name__ == "__main__":
    main()

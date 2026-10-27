from __future__ import annotations

import io
import os
import queue
import shutil
import subprocess
import threading
import time
import tkinter as tk
from pathlib import Path
from tkinter import ttk

try:
    from PIL import Image, ImageTk
except ImportError as exc:  # pragma: no cover - 启动环境检查
    raise SystemExit("缺少 Pillow，请执行：python -m pip install Pillow") from exc


DEFAULT_TARGET = "box.local:30016"
PNG_HEADER = b"\x89PNG\r\n\x1a\n"


class ScreenViewer:
    """通过 ADB screencap 周期性抓取并显示机顶盒画面。"""

    def __init__(self, root: tk.Tk) -> None:
        self.root = root
        self.root.title("机顶盒画面查看器")
        self.root.geometry("960x640")
        self.root.minsize(480, 360)

        self.adb_path = self._find_adb()
        self.target_var = tk.StringVar(
            value=os.environ.get("ADB_TARGET", DEFAULT_TARGET)
        )
        self.interval_var = tk.StringVar(value="0.30")
        self.status_var = tk.StringVar(value="准备连接")

        self.target = self.target_var.get().strip()
        self.serial: str | None = None
        self.worker: threading.Thread | None = None
        self.stop_event = threading.Event()
        self.connection_lock = threading.Lock()
        self.reconnect_at = 0.0
        self.frame_count = 0
        self.last_status_at = 0.0

        self.ui_queue: queue.Queue[tuple[str, object]] = queue.Queue(maxsize=80)
        self.frame_queue: queue.Queue[Image.Image] = queue.Queue(maxsize=1)
        self.display_rect = (0, 0, 0, 0)
        self.frame_size = (0, 0)

        self._build_ui()
        self.root.protocol("WM_DELETE_WINDOW", self.close)
        self.root.after(40, self._process_ui_queue)
        self.root.after(250, self.connect)

    @staticmethod
    def _find_adb() -> str | None:
        """按常见位置查找 adb.exe。"""
        candidates: list[Path] = []
        local_app_data = os.environ.get("LOCALAPPDATA")
        if local_app_data:
            candidates.append(
                Path(local_app_data) / "Android" / "Sdk" / "platform-tools" / "adb.exe"
            )
        here = Path(__file__).resolve().parent
        candidates.extend((here / "adb.exe", here / "platform-tools" / "adb.exe"))

        for candidate in candidates:
            if candidate.is_file():
                return str(candidate)

        return shutil.which("adb.exe") or shutil.which("adb")

    def _build_ui(self) -> None:
        controls = ttk.Frame(self.root, padding=(10, 10, 10, 6))
        controls.pack(fill=tk.X)

        ttk.Label(controls, text="ADB 地址/序列号").pack(side=tk.LEFT)
        target_entry = ttk.Entry(controls, textvariable=self.target_var, width=24)
        target_entry.pack(side=tk.LEFT, padx=(6, 10))
        target_entry.bind("<Return>", lambda _event: self.connect())

        ttk.Button(controls, text="连接并显示", command=self.connect).pack(side=tk.LEFT)
        ttk.Button(controls, text="停止刷新", command=self.stop_capture).pack(
            side=tk.LEFT, padx=(6, 0)
        )

        ttk.Label(controls, text="间隔(秒)").pack(side=tk.LEFT, padx=(18, 6))
        ttk.Entry(controls, textvariable=self.interval_var, width=7).pack(side=tk.LEFT)

        self.screen_label = tk.Label(
            self.root,
            text="等待连接机顶盒...",
            bg="#111111",
            fg="#eeeeee",
            anchor=tk.CENTER,
        )
        self.screen_label.pack(fill=tk.BOTH, expand=True, padx=10, pady=(0, 6))
        self.screen_label.configure(cursor="crosshair")
        self.screen_label.bind("<Button-1>", self._handle_click)

        status_bar = ttk.Label(
            self.root,
            textvariable=self.status_var,
            relief=tk.SUNKEN,
            anchor=tk.W,
            padding=(8, 4),
        )
        status_bar.pack(fill=tk.X, side=tk.BOTTOM)

    @staticmethod
    def _creation_kwargs() -> dict[str, object]:
        if os.name == "nt":
            return {"creationflags": subprocess.CREATE_NO_WINDOW}
        return {}

    def _run_adb_text(
        self, args: list[str], timeout: float = 8.0
    ) -> tuple[int, str, str]:
        if not self.adb_path:
            return 127, "", "找不到 adb.exe"
        try:
            result = subprocess.run(
                [self.adb_path, *args],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout,
                **self._creation_kwargs(),
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            return 1, "", str(exc)
        return (
            result.returncode,
            result.stdout.decode("utf-8", errors="replace"),
            result.stderr.decode("utf-8", errors="replace"),
        )

    def _run_adb_bytes(
        self, args: list[str], timeout: float = 5.0
    ) -> tuple[int, bytes, str]:
        if not self.adb_path:
            return 127, b"", "找不到 adb.exe"
        try:
            result = subprocess.run(
                [self.adb_path, *args],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout,
                **self._creation_kwargs(),
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            return 1, b"", str(exc)
        return (
            result.returncode,
            result.stdout,
            result.stderr.decode("utf-8", errors="replace"),
        )

    @staticmethod
    def _is_network_target(target: str) -> bool:
        if ":" not in target:
            return False
        port = target.rsplit(":", 1)[-1]
        return port.isdigit()

    @staticmethod
    def _parse_devices(output: str) -> list[tuple[str, str, str]]:
        devices: list[tuple[str, str, str]] = []
        for raw_line in output.splitlines():
            line = raw_line.strip()
            if not line or line.startswith("List of devices"):
                continue
            fields = line.split()
            if len(fields) >= 2:
                devices.append((fields[0], fields[1], line))
        return devices

    def _post_ui(self, kind: str, value: object) -> None:
        try:
            self.ui_queue.put_nowait((kind, value))
        except queue.Full:
            try:
                self.ui_queue.get_nowait()
            except queue.Empty:
                pass
            try:
                self.ui_queue.put_nowait((kind, value))
            except queue.Full:
                pass

    def _post_status(self, text: str, throttle: bool = False) -> None:
        now = time.monotonic()
        if throttle and now - self.last_status_at < 1.0:
            return
        self.last_status_at = now
        self._post_ui("status", text)

    def connect(self) -> None:
        """在后台连接目标，避免网络 ADB 卡住窗口。"""
        self.target = self.target_var.get().strip()
        if not self.target:
            self._post_status("请填写 ADB 地址或设备序列号")
            return
        if not self.adb_path:
            self._post_status("找不到 adb.exe，请检查 ADB 安装")
            return
        if self.connection_lock.locked():
            self._post_status("正在连接，请稍候")
            return
        threading.Thread(
            target=self._refresh_device,
            args=(False,),
            daemon=True,
            name="adb-connect",
        ).start()

    def _refresh_device(self, silent: bool) -> None:
        if not self.connection_lock.acquire(blocking=False):
            return
        try:
            if not silent:
                self._post_status(f"正在连接 {self.target}...")
            self._run_adb_text(["start-server"], timeout=8.0)

            if self._is_network_target(self.target):
                self._run_adb_text(["connect", self.target], timeout=8.0)

            code, output, error = self._run_adb_text(["devices", "-l"], timeout=8.0)
            devices = self._parse_devices(output)
            if code != 0:
                self.serial = None
                self._post_status(f"ADB 查询失败：{self._clean_error(error or output)}")
                return

            selected: tuple[str, str, str] | None = None
            for device in devices:
                if device[0] == self.target and device[1] == "device":
                    selected = device
                    break
            if selected is None:
                for device in devices:
                    if device[1] == "device":
                        selected = device
                        break

            if selected is None:
                self.serial = None
                matching = next(
                    (device for device in devices if device[0] == self.target), None
                )
                if matching:
                    self._post_status(
                        f"目标状态为 {matching[1]}，请保持设备在线",
                        throttle=silent,
                    )
                else:
                    self._post_status("没有找到可用的 ADB 设备", throttle=silent)
                return

            self.serial = selected[0]
            self._post_status(f"已连接：{selected[2]}")
            self._post_ui("start", None)
        finally:
            self.connection_lock.release()

    def start_capture(self) -> None:
        if not self.serial:
            self._post_status("尚未连接到 ADB 设备")
            return
        try:
            interval = float(self.interval_var.get().strip())
        except ValueError:
            interval = 0.30
        interval = max(0.10, min(interval, 5.0))
        self.interval_var.set(f"{interval:.2f}")

        if self.worker and self.worker.is_alive():
            return
        self.stop_event.clear()
        self.worker = threading.Thread(
            target=self._capture_loop,
            args=(interval,),
            daemon=True,
            name="adb-screencap",
        )
        self.worker.start()
        self._post_status(f"正在显示画面，约每 {interval:.2f} 秒刷新")

    def stop_capture(self) -> None:
        self.stop_event.set()
        self._post_status("已停止刷新，点击“连接并显示”可继续")

    def _capture_png(self, serial: str) -> tuple[int, bytes, str]:
        # Android 4.4 的 adbd 只支持 PTY，优先使用普通 shell 抓取 display 0。
        shell_code, shell_data, shell_error = self._run_adb_bytes(
            ["-s", serial, "shell", "screencap", "-d", "0", "-p"]
        )
        # Windows + 旧版 adbd 的 PTY 会把每个换行扩展成多个回车。
        shell_data = shell_data.replace(b"\r\r\r\n", b"\r\n")
        shell_data = shell_data.replace(b"\r\r\n", b"\n")
        if shell_code == 0 and shell_data.startswith(PNG_HEADER):
            return shell_code, shell_data, shell_error

        # 新版设备如果不接受上面的参数，再尝试不经过 PTY 的 exec-out。
        exec_code, exec_data, exec_error = self._run_adb_bytes(
            ["-s", serial, "exec-out", "screencap", "-d", "0", "-p"]
        )
        if exec_code == 0 and exec_data.startswith(PNG_HEADER):
            return exec_code, exec_data, exec_error
        return shell_code or exec_code, shell_data or exec_data, shell_error or exec_error

    def _capture_loop(self, interval: float) -> None:
        while not self.stop_event.is_set():
            serial = self.serial
            if not serial:
                self.stop_event.wait(0.5)
                continue

            code, data, error = self._capture_png(serial)
            if code == 0 and data.startswith(PNG_HEADER):
                try:
                    with Image.open(io.BytesIO(data)) as image:
                        frame = image.convert("RGB")
                except Exception as exc:  # Pillow 对损坏帧的统一处理
                    self._post_status(f"画面解析失败：{exc}", throttle=True)
                else:
                    self.frame_count += 1
                    self._put_latest_frame(frame)
            else:
                self._post_status(
                    f"抓取失败：{self._clean_error(error)}，正在重试",
                    throttle=True,
                )
                now = time.monotonic()
                if now >= self.reconnect_at:
                    self.reconnect_at = now + 3.0
                    threading.Thread(
                        target=self._refresh_device,
                        args=(True,),
                        daemon=True,
                        name="adb-reconnect",
                    ).start()

            self.stop_event.wait(interval)

    def _put_latest_frame(self, frame: Image.Image) -> None:
        try:
            self.frame_queue.get_nowait()
        except queue.Empty:
            pass
        try:
            self.frame_queue.put_nowait(frame)
        except queue.Full:
            pass

    def _process_ui_queue(self) -> None:
        try:
            while True:
                kind, value = self.ui_queue.get_nowait()
                if kind == "status":
                    self.status_var.set(str(value))
                elif kind == "start":
                    self.start_capture()
        except queue.Empty:
            pass

        try:
            while True:
                frame = self.frame_queue.get_nowait()
                self._show_frame(frame)
        except queue.Empty:
            pass

        self.root.after(40, self._process_ui_queue)

    def _show_frame(self, frame: Image.Image) -> None:
        label_width = max(self.screen_label.winfo_width(), 120)
        label_height = max(self.screen_label.winfo_height(), 120)
        width = max(label_width - 20, 100)
        height = max(label_height - 20, 100)
        display = frame.copy()
        resampling = getattr(Image, "Resampling", Image)
        display.thumbnail((width, height), resampling.LANCZOS)

        self.frame_size = frame.size
        left = (label_width - display.width) // 2
        top = (label_height - display.height) // 2
        self.display_rect = (
            left,
            top,
            left + display.width,
            top + display.height,
        )

        photo = ImageTk.PhotoImage(display)
        self.screen_label.configure(image=photo, text="")
        self.screen_label.image = photo

    def _handle_click(self, event: tk.Event) -> None:
        """把窗口内的鼠标位置换算成设备原始坐标。"""
        left, top, right, bottom = self.display_rect
        frame_width, frame_height = self.frame_size
        serial = self.serial
        if not serial or frame_width <= 0 or frame_height <= 0:
            self._post_status("画面还没有准备好，暂时不能点击")
            return
        if not (left <= event.x < right and top <= event.y < bottom):
            return

        display_width = right - left
        display_height = bottom - top
        x = round((event.x - left) * frame_width / display_width)
        y = round((event.y - top) * frame_height / display_height)
        x = max(0, min(frame_width - 1, x))
        y = max(0, min(frame_height - 1, y))
        threading.Thread(
            target=self._send_tap,
            args=(serial, x, y),
            daemon=True,
            name="adb-tap",
        ).start()

    def _send_tap(self, serial: str, x: int, y: int) -> None:
        code, output, error = self._run_adb_text(
            ["-s", serial, "shell", "input", "tap", str(x), str(y)],
            timeout=4.0,
        )
        if code == 0:
            self._post_status(f"已点击设备坐标 ({x}, {y})")
        else:
            self._post_status(f"点击失败：{self._clean_error(error or output)}")

    @staticmethod
    def _clean_error(text: str) -> str:
        cleaned = " ".join(text.replace("\r", " ").replace("\n", " ").split())
        return cleaned[:180] or "设备没有返回错误信息"

    def close(self) -> None:
        self.stop_event.set()
        self.root.destroy()


def main() -> None:
    root = tk.Tk()
    ScreenViewer(root)
    root.mainloop()


if __name__ == "__main__":
    main()

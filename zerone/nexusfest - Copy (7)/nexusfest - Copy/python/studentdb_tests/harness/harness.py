"""PY-01: Test Harness and Process Manager.

Controls the Go binary lifecycle, enforces workspace isolation, and wraps HTTP API v1 routes.
"""

from __future__ import annotations
from dataclasses import dataclass
import json
import os
import shutil
import signal
import subprocess
import sys
import time
from typing import Any, Dict, List, Optional, Tuple
import urllib.request
import urllib.error


class HarnessError(Exception):
    """Raised on test harness setup, execution, or lifecycle failures."""
    pass


@dataclass
class BinaryInfo:
    path: str
    version: str
    format_version: int
    build: str  # "production" | "faultinject"

    @classmethod
    def probe(cls, binary_path: str) -> BinaryInfo:
        if not os.path.exists(binary_path):
            raise HarnessError(f"Binary not found: {binary_path}")

        try:
            out = subprocess.check_output([binary_path, "version", "--json"], text=True, stderr=subprocess.PIPE)
            data = json.loads(out)
            return cls(
                path=binary_path,
                version=data.get("version", "1.0.0"),
                format_version=data.get("format_version", 1),
                build=data.get("build", "production"),
            )
        except Exception as e:
            raise HarnessError(f"Failed to probe binary {binary_path}: {e}")

    @property
    def is_faultinject(self) -> bool:
        return self.build == "faultinject"


class ApiClient:
    """HTTP client for BinStudentDB loopback API."""

    def __init__(self, base_url: str, admin_token: Optional[str] = None) -> None:
        self.base_url = base_url.rstrip("/")
        self.admin_token = admin_token

    def _request(self, method: str, path: str, data: Optional[Dict[str, Any]] = None, admin: bool = False) -> Tuple[int, Any]:
        url = f"{self.base_url}{path}"
        headers = {"Content-Type": "application/json"}
        if admin and self.admin_token:
            headers["Authorization"] = f"Bearer {self.admin_token}"

        body_bytes = None
        if data is not None:
            body_bytes = json.dumps(data).encode("utf-8")

        req = urllib.request.Request(url, data=body_bytes, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=10.0) as resp:
                resp_body = resp.read().decode("utf-8")
                try:
                    return resp.status, json.loads(resp_body)
                except Exception:
                    return resp.status, resp_body
        except urllib.error.HTTPError as e:
            err_body = e.read().decode("utf-8")
            try:
                return e.code, json.loads(err_body)
            except Exception:
                return e.code, err_body
        except Exception as e:
            raise HarnessError(f"HTTP request {method} {url} failed: {e}")

    def create_student(self, st: Dict[str, Any]) -> Tuple[int, Any]:
        return self._request("POST", "/v1/students", st)

    def get_student(self, sid: int) -> Tuple[int, Any]:
        return self._request("GET", f"/v1/students/{sid}")

    def update_student(self, sid: int, st: Dict[str, Any]) -> Tuple[int, Any]:
        return self._request("PUT", f"/v1/students/{sid}", st)

    def delete_student(self, sid: int) -> Tuple[int, Any]:
        return self._request("DELETE", f"/v1/students/{sid}")

    def batch(self, ops: List[Dict[str, Any]]) -> Tuple[int, Any]:
        return self._request("POST", "/v1/tx", {"ops": ops})

    def scan(self, cursor: int = 0, limit: int = 1000, **filters: Any) -> Tuple[int, Any]:
        query = f"mode=scan&cursor={cursor}&limit={limit}"
        for k, v in filters.items():
            if v is not None and v != "":
                query += f"&{k}={v}"
        return self._request("GET", f"/v1/students?{query}")

    def range_index(self, from_id: int = 0, to_id: int = 0xFFFFFFFF, limit: int = 1000) -> Tuple[int, Any]:
        return self._request("GET", f"/v1/students?mode=index&from={from_id}&to={to_id}&limit={limit}")

    def health(self) -> Tuple[int, Any]:
        return self._request("GET", "/v1/health")

    def admin_stats(self) -> Tuple[int, Any]:
        return self._request("GET", "/v1/admin/stats", admin=True)

    def admin_verify(self) -> Tuple[int, Any]:
        return self._request("GET", "/v1/admin/verify", admin=True)

    def admin_checkpoint(self) -> Tuple[int, Any]:
        return self._request("POST", "/v1/admin/checkpoint", admin=True)

    def admin_shutdown(self) -> Tuple[int, Any]:
        return self._request("POST", "/v1/admin/shutdown", admin=True)

    def admin_capabilities(self) -> Tuple[int, Any]:
        return self._request("GET", "/v1/admin/capabilities")

    def get_failpoints(self) -> Tuple[int, Any]:
        return self._request("GET", "/v1/admin/failpoints", admin=True)

    def arm_failpoints(self, spec: Dict[str, Any]) -> Tuple[int, Any]:
        return self._request("POST", "/v1/admin/failpoints", spec, admin=True)

    def disarm_failpoints(self) -> Tuple[int, Any]:
        return self._request("DELETE", "/v1/admin/failpoints", admin=True)


class WorkspaceManager:
    """Manages disposable copies and enforces isolation within data/work/."""

    def __init__(self, base_work_dir: str = "data/work") -> None:
        self.base_work_dir = os.path.abspath(base_work_dir)

    def create_disposable_workspace(self, run_id: str, iteration: str, golden_dir: Optional[str] = None) -> str:
        dest_dir = os.path.join(self.base_work_dir, run_id, iteration, "db")
        # Path guard
        if not os.path.abspath(dest_dir).startswith(self.base_work_dir):
            raise HarnessError(f"Path escape attempt rejected: {dest_dir}")

        if os.path.exists(dest_dir):
            shutil.rmtree(dest_dir)
        os.makedirs(dest_dir, exist_ok=True)

        if golden_dir and os.path.exists(golden_dir):
            # Copy all files from golden
            for fname in os.listdir(golden_dir):
                src = os.path.join(golden_dir, fname)
                dst = os.path.join(dest_dir, fname)
                if os.path.isfile(src):
                    shutil.copy2(src, dst)
                    # Ensure writable
                    os.chmod(dst, 0o600)

        return dest_dir

    def cleanup_workspace(self, workspace_path: str) -> None:
        if not os.path.abspath(workspace_path).startswith(self.base_work_dir):
            raise HarnessError(f"Cannot clean path outside work dir: {workspace_path}")
        if os.path.exists(workspace_path):
            shutil.rmtree(workspace_path, ignore_errors=True)


class ProcessManager:
    """Manages the studentdb process lifecycle."""

    def __init__(self, binary_path: str, data_dir: str, env: Optional[Dict[str, str]] = None) -> None:
        self.binary_path = binary_path
        self.data_dir = data_dir
        self.env = os.environ.copy()
        if env:
            self.env.update(env)
        self.process: Optional[subprocess.Popen] = None
        self.ready_line: Optional[Dict[str, Any]] = None
        self.base_url: Optional[str] = None
        self.client: Optional[ApiClient] = None

    def start(self, extra_args: Optional[List[str]] = None, timeout: float = 10.0) -> None:
        cmd = [
            self.binary_path, "serve",
            "--data-dir", self.data_dir,
            "--addr", "127.0.0.1:0",
        ]
        if extra_args:
            cmd.extend(extra_args)

        self.process = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
            env=self.env,
        )

        # Wait for ready line
        start_time = time.time()
        while time.time() - start_time < timeout:
            if self.process.poll() is not None:
                err_out = self.process.stderr.read() if self.process.stderr else ""
                raise HarnessError(f"Process terminated early with exit code {self.process.returncode}: {err_out}")

            line = self.process.stdout.readline() if self.process.stdout else ""
            if line:
                try:
                    data = json.loads(line.strip())
                    if data.get("event") == "ready":
                        self.ready_line = data
                        addr = data["addr"]
                        self.base_url = f"http://{addr}"
                        self.client = ApiClient(self.base_url, admin_token=self.env.get("STUDENTDB_ADMIN_TOKEN"))
                        return
                except Exception:
                    pass
            time.sleep(0.05)

        self.kill()
        raise HarnessError("Timed out waiting for ready line from studentdb server")

    def kill(self) -> int:
        """Abruptly terminate the process (SIGKILL equivalent)."""
        if self.process:
            try:
                self.process.kill()
            except Exception:
                pass
            self.process.wait()
            return self.process.returncode
        return 0

    def stop(self, timeout: float = 5.0) -> int:
        """Gracefully stop the server with SIGTERM or admin shutdown."""
        if self.process and self.process.poll() is None:
            try:
                if self.client:
                    self.client.admin_shutdown()
            except Exception:
                pass

            try:
                self.process.terminate()
                self.process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()

            return self.process.returncode
        return 0


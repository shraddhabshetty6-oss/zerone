"""PY-12: Performance Monitor and Benchmarking Engine.

Measures sequential scan latency, indexed point lookup latency, throughput, and recovery duration.
Captures system environment block per TEST.md §E.
"""

from __future__ import annotations
import os
import platform
import time
from typing import Any, Dict, List

try:
    import psutil
except ImportError:
    psutil = None

from studentdb_tests.harness import ProcessManager


def capture_environment_block() -> Dict[str, Any]:
    """Capture host system metadata."""
    cpu_count = psutil.cpu_count(logical=True) if psutil else os.cpu_count()
    ram_gb = round(psutil.virtual_memory().total / (1024**3), 2) if psutil else 0.0
    return {
        "os": platform.system(),
        "os_release": platform.release(),
        "architecture": platform.machine(),
        "cpu_count": cpu_count,
        "total_ram_gb": ram_gb,
        "python_version": platform.python_version(),
    }


def compute_percentiles(latencies_ms: List[float]) -> Dict[str, float]:
    """Compute p50, p95, p99, and max percentiles."""
    if not latencies_ms:
        return {"p50": 0.0, "p95": 0.0, "p99": 0.0, "max": 0.0}

    s = sorted(latencies_ms)
    n = len(s)

    def pct(p: float) -> float:
        idx = int(p * n)
        if idx >= n:
            idx = n - 1
        return round(s[idx], 3)

    return {
        "p50": pct(0.50),
        "p95": pct(0.95),
        "p99": pct(0.99),
        "max": round(s[-1], 3),
    }


class PerformanceMonitor:
    """Benchmark runner."""

    def __init__(self, proc_mgr: ProcessManager) -> None:
        self.proc_mgr = proc_mgr
        self.client = proc_mgr.client

    def benchmark_indexed_lookup(self, sample_ids: List[int], repeats: int = 100) -> Dict[str, Any]:
        """Measure point lookup latency."""
        assert self.client is not None, "Client not connected"
        if not sample_ids:
            return {}

        # Warm-up phase
        for sid in sample_ids[:10]:
            self.client.get_student(sid)

        latencies: List[float] = []
        for _ in range(repeats):
            for sid in sample_ids:
                t0 = time.perf_counter()
                code, _ = self.client.get_student(sid)
                el = (time.perf_counter() - t0) * 1000.0  # ms
                if code == 200:
                    latencies.append(el)

        return compute_percentiles(latencies)

    def benchmark_sequential_scan(self, repeats: int = 5) -> Dict[str, Any]:
        """Measure sequential scan page latency."""
        assert self.client is not None, "Client not connected"

        latencies: List[float] = []
        for _ in range(repeats):
            t0 = time.perf_counter()
            code, _ = self.client.scan(limit=1000)
            el = (time.perf_counter() - t0) * 1000.0  # ms
            if code == 200:
                latencies.append(el)

        return compute_percentiles(latencies)

"""PY-13: Unified Test Reporting Engine.

Ingests machine-readable results rows and produces report.json, report.md, report.html, and junit.xml.
"""

from __future__ import annotations
import html
import json
import os
import time
from typing import Any, Dict, List
import xml.etree.ElementTree as ET


class ReportingEngine:
    """Aggregates test outcomes and generates artifacts."""

    def __init__(self, run_id: str, out_dir: str = "reports") -> None:
        self.run_id = run_id
        self.report_dir = os.path.join(out_dir, run_id)
        os.makedirs(self.report_dir, exist_ok=True)
        self.rows: List[Dict[str, Any]] = []

    def add_result(self, row: Dict[str, Any]) -> None:
        self.rows.append(row)

    def write_reports(self, env_block: Dict[str, Any]) -> Dict[str, str]:
        """Generate report.json, report.md, report.html, and junit.xml."""
        total = len(self.rows)
        passed = sum(1 for r in self.rows if r.get("status") == "PASS")
        failed = sum(1 for r in self.rows if r.get("status") == "FAIL")

        summary = {
            "run_id": self.run_id,
            "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "total_tests": total,
            "passed": passed,
            "failed": failed,
            "environment": env_block,
            "results": self.rows,
        }

        # 1. report.json
        json_path = os.path.join(self.report_dir, "report.json")
        with open(json_path, "w", encoding="utf-8") as f:
            json.dump(summary, f, indent=2)

        # 2. report.md
        md_path = os.path.join(self.report_dir, "report.md")
        with open(md_path, "w", encoding="utf-8") as f:
            f.write(f"# BinStudentDB Test Report — Run {self.run_id}\n\n")
            f.write(f"**Status:** {'PASS' if failed == 0 else 'FAIL'} ({passed}/{total} passed)\n\n")
            f.write("## Test Summary\n\n")
            f.write("| Test ID | Scenario | Status | Duration (ms) | Details |\n")
            f.write("|---|---|---|---|---|\n")
            for r in self.rows:
                f.write(f"| {r.get('test_id', '-')} | {r.get('scenario', '-')} | {r.get('status', '-')} | {r.get('duration_ms', 0)} | {r.get('details', '')} |\n")
            f.write("\n## Environment\n\n```json\n")
            f.write(json.dumps(env_block, indent=2))
            f.write("\n```\n")

        # 3. report.html (offline styling, no external dependencies)
        html_path = os.path.join(self.report_dir, "report.html")
        with open(html_path, "w", encoding="utf-8") as f:
            status_color = "#28a745" if failed == 0 else "#dc3545"
            f.write(f"""<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>BinStudentDB Report - {html.escape(self.run_id)}</title>
<style>
body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 40px; background: #f8f9fa; color: #333; }}
.card {{ background: white; padding: 24px; border-radius: 8px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); margin-bottom: 24px; }}
h1, h2 {{ margin-top: 0; }}
.badge {{ display: inline-block; padding: 4px 12px; border-radius: 12px; color: white; background: {status_color}; font-weight: bold; }}
table {{ width: 100%; border-collapse: collapse; margin-top: 16px; }}
th, td {{ padding: 10px 12px; border-bottom: 1px solid #dee2e6; text-align: left; }}
th {{ background: #f1f3f5; }}
tr:hover {{ background: #f8f9fa; }}
.pass {{ color: #28a745; font-weight: bold; }}
.fail {{ color: #dc3545; font-weight: bold; }}
</style>
</head>
<body>
<div class="card">
  <h1>BinStudentDB Test Report <span class="badge">{'PASSED' if failed == 0 else 'FAILED'}</span></h1>
  <p><strong>Run ID:</strong> {html.escape(self.run_id)} | <strong>Total:</strong> {total} | <strong>Passed:</strong> {passed} | <strong>Failed:</strong> {failed}</p>
</div>
<div class="card">
  <h2>Test Executions</h2>
  <table>
    <thead><tr><th>Test ID</th><th>Scenario</th><th>Status</th><th>Duration</th><th>Details</th></tr></thead>
    <tbody>
""")
            for r in self.rows:
                s_cls = "pass" if r.get("status") == "PASS" else "fail"
                f.write(f"<tr><td>{html.escape(str(r.get('test_id', '')))}</td><td>{html.escape(str(r.get('scenario', '')))}</td><td class='{s_cls}'>{html.escape(str(r.get('status', '')))}</td><td>{r.get('duration_ms', 0)} ms</td><td>{html.escape(str(r.get('details', '')))}</td></tr>\n")

            f.write(f"""    </tbody>
  </table>
</div>
</body>
</html>
""")

        # 4. junit.xml
        junit_path = os.path.join(self.report_dir, "junit.xml")
        testsuite = ET.Element("testsuite", {
            "name": "BinStudentDB",
            "tests": str(total),
            "failures": str(failed),
            "errors": "0",
            "time": "0",
        })
        for r in self.rows:
            tc = ET.SubElement(testsuite, "testcase", {
                "name": str(r.get("scenario", "unknown")),
                "classname": str(r.get("test_id", "test")),
                "time": str(r.get("duration_ms", 0) / 1000.0),
            })
            if r.get("status") == "FAIL":
                fail = ET.SubElement(tc, "failure", {"message": str(r.get("details", ""))})
                fail.text = str(r.get("details", ""))

        tree = ET.ElementTree(testsuite)
        tree.write(junit_path, encoding="utf-8", xml_declaration=True)

        return {
            "json": json_path,
            "md": md_path,
            "html": html_path,
            "junit": junit_path,
        }


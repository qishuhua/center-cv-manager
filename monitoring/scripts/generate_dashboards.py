import base64
import json
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


CENTERS_FILE = Path(os.environ.get("CENTERS_FILE", "/targets/centers.json"))
TEMPLATES_DIR = Path("/templates")
OUTPUT_DIR = Path("/output")
REFRESH_SECONDS = int(os.environ.get("DASHBOARD_REFRESH_SECONDS", "15"))
GRAFANA_URL = os.environ.get("GRAFANA_URL", "http://grafana:3000").rstrip("/")
GRAFANA_USER = os.environ.get("GRAFANA_ADMIN_USER", "admin")
GRAFANA_PASSWORD = os.environ.get("GRAFANA_ADMIN_PASSWORD", "")
LOGS_USERNAME = os.environ.get("CENTER_LOGS_USERNAME", "grafana_logs")
LOGS_PASSWORD_FILE = Path(
    os.environ.get("LOGS_PASSWORD_FILE", "/secrets/center-logs-password")
)
SITE_PATTERN = re.compile(r"^[a-z0-9_-]+$")
SUPPORTED_GPU_VENDORS = {"nvidia", "denglin"}
PROMETHEUS_UID_PREFIX = "center-prometheus-"
ELASTICSEARCH_UID_PREFIX = "center-elasticsearch-"


def set_center_datasources(value, prometheus_uid, elasticsearch_uid):
    if isinstance(value, list):
        for item in value:
            set_center_datasources(item, prometheus_uid, elasticsearch_uid)
    elif isinstance(value, dict):
        if value.get("type") == "prometheus" and "uid" in value:
            value["uid"] = prometheus_uid
        elif value.get("type") == "elasticsearch" and "uid" in value:
            value["uid"] = elasticsearch_uid
        for item in value.values():
            set_center_datasources(item, prometheus_uid, elasticsearch_uid)


def load_template(gpu_vendor):
    template_file = TEMPLATES_DIR / f"center-cv-overview.{gpu_vendor}.json"
    if not template_file.is_file():
        raise ValueError(f"缺少 {gpu_vendor} 仪表盘模板：{template_file}")
    return json.loads(template_file.read_text(encoding="utf-8-sig"))


def write_dashboard(filename, dashboard):
    output_file = OUTPUT_DIR / filename
    content = json.dumps(dashboard, ensure_ascii=False, indent=2) + "\n"
    if not output_file.exists() or output_file.read_text(encoding="utf-8") != content:
        temporary_file = output_file.with_suffix(".json.tmp")
        temporary_file.write_text(content, encoding="utf-8")
        temporary_file.replace(output_file)


def build_overview_dashboard(centers):
    panels = []
    for index, center in enumerate(centers):
        site = center["site"]
        site_name = center["site_name"]
        panels.append(
            {
                "id": index + 1,
                "type": "stat",
                "title": site_name,
                "description": f"开发机到 {site_name} Prometheus 指标接口的连接状态",
                "datasource": {"type": "prometheus", "uid": "prometheus"},
                "gridPos": {
                    "h": 6,
                    "w": 6,
                    "x": (index % 4) * 6,
                    "y": (index // 4) * 6,
                },
                "fieldConfig": {
                    "defaults": {
                        "color": {"mode": "thresholds"},
                        "links": [
                            {
                                "title": f"打开{site_name}仪表盘",
                                "url": f"/d/center-cv-{site}",
                            }
                        ],
                        "mappings": [
                            {
                                "type": "value",
                                "options": {
                                    "0": {
                                        "color": "red",
                                        "index": 0,
                                        "text": "无法连接",
                                    },
                                    "1": {
                                        "color": "green",
                                        "index": 1,
                                        "text": "正常",
                                    },
                                },
                            }
                        ],
                        "noValue": "等待检测",
                        "thresholds": {
                            "mode": "absolute",
                            "steps": [
                                {"color": "red", "value": None},
                                {"color": "green", "value": 1},
                            ],
                        },
                    },
                    "overrides": [],
                },
                "options": {
                    "colorMode": "background",
                    "graphMode": "none",
                    "justifyMode": "center",
                    "orientation": "auto",
                    "reduceOptions": {
                        "calcs": ["lastNotNull"],
                        "fields": "",
                        "values": False,
                    },
                    "showPercentChange": False,
                    "textMode": "auto",
                    "wideLayout": True,
                },
                "targets": [
                    {
                        "editorMode": "code",
                        "expr": f'up{{job="federate-centers",site="{site}"}}',
                        "instant": True,
                        "legendFormat": site_name,
                        "range": False,
                        "refId": "A",
                    }
                ],
            }
        )

    return {
        "annotations": {"list": []},
        "editable": False,
        "graphTooltip": 0,
        "id": None,
        "links": [],
        "panels": panels,
        "refresh": "30s",
        "schemaVersion": 42,
        "tags": ["generated", "中心总览"],
        "templating": {"list": []},
        "time": {"from": "now-6h", "to": "now"},
        "timezone": "browser",
        "title": "中心状态总览",
        "uid": "center-status-overview",
        "version": 1,
    }


def load_centers():
    groups = json.loads(CENTERS_FILE.read_text(encoding="utf-8-sig"))
    if not isinstance(groups, list):
        raise ValueError("centers.json 顶层必须是数组")

    centers = []
    seen_sites = set()
    for group in groups:
        if not isinstance(group, dict):
            raise ValueError("每个中心必须是对象")
        targets = group.get("targets")
        labels = group.get("labels")
        if not isinstance(targets, list) or not targets:
            raise ValueError("每个中心必须至少包含一个目标地址")
        if not isinstance(labels, dict):
            raise ValueError("每个中心必须包含 labels")
        site = labels.get("site", "")
        site_name = labels.get("site_name", "")
        gpu_vendor = labels.get("gpu_vendor", "unknown")
        if not SITE_PATTERN.fullmatch(site):
            raise ValueError(f"中心标识不合法：{site}")
        if site in seen_sites:
            raise ValueError(f"中心标识重复：{site}")
        if not site_name:
            raise ValueError(f"中心名称不能为空：{site}")
        if gpu_vendor not in SUPPORTED_GPU_VENDORS:
            raise ValueError(
                f"中心 {site} 的 gpu_vendor 必须是 nvidia 或 denglin"
            )
        seen_sites.add(site)
        target = targets[0]
        if not isinstance(target, str) or not target.strip():
            raise ValueError(f"中心 {site} 的目标地址不能为空")
        if len(targets) != 1:
            raise ValueError(f"中心 {site} 只能登记一个指标接口")
        target = target.strip().rstrip("/")
        if "://" in target:
            prometheus_url = target
        else:
            prometheus_url = f"http://{target}"
        if not prometheus_url.endswith("/prometheus"):
            prometheus_url += "/prometheus"
        elasticsearch_url = prometheus_url.removesuffix("/prometheus") + "/elasticsearch"
        centers.append(
            {
                "site": site,
                "site_name": site_name,
                "gpu_vendor": gpu_vendor,
                "prometheus_url": prometheus_url,
                "elasticsearch_url": elasticsearch_url,
                "prometheus_uid": f"{PROMETHEUS_UID_PREFIX}{site}",
                "elasticsearch_uid": f"{ELASTICSEARCH_UID_PREFIX}{site}",
            }
        )
    return centers


def generate():
    centers = load_centers()
    expected_files = {"overview.json"}

    for center in centers:
        site = center["site"]
        site_name = center["site_name"]
        gpu_vendor = center["gpu_vendor"]
        dashboard = load_template(gpu_vendor)
        dashboard["id"] = None
        dashboard["uid"] = f"center-cv-{site}"
        dashboard["title"] = f"{site_name}运行状态"
        tags = [tag for tag in dashboard.get("tags", []) if tag != "generated"]
        dashboard["tags"] = [*tags, "generated", site, gpu_vendor]
        set_center_datasources(
            dashboard,
            center["prometheus_uid"],
            center["elasticsearch_uid"],
        )
        filename = f"{site}.json"
        expected_files.add(filename)
        write_dashboard(filename, dashboard)

    write_dashboard("overview.json", build_overview_dashboard(centers))

    for output_file in OUTPUT_DIR.glob("*.json"):
        if output_file.name not in expected_files:
            output_file.unlink()

    return len(centers)


def grafana_request(path, method="GET", payload=None):
    credentials = base64.b64encode(
        f"{GRAFANA_USER}:{GRAFANA_PASSWORD}".encode("utf-8")
    ).decode("ascii")
    data = None
    headers = {"Authorization": f"Basic {credentials}"}
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(
        f"{GRAFANA_URL}{path}", data=data, headers=headers, method=method
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        body = response.read()
        return json.loads(body) if body else None


def prometheus_datasource_payload(center):
    return {
        "name": center["site_name"],
        "uid": center["prometheus_uid"],
        "type": "prometheus",
        "access": "proxy",
        "url": center["prometheus_url"],
        "isDefault": False,
        "editable": False,
        "jsonData": {
            "httpMethod": "GET",
            "timeInterval": "15s",
            "manageAlerts": False,
        },
    }


def elasticsearch_datasource_payload(center, logs_password):
    return {
        "name": f"{center['site_name']}日志",
        "uid": center["elasticsearch_uid"],
        "type": "elasticsearch",
        "access": "proxy",
        "url": center["elasticsearch_url"],
        "isDefault": False,
        "editable": False,
        "basicAuth": True,
        "basicAuthUser": LOGS_USERNAME,
        "jsonData": {
            "index": "center-cv-logs*",
            "timeField": "@timestamp",
            "logMessageField": "message",
            "esVersion": "8.19.0",
            "maxConcurrentShardRequests": 5,
            "timeInterval": "15s",
        },
        "secureJsonData": {"basicAuthPassword": logs_password},
    }


def datasource_needs_update(current, expected):
    if any(
        (
            current.get("name") != expected["name"],
            current.get("type") != expected["type"],
            current.get("access") != expected["access"],
            current.get("url", "").rstrip("/") != expected["url"].rstrip("/"),
            current.get("basicAuth", False) != expected.get("basicAuth", False),
        )
    ):
        return True
    current_json = current.get("jsonData", {})
    return any(current_json.get(key) != value for key, value in expected["jsonData"].items())


def sync_datasources(centers, force_secure_update=False):
    logs_password = LOGS_PASSWORD_FILE.read_text(encoding="utf-8").strip()
    if not logs_password:
        raise ValueError("中心日志查询密码不能为空")
    current_datasources = grafana_request("/api/datasources")
    current_by_uid = {item.get("uid"): item for item in current_datasources}
    expected_uids = {
        uid
        for center in centers
        for uid in (center["prometheus_uid"], center["elasticsearch_uid"])
    }

    for center in centers:
        payloads = (
            (prometheus_datasource_payload(center), "指标", False),
            (elasticsearch_datasource_payload(center, logs_password), "日志", True),
        )
        for payload, label, has_secret in payloads:
            uid = payload["uid"]
            current = current_by_uid.get(uid)
            if current is None:
                grafana_request("/api/datasources", method="POST", payload=payload)
                print(f"已创建 {center['site_name']} {label}数据源", flush=True)
            elif datasource_needs_update(current, payload) or (
                has_secret and force_secure_update
            ):
                quoted_uid = urllib.parse.quote(uid, safe="")
                grafana_request(
                    f"/api/datasources/uid/{quoted_uid}", method="PUT", payload=payload
                )
                print(f"已更新 {center['site_name']} {label}数据源", flush=True)

    for uid in current_by_uid:
        is_generated = uid and uid.startswith(
            (PROMETHEUS_UID_PREFIX, ELASTICSEARCH_UID_PREFIX)
        )
        if is_generated and uid not in expected_uids:
            quoted_uid = urllib.parse.quote(uid, safe="")
            grafana_request(f"/api/datasources/uid/{quoted_uid}", method="DELETE")
            print(f"已删除过期中心数据源 {uid}", flush=True)


def main():
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    last_signature = None
    while True:
        try:
            centers = load_centers()
            signature = (
                CENTERS_FILE.read_bytes(),
                LOGS_PASSWORD_FILE.read_bytes(),
                tuple(
                    (path.name, path.read_bytes())
                    for path in sorted(TEMPLATES_DIR.glob("center-cv-overview.*.json"))
                ),
            )
            signature_changed = signature != last_signature
            if signature_changed:
                count = generate()
                print(f"已生成 {count} 个中心仪表盘", flush=True)
                last_signature = signature
            sync_datasources(centers, force_secure_update=signature_changed)
        except Exception as error:
            print(f"仪表盘生成失败：{error}", flush=True)
        time.sleep(REFRESH_SECONDS)


if __name__ == "__main__":
    main()

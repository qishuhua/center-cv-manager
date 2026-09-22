#!/bin/sh
set -eu

kibana_url="http://127.0.0.1:5601/kibana"
data_view_id="center-cv-logs"

until curl -fsS "$kibana_url/api/status" >/dev/null; do
  echo "等待 Kibana 就绪..."
  sleep 2
done

if curl -fsS "$kibana_url/api/data_views/data_view/$data_view_id" >/dev/null 2>&1; then
  echo "更新 Kibana Data View: $data_view_id"
  curl -fsS -X POST "$kibana_url/api/data_views/data_view/$data_view_id" \
    -H 'kbn-xsrf: true' -H 'Content-Type: application/json' \
    -d '{"data_view":{"name":"Center CV Logs","title":"center-cv-logs","timeFieldName":"@timestamp","allowNoIndex":true},"refresh_fields":true}' >/dev/null
else
  echo "创建 Kibana Data View: $data_view_id"
  curl -fsS -X POST "$kibana_url/api/data_views/data_view" \
    -H 'kbn-xsrf: true' -H 'Content-Type: application/json' \
    -d '{"data_view":{"id":"center-cv-logs","name":"Center CV Logs","title":"center-cv-logs","timeFieldName":"@timestamp","allowNoIndex":true}}' >/dev/null
fi

curl -fsS -X POST "$kibana_url/api/data_views/default" \
  -H 'kbn-xsrf: true' -H 'Content-Type: application/json' \
  -d '{"data_view_id":"center-cv-logs","force":true}' >/dev/null

echo "Kibana 默认 Data View 已就绪: center-cv-logs"

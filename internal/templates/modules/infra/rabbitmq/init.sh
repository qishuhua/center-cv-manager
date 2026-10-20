#!/bin/sh
set -eu

: "${RABBITMQ_DEFAULT_VHOST:?RABBITMQ_DEFAULT_VHOST is required}"
: "${RABBITMQ_DEFAULT_USER:?RABBITMQ_DEFAULT_USER is required}"

# Fixed deployment topology. Task Ingress, CV workers, and video-transcode use
# the same defaults.
task_exchange="cv.task"
task_routing_key="task"
task_queue="cv.task"

alarm_exchange="cv.alarm"
alarm_transcode_routing_key="transcode"
alarm_transcode_queue="cv.alarm.transcode"
alarm_report_routing_key="report"
alarm_report_queue="cv.alarm.report"

rabbitmqadmin --non-interactive declare vhost \
  --name "$RABBITMQ_DEFAULT_VHOST" \
  --default-queue-type quorum

rabbitmqadmin --non-interactive permissions declare \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --username "$RABBITMQ_DEFAULT_USER" \
  --configure '.*' \
  --write '.*' \
  --read '.*'

rabbitmqadmin --non-interactive declare exchange \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --name "$task_exchange" \
  --type direct \
  --durable true \
  --auto-delete false

rabbitmqadmin --non-interactive declare exchange \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --name "$alarm_exchange" \
  --type direct \
  --durable true \
  --auto-delete false

# RabbitMQ 4.3+ quorum queues support message priority 0..31 natively.
# Do not set x-max-priority (classic queues only). This policy priority is NOT
# an AMQP message priority. Publishers set realtime/playback priorities.
# Keep mutable queue behavior in a policy.
# reject-publish lets a publisher using confirms observe overload instead of
# silently discarding an older alarm message.
rabbitmqadmin --non-interactive --vhost "$RABBITMQ_DEFAULT_VHOST" policies declare \
  --name "cv-alarm-transcode" \
  --pattern '^cv[.]alarm[.]transcode$' \
  --definition '{"delivery-limit":5,"max-length":10000,"max-length-bytes":268435456,"overflow":"reject-publish"}' \
  --apply-to queues \
  --priority 10

rabbitmqadmin --non-interactive --vhost "$RABBITMQ_DEFAULT_VHOST" policies declare \
  --name "cv-alarm-report" \
  --pattern '^cv[.]alarm[.]report$' \
  --definition '{"delivery-limit":5,"max-length":10000,"max-length-bytes":268435456,"overflow":"reject-publish"}' \
  --apply-to queues \
  --priority 10

rabbitmqadmin --non-interactive --vhost "$RABBITMQ_DEFAULT_VHOST" policies declare \
  --name "cv-task" \
  --pattern '^cv[.]task$' \
  --definition '{"max-length-bytes":268435456,"overflow":"reject-publish"}' \
  --apply-to queues \
  --priority 10

rabbitmqadmin --non-interactive declare queue \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --name "$task_queue" \
  --type quorum \
  --durable true \
  --auto-delete false

rabbitmqadmin --non-interactive declare queue \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --name "$alarm_transcode_queue" \
  --type quorum \
  --durable true \
  --auto-delete false

rabbitmqadmin --non-interactive declare queue \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --name "$alarm_report_queue" \
  --type quorum \
  --durable true \
  --auto-delete false

rabbitmqadmin --non-interactive declare binding \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --source "$task_exchange" \
  --destination-type queue \
  --destination "$task_queue" \
  --routing-key "$task_routing_key"

rabbitmqadmin --non-interactive declare binding \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --source "$alarm_exchange" \
  --destination-type queue \
  --destination "$alarm_transcode_queue" \
  --routing-key "$alarm_transcode_routing_key"

rabbitmqadmin --non-interactive declare binding \
  --vhost "$RABBITMQ_DEFAULT_VHOST" \
  --source "$alarm_exchange" \
  --destination-type queue \
  --destination "$alarm_report_queue" \
  --routing-key "$alarm_report_routing_key"

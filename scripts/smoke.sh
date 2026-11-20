#!/bin/sh
# End-to-end smoke test against a running Docker Compose stack (make smoke, and CI):
# both health checks answer 200, and a notification sent through the API reaches "sent".
set -eu

API=${API:-http://localhost:8080}
WORKER="http://$(docker compose port worker 8081)" # host port can differ, see docker-compose.yml

fail() {
	echo "smoke test failed: $*" >&2
	exit 1
}

for url in "$API/health" "$WORKER/health"; do
	code=$(curl -s -o /dev/null -w '%{http_code}' "$url")
	[ "$code" = 200 ] || fail "$url answered $code"
	echo "ok   $url"
done

body=$(curl -s -X POST "$API/api/v1/notifications" -H 'Content-Type: application/json' \
	-d '{"recipient":"+905551234567","channel":"sms","content":"smoke test"}')
id=$(printf '%s' "$body" | sed -n 's/^{"id":"\([^"]*\)".*/\1/p')
[ -n "$id" ] || fail "create answered $body"
echo "ok   created $id"

# The worker should send it within a second; allow 30 for a slow runner.
i=0
while [ $i -lt 60 ]; do
	status=$(curl -s "$API/api/v1/notifications/$id" | sed -n 's/.*"status":"\([a-z]*\)".*/\1/p')
	case $status in
	sent)
		echo "ok   $id sent"
		exit 0
		;;
	failed | cancelled) fail "$id ended as $status" ;;
	esac
	i=$((i + 1))
	sleep 0.5
done
fail "$id still $status after 30 s"

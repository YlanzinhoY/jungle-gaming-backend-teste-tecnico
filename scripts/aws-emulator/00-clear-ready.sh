#!/bin/sh
set -eu

rm -f "${SQS_IAM_READY_PATH:-/var/lib/sqs-app-credentials/ready}"

#!/usr/bin/env python3
"""Provision SQS queues, least-privilege IAM users, and local credentials."""

from __future__ import annotations

import configparser
import json
import os
from pathlib import Path
from typing import Any

import boto3
from botocore.exceptions import ClientError


REGION = os.getenv("AWS_DEFAULT_REGION", "us-east-1")
ENDPOINT = os.getenv("AWS_ENDPOINT_URL", "http://localhost:4566")
ACCOUNT_ID = os.getenv("MINISTACK_ACCOUNT_ID", "000000000000")
APP_CREDENTIALS_PATH = Path(
    os.getenv(
        "SQS_APP_CREDENTIALS_PATH",
        "/var/lib/sqs-app-credentials/credentials",
    )
)
TOOLING_CREDENTIALS_PATH = Path(
    os.getenv(
        "SQS_TOOLING_CREDENTIALS_PATH",
        "/var/lib/sqs-tooling-credentials/credentials",
    )
)
READY_PATH = Path(
    os.getenv("SQS_IAM_READY_PATH", "/var/lib/sqs-app-credentials/ready")
)
JSON_CREDENTIALS_PATH = os.getenv("SQS_CREDENTIALS_JSON_PATH", "")

INPUT_QUEUE = os.getenv("SQS_INPUT_QUEUE", "wager-transactions.fifo")
INPUT_DLQ = os.getenv("SQS_INPUT_DLQ", "wager-transactions-dlq.fifo")
EVENT_QUEUE = os.getenv("SQS_EVENT_QUEUE", "wager-events.fifo")
EVENT_DLQ = os.getenv("SQS_EVENT_DLQ", "wager-events-dlq.fifo")
MAX_RECEIVES = os.getenv("SQS_MAX_RECEIVES", "5")
VISIBILITY_TIMEOUT = os.getenv("SQS_VISIBILITY_TIMEOUT_SECONDS", "30")


def client(service: str):
    return boto3.client(service, region_name=REGION, endpoint_url=ENDPOINT)


iam = client("iam")
sqs = client("sqs")


def queue_arn(name: str) -> str:
    return f"arn:aws:sqs:{REGION}:{ACCOUNT_ID}:{name}"


def ensure_queue(name: str) -> str:
    response = sqs.create_queue(
        QueueName=name,
        Attributes={
            "FifoQueue": "true",
            "ContentBasedDeduplication": "false",
        },
    )
    return response["QueueUrl"]


def configure_queues() -> dict[str, str]:
    urls = {
        INPUT_DLQ: ensure_queue(INPUT_DLQ),
        EVENT_DLQ: ensure_queue(EVENT_DLQ),
        INPUT_QUEUE: ensure_queue(INPUT_QUEUE),
        EVENT_QUEUE: ensure_queue(EVENT_QUEUE),
    }
    sqs.set_queue_attributes(
        QueueUrl=urls[INPUT_QUEUE],
        Attributes={
            "VisibilityTimeout": VISIBILITY_TIMEOUT,
            "RedrivePolicy": json.dumps(
                {
                    "deadLetterTargetArn": queue_arn(INPUT_DLQ),
                    "maxReceiveCount": MAX_RECEIVES,
                },
                separators=(",", ":"),
            ),
        },
    )
    sqs.set_queue_attributes(
        QueueUrl=urls[EVENT_QUEUE],
        Attributes={
            "VisibilityTimeout": VISIBILITY_TIMEOUT,
            "RedrivePolicy": json.dumps(
                {
                    "deadLetterTargetArn": queue_arn(EVENT_DLQ),
                    "maxReceiveCount": MAX_RECEIVES,
                },
                separators=(",", ":"),
            ),
        },
    )
    return urls


def ensure_user(name: str) -> None:
    try:
        iam.create_user(UserName=name)
    except ClientError as error:
        if error.response["Error"]["Code"] != "EntityAlreadyExists":
            raise


def ensure_policy(name: str, document: dict[str, Any], user: str) -> None:
    arn = f"arn:aws:iam::{ACCOUNT_ID}:policy/{name}"
    try:
        iam.get_policy(PolicyArn=arn)
    except ClientError as error:
        if error.response["Error"]["Code"] != "NoSuchEntity":
            raise
        iam.create_policy(
            PolicyName=name,
            PolicyDocument=json.dumps(document, separators=(",", ":")),
        )
    iam.attach_user_policy(UserName=user, PolicyArn=arn)


def existing_profiles(path: Path) -> configparser.ConfigParser:
    parser = configparser.ConfigParser()
    if path.exists():
        parser.read(path)
    return parser


def ensure_access_key(
    user: str,
    profile: str,
    stored: configparser.ConfigParser,
) -> dict[str, str]:
    listed = iam.list_access_keys(UserName=user).get("AccessKeyMetadata", [])
    active_ids = {
        item["AccessKeyId"]
        for item in listed
        if item.get("Status", "Active") == "Active"
    }
    if stored.has_section(profile):
        access_key = stored.get(profile, "aws_access_key_id", fallback="")
        secret_key = stored.get(profile, "aws_secret_access_key", fallback="")
        if access_key in active_ids and secret_key:
            return {"accessKeyId": access_key, "secretAccessKey": secret_key}

    for item in listed:
        iam.delete_access_key(UserName=user, AccessKeyId=item["AccessKeyId"])
    created = iam.create_access_key(UserName=user)["AccessKey"]
    return {
        "accessKeyId": created["AccessKeyId"],
        "secretAccessKey": created["SecretAccessKey"],
    }


def write_credentials(path: Path, profiles: dict[str, dict[str, str]]) -> None:
    parser = configparser.ConfigParser()
    for profile, credentials in profiles.items():
        parser[profile] = {
            "aws_access_key_id": credentials["accessKeyId"],
            "aws_secret_access_key": credentials["secretAccessKey"],
            "region": REGION,
        }
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".tmp")
    with temporary.open("w", encoding="utf-8") as output:
        parser.write(output)
    temporary.chmod(0o644)
    temporary.replace(path)


def allow(actions: list[str], resources: list[str] | str) -> dict[str, Any]:
    return {"Effect": "Allow", "Action": actions, "Resource": resources}


def main() -> None:
    READY_PATH.unlink(missing_ok=True)
    configure_queues()

    definitions = {
        "application": {
            "user": "jungle-gaming-application",
            "policy": "jungle-gaming-application-sqs-v1",
            "document": {
                "Version": "2012-10-17",
                "Statement": [
                    allow(["sqs:GetQueueUrl"], "*"),
                    allow(
                        [
                            "sqs:GetQueueAttributes",
                            "sqs:ReceiveMessage",
                            "sqs:DeleteMessage",
                            "sqs:ChangeMessageVisibility",
                            "sqs:SendMessage",
                        ],
                        "*",
                    ),
                ],
            },
        },
        "provider-publisher": {
            "user": "jungle-gaming-provider-publisher",
            "policy": "jungle-gaming-provider-publisher-sqs-v1",
            "document": {
                "Version": "2012-10-17",
                "Statement": [
                    allow(["sqs:GetQueueUrl"], "*"),
                    allow(["sqs:SendMessage"], "*"),
                ],
            },
        },
        "event-consumer": {
            "user": "jungle-gaming-event-consumer",
            "policy": "jungle-gaming-event-consumer-sqs-v1",
            "document": {
                "Version": "2012-10-17",
                "Statement": [
                    allow(["sqs:GetQueueUrl"], "*"),
                    allow(
                        [
                            "sqs:GetQueueAttributes",
                            "sqs:ReceiveMessage",
                            "sqs:DeleteMessage",
                            "sqs:ChangeMessageVisibility",
                        ],
                        "*",
                    ),
                ],
            },
        },
        "test-harness": {
            "user": "jungle-gaming-test-harness",
            "policy": "jungle-gaming-test-harness-sqs-v1",
            "document": {
                "Version": "2012-10-17",
                "Statement": [
                    allow(["sqs:GetQueueUrl"], "*"),
                    allow(["sqs:*"], "*"),
                ],
            },
        },
        "denied": {
            "user": "jungle-gaming-denied",
            "policy": "",
            "document": {},
        },
    }

    app_stored = existing_profiles(APP_CREDENTIALS_PATH)
    tooling_stored = existing_profiles(TOOLING_CREDENTIALS_PATH)
    credentials: dict[str, dict[str, str]] = {}
    for profile, definition in definitions.items():
        user = definition["user"]
        ensure_user(user)
        if definition["policy"]:
            ensure_policy(definition["policy"], definition["document"], user)
        stored = app_stored if profile == "application" else tooling_stored
        credentials[profile] = ensure_access_key(user, profile, stored)

    write_credentials(APP_CREDENTIALS_PATH, {"application": credentials["application"]})
    write_credentials(
        TOOLING_CREDENTIALS_PATH,
        {profile: value for profile, value in credentials.items() if profile != "application"},
    )
    if JSON_CREDENTIALS_PATH:
        json_path = Path(JSON_CREDENTIALS_PATH)
        json_path.parent.mkdir(parents=True, exist_ok=True)
        temporary = json_path.with_suffix(".tmp")
        temporary.write_text(json.dumps(credentials), encoding="utf-8")
        temporary.chmod(0o600)
        temporary.replace(json_path)

    READY_PATH.parent.mkdir(parents=True, exist_ok=True)
    READY_PATH.write_text("ready\n", encoding="utf-8")
    READY_PATH.chmod(0o644)


if __name__ == "__main__":
    main()

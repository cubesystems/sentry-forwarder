#!/usr/bin/env sh

TAG=cubesystems/sentry-forwarder:1.0

docker buildx build . \
  -t $TAG \
  --platform linux/amd64 \
  --push

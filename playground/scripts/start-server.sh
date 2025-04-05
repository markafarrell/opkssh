#!/bin/bash
set -eou pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

cd $SCRIPT_DIR/..

docker build \
    --build-arg GOOGLE_USER_EMAIL=${GOOGLE_USER_EMAIL} \
    --build-arg MICROSOFT_USER_EMAIL=${MICROSOFT_USER_EMAIL} \
    --build-arg GITLAB_USER_EMAIL=${GITLAB_USER_EMAIL} \
    -t opkssh-server \
    -f container-files/server/Dockerfile .

docker stop opkssh-server 2>&1 >/dev/null || true
docker rm opkssh-server 2>&1 >/dev/null || true

docker run -d --rm \
    -e LOG_STDOUT=true \
    --name=opkssh-server \
    --net=opkssh-net \
    opkssh-server:latest

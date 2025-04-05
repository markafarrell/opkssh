#!/bin/bash
set -eou pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

cd $SCRIPT_DIR/../..

mkdir -p playground/.cache
mkdir -p playground/.mod-cache

docker run --rm \
    -v "$PWD":/data/ \
    -w /data \
    --user=$(id -g):$(id -g) \
    -v ${PWD}/playground/.cache:/.cache \
    -v ${PWD}/playground/.mod-cache:/go/pkg/mod \
    golang:1.24.2-alpine \
    go build -v -o playground/opkssh

chmod +x playground/opkssh

docker network create opkssh-net

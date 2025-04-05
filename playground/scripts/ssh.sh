#!/bin/bash
set -eou pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

cd $SCRIPT_DIR/..

ssh \
    -o StrictHostKeyChecking=no \
    -o IdentityAgent=none \
    -o UserKnownHostsFile=/dev/null \
    -i .ssh/id_ecdsa \
    $1@$(docker inspect opkssh-server | jq -r '.[0].NetworkSettings.Networks["opkssh-net"].IPAddress') \
    -p 2222 \
    ${@:2}
    
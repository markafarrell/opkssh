#!/bin/bash
set -eou pipefail

docker stop opkssh-server || true

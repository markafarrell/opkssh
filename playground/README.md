# opkssh playground

## Preparation

1. Run `./scripts/prep.sh`

## Starting the target ssh server

1. Run `./scripts/start-server.sh`

## Login to host

```bash
mkdir -p .ssh
./opkssh login --print-id-token -i .ssh/id_ecdsa
./scripts/ssh.sh bob
```